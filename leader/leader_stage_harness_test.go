// Stage-based multi-node test harness (step 1 of the leader.audit.md
// remediation plan, see ~/devel/ergo.services/leader.audit.md, "Fix plan" /
// "Step 1"). Before this file, the suite had zero testing/stage tests: two
// leader.Actor instances had never talked to each other, and the three wire
// types in init.go (msgVote/msgVoteReply/msgHeartbeat) had never been
// serialized across a real connection.
//
// Everything here observes the library only through its public surface -
// HandleInspect via the real gen.Node.Inspect request path, connection state via
// gen.Network - never by reaching into actor fields, exactly as a real consumer
// (TORA/KORA/SORA) would.
//
// RED-by-default canary: TestCanary_N1_TwoNodeClusterElectsAtMostOneLeader is
// t.Skip'd so `go test ./...` stays green. Delete its t.Skip line to see it fail
// for the reason named in the skip message, and restore the skip afterward. Do
// NOT "fix" it by editing leader.go/init.go in this step - that is step 4 of the
// remediation plan, a separate reviewed change requiring a coordinated release
// with every consumer (see the audit's step 4 "Breakage" note).
package leader

import (
	"fmt"
	"testing"
	"time"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/stage"
)

// ---------------------------------------------------------------------------
// A. Multi-node cluster harness.
// ---------------------------------------------------------------------------

// stageLeaderBehavior is a minimal ActorBehavior for stage tests. It takes only
// what leader.Options already exposes (ClusterID, Bootstrap) and implements
// only Init, which has no default. Every other callback comes from *Actor. Kept
// intentionally trivial: stage tests observe the library only through its public
// surface, never by reaching into fields, the same as TORA/KORA/SORA do.
type stageLeaderBehavior struct {
	Actor

	clusterID      string
	bootstrap      []gen.ProcessID
	minClusterSize int
}

func factoryStageLeader(clusterID string, bootstrap []gen.ProcessID) gen.ProcessFactory {
	return factoryStageLeaderMin(clusterID, bootstrap, 0)
}

func factoryStageLeaderMin(clusterID string, bootstrap []gen.ProcessID, minClusterSize int) gen.ProcessFactory {
	return func() gen.ProcessBehavior {
		return &stageLeaderBehavior{
			clusterID:      clusterID,
			bootstrap:      bootstrap,
			minClusterSize: minClusterSize,
		}
	}
}

func (s *stageLeaderBehavior) Init(args ...any) (Options, error) {
	return Options{
		ClusterID:      s.clusterID,
		Bootstrap:      s.bootstrap,
		MinClusterSize: s.minClusterSize,
	}, nil
}

func (s *stageLeaderBehavior) HandleBecomeLeader() error            { return nil }
func (s *stageLeaderBehavior) HandleBecomeFollower(_ gen.PID) error { return nil }

// leaderCluster is a set of real nodes, each running one leader.Actor-derived
// process, meshed together.
type leaderCluster struct {
	nodes []*stage.Node
	pids  []gen.PID
}

// leaderRegName is the name every cluster member registers under. Join/Bootstrap
// address peers as gen.ProcessID{Name, Node}, so a registered name is required -
// exactly how a real consumer (TORA/KORA/SORA) spawns its controller.
const leaderRegName gen.Atom = "leader"

// newLeaderCluster starts n real nodes on s, spawns one leader.Actor-derived
// behavior per node (registered as leaderRegName), meshes them, and configures
// every node's Bootstrap list with every OTHER node's ProcessID. Bootstrap is the
// library's own documented discovery mechanism (leader.Options.Bootstrap), not a
// test-only shortcut: once each node's own election timer fires, becomeCandidate
// fans its vote request out to every bootstrap entry (leader.go:435-440), which
// is enough for mutual discovery in both directions (the receiver's discoverPeer
// on the incoming vote, and the sender's discoverPeer on the reply that comes
// back) - the same mechanism Join uses, just pre-configured instead of dynamic.
//
// Wire types are registered per node before anything is spawned, which is the contract
// the library places on whoever starts it - the package registers nothing itself.
func newLeaderCluster(t *testing.T, s *stage.Stage, clusterID string, n int) *leaderCluster {
	t.Helper()

	c := &leaderCluster{}
	for i := 0; i < n; i++ {
		node := s.StartNode(fmt.Sprintf("n%d", i))
		// The caller registers the wire types, before anything is spawned - the same
		// contract a consumer has to honour through ApplicationSpec.Network.
		if err := node.Native().Network().RegisterTypes(NetworkTypes()); err != nil {
			t.Fatalf("newLeaderCluster: RegisterTypes on %s: %s", node.Name(), err)
		}
		if err := node.Native().Network().RegisterErrors(ErrorTypes()); err != nil {
			t.Fatalf("newLeaderCluster: RegisterErrors on %s: %s", node.Name(), err)
		}
		c.nodes = append(c.nodes, node)
	}

	s.ConnectMesh(c.nodes...)

	for i, node := range c.nodes {
		var bootstrap []gen.ProcessID
		for j, peer := range c.nodes {
			if j == i {
				continue
			}
			bootstrap = append(bootstrap, gen.ProcessID{Name: leaderRegName, Node: peer.Name()})
		}
		// Floor 1: this harness exercises view convergence, not the floor. The floor is
		// covered by TestCanary_N1_IsolatedNodesRespectTheFloor.
		pid := node.SpawnRegister(leaderRegName,
			factoryStageLeaderMin(clusterID, bootstrap, 1), gen.ProcessOptions{})
		c.pids = append(c.pids, pid)
	}
	return c
}

// minLeaderStability is the minimum time assertExactlyOneLeader requires a
// single-leader reading to hold before it is trusted. It must exceed the widest
// possible gap between two independently-drawn election timeouts (the default
// range is [150,300)ms, so the widest possible gap between any two draws in that
// range is bounded below 150ms) - otherwise a broken quorum that lets two nodes
// self-elect on their own independent timers (N1) would still produce a
// transient window, between the FIRST self-election and the SECOND, where
// exactly one node reports as leader, and a single instantaneous poll landing in
// that window would wrongly read as "converged". Set comfortably above that
// bound so only genuine, sustained single-leadership can satisfy it.
const minLeaderStability = 400 * time.Millisecond

// assertExactlyOneLeader polls the given nodes/pids - via the real per-node
// inspection RPC, gen.Node.Inspect, which drives HandleInspect's "leader" field,
// never by reaching into actor state - until exactly one reports leadership and
// holds that reading for minLeaderStability, or within passes without that ever
// happening. nodes[i] must be the node hosting pids[i] (gen.Node.Inspect only
// reaches a local process).
func assertExactlyOneLeader(t *testing.T, nodes []*stage.Node, pids []gen.PID, within time.Duration) {
	t.Helper()
	const tick = 20 * time.Millisecond

	deadline := time.Now().Add(within)
	var stableSince time.Time
	var lastFlags []bool

	for {
		flags := make([]bool, len(pids))
		leaders := 0
		for i, pid := range pids {
			result, err := nodes[i].Native().Inspect(pid)
			if err == nil && result["ergo:leader"] == "true" {
				flags[i] = true
				leaders++
			}
		}
		lastFlags = flags

		if leaders == 1 {
			if stableSince.IsZero() {
				stableSince = time.Now()
			}
			if time.Since(stableSince) >= minLeaderStability {
				return
			}
		} else {
			stableSince = time.Time{}
		}

		if time.Now().After(deadline) {
			t.Fatalf("leader cluster: never settled on exactly one leader (stable for >= %s) within %s; "+
				"last observed per-node leader flags: %v", minLeaderStability, within, lastFlags)
		}
		time.Sleep(tick)
	}
}

// partition severs connectivity between every node in groupA and every node in
// groupB, from both directions of each pair.
//
// The audit's fix plan names "remote.Disconnect()" as the API to use for this.
// That free function does not exist. The real API is a method on the
// gen.RemoteNode a node's own Network().Node()/GetNode() returns
// (gen/network.go:264-266): "Disconnect closes the connection to the remote
// node. All processes with links/monitors to remote processes will receive
// down/exit messages." Confirmed against the only two places in the whole
// framework that call it: testing/tests/distributed/link_test.go:132 and
// monitor_test.go:107, both via
// `remote, _ := n1.Native().Network().Node(n2.Name()); remote.Disconnect()`.
// Both directions are severed here (not just one) so the pair cannot keep
// talking through a connection the other side never dropped.
func partition(groupA, groupB []*stage.Node) {
	for _, a := range groupA {
		for _, b := range groupB {
			sever(a, b)
			sever(b, a)
		}
	}
}

func sever(from, to *stage.Node) {
	remote, err := from.Native().Network().Node(to.Name())
	if err != nil {
		// already disconnected from this side
		return
	}
	remote.Disconnect()
}

// heal reconnects every pair partition severed. stage.Connect dials and waits
// deterministically until both sides have registered the link before
// returning, mirroring how a real cluster's own traffic re-establishes a
// connection once the network recovers.
func heal(s *stage.Stage, groupA, groupB []*stage.Node) {
	for _, a := range groupA {
		for _, b := range groupB {
			s.Connect(a, b)
		}
	}
}

// pollUntil polls cond until it returns true or within elapses, returning
// whether cond was ever observed true.
func pollUntil(within time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(within)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// Harness sanity checks (green, defect-neutral): prove the plumbing above works
// on its own, independent of any of the audited defects, so a canary failure
// below can be trusted to be about the library and not about this harness.
// ---------------------------------------------------------------------------

func TestStageHarness_ClusterDiscoversPeers(t *testing.T) {
	s := stage.New(t)
	c := newLeaderCluster(t, s, "stage-cluster", 2)

	ok := pollUntil(2*time.Second, func() bool {
		for i, pid := range c.pids {
			result, err := c.nodes[i].Native().Inspect(pid)
			if err != nil || result["ergo:peers"] != "1" {
				return false
			}
		}
		return true
	})
	if ok == false {
		t.Fatalf("cluster of %d nodes did not converge to peers=1 on every node in time", len(c.nodes))
	}
}

func TestStageHarness_PartitionAndHeal(t *testing.T) {
	s := stage.New(t)
	a := s.StartNode("a")
	b := s.StartNode("b")
	s.Connect(a, b)

	partition([]*stage.Node{a}, []*stage.Node{b})

	// Disconnect() tears the connection down asynchronously (it closes the pool's
	// TCP connections and their read loops deregister on the resulting EOF/error),
	// so the node's own connection registry does not necessarily reflect it the
	// instant Disconnect() returns - poll rather than assert immediately.
	ok := pollUntil(time.Second, func() bool {
		_, errA := a.Native().Network().Node(b.Name())
		_, errB := b.Native().Network().Node(a.Name())
		return errA != nil && errB != nil
	})
	if ok == false {
		t.Fatalf("partition: connection between a and b was not torn down in time")
	}

	heal(s, []*stage.Node{a}, []*stage.Node{b})

	if _, err := a.Native().Network().Node(b.Name()); err != nil {
		t.Fatalf("heal: a does not see a connection to b: %s", err)
	}
	if _, err := b.Native().Network().Node(a.Name()); err != nil {
		t.Fatalf("heal: b does not see a connection to a: %s", err)
	}
}

// ---------------------------------------------------------------------------
// C. RED-by-default canary.
// ---------------------------------------------------------------------------

// N1 - quorum is a majority of peers, not of the cluster: len(l.peers)/2 + 1 is 1
// whether len(peers) is 0 or 1, and votes already starts at 1 for the self-vote.
//
// The two nodes are deliberately NEVER connected. Meshing them and racing their
// election timers was tried first and is not a reliable reproduction: the node
// that times out first starts heartbeating, and that heartbeat resets the other
// node's timer (leader.go:575) before it fires, so the entirely-correct "a
// follower defers to a live leader" mechanism suppresses the second campaign
// almost every time. Two nodes that never talk share only a ClusterID - the only
// membership input the library takes, which is L8 - and each self-elects once its
// own timer fires, giving stable dual leadership instead of a race.
// Two nodes configured for the same cluster that can never reach each other. With
// MinClusterSize 2 neither may operate alone, so neither takes leadership.
//
// This test used to assert "at most one leader" against the unfixed quorum
// arithmetic, where a lone node was a majority of its own view. That arithmetic is
// now correct - a node with zero peers is a legitimate single-node cluster and does
// self-elect - so what needs pinning is the floor, which is the mechanism that
// actually stops a fragment from operating.
func TestCanary_N1_IsolatedNodesRespectTheFloor(t *testing.T) {
	s := stage.New(t)
	a := s.StartNode("a")
	b := s.StartNode("b")

	factory := factoryStageLeaderMin("n1-canary", nil, 2)
	pidA := a.SpawnRegister(leaderRegName, factory, gen.ProcessOptions{})
	pidB := b.SpawnRegister(leaderRegName, factory, gen.ProcessOptions{})

	// Well past several election timeouts: neither node may ever lead.
	time.Sleep(time.Second)

	for _, probe := range []struct {
		node *stage.Node
		pid  gen.PID
	}{{a, pidA}, {b, pidB}} {
		result, err := probe.node.Native().Inspect(probe.pid)
		if err != nil {
			t.Fatalf("inspect %s: %s", probe.pid, err)
		}
		if result["ergo:leader"] != "false" {
			t.Fatalf("%s took leadership below the floor: leader=%s view_size=%s min_cluster_size=%s",
				probe.pid, result["ergo:leader"], result["ergo:view_size"], result["ergo:min_cluster_size"])
		}
		if result["ergo:state"] != "unclustered" {
			t.Fatalf("%s must report unclustered, got %q", probe.pid, result["ergo:state"])
		}
	}
}
