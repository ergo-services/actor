package leader

// Step 3 of the remediation plan in ~/devel/ergo.services/leader.audit.md:
// observability. These are green - the step changes no election behaviour, it only
// makes the existing behaviour visible. They pin L4, N17, N12, N16 and the logging
// half of N21 so a later step cannot quietly drop the surface again.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/check"
	"ergo.services/ergo/testing/unit"
)

// obsLeader records the callbacks the default Actor implements as no-ops, and
// deliberately does NOT override HandleInspect - TestLeader does, which is why the
// real one could only ever be tested by calling it directly.
type obsLeader struct {
	Actor

	minClusterSize int
	ghostTTL       int
	kinds          []gen.ProcessKind
}

func (o *obsLeader) Init(args ...any) (Options, error) {
	return Options{
		ClusterID:      args[0].(string),
		MinClusterSize: minClusterSizeOrOne(o.minClusterSize),
		GhostTTL:       o.ghostTTL,
	}, nil
}

func (o *obsLeader) HandleBecomeLeader() error            { return nil }
func (o *obsLeader) HandleBecomeFollower(_ gen.PID) error { return nil }

func factoryObsLeader() gen.ProcessFactory {
	return func() gen.ProcessBehavior { return &obsLeader{} }
}

// spawnObs spawns an obsLeader with peers pre-seeded so it cannot win an election
// on its own, which is the only way to observe the candidate state.
func spawnObs(t *testing.T, peers int) (*unit.Subject, *obsLeader) {
	t.Helper()

	actor, err := spawnLeader(t, factoryObsLeader(), gen.ProcessOptions{}, "obs-cluster")
	check.NoError(t, err)

	behavior := actor.Behavior().(*obsLeader)
	actor.OnSetProcessKind(func(kind gen.ProcessKind) error {
		behavior.kinds = append(behavior.kinds, kind)
		return nil
	})

	for i := 1; i <= peers; i++ {
		seedPeer(&behavior.Actor, testPeerPID(i))
	}
	return actor, behavior
}

// L4 / N17: the state that had to be reconstructed from transport counters during
// the incident is now one Inspect call.
func TestObservability_InspectReportsFullElectionState(t *testing.T) {
	actor, behavior := spawnObs(t, 2)

	// 2 peers means quorum 2, so this campaign cannot conclude and the actor stays
	// a candidate - the state that was previously indistinguishable from a healthy
	// follower on every surface.
	actor.FireTimers()

	result, err := actor.Inspect(gen.PID{})
	check.NoError(t, err)

	check.Equal(t, "candidate", result["ergo:state"], "a campaigning replica must not read as a follower")
	check.Equal(t, "false", result["ergo:leader"])
	check.Equal(t, "1", result["ergo:term"])
	check.Equal(t, actor.PID().String(), result["ergo:voted_for"], "a candidate votes for itself")
	check.Equal(t, "2", result["ergo:peers"])
	check.Equal(t, "2", result["ergo:quorum"])
	check.Equal(t, "0", result["ergo:votes_count"])
	check.Equal(t, "never", result["ergo:heartbeat_in_last"])
	check.Equal(t, "never", result["ergo:heartbeat_out_last"])

	// Peer identities, not just a count: a ghost peer inflating quorum is
	// invisible in a number.
	for i := 1; i <= 2; i++ {
		check.Contains(t, result["ergo:peers_list"], testPeerPID(i).String())
	}

	// Whether a timer is armed separates a follower waiting to campaign from one that
	// has stopped electing entirely. A candidate that missed quorum must be waiting to
	// try again.
	check.Equal(t, "true", result["ergo:election_timer_armed"],
		"a candidate re-arms its own election timer")
	check.Equal(t, "false", result["ergo:heartbeat_timer_armed"])

	check.NotEqual(t, "", result["ergo:term_changed_at"])
	_ = behavior
}

// L4: item filtering, and the help key that lists what can be asked for.
func TestObservability_InspectItemFiltering(t *testing.T) {
	actor, _ := spawnObs(t, 2)

	result, err := actor.Inspect(gen.PID{}, "ergo:state", "ergo:term")
	check.NoError(t, err)
	check.Equal(t, 2, len(result), "only the requested items come back")
	check.Equal(t, "follower", result["ergo:state"])
	check.Equal(t, "0", result["ergo:term"])

	unknown, err := actor.Inspect(gen.PID{}, "nope")
	check.NoError(t, err)
	check.Equal(t, "<unknown item>", unknown["nope"], "an unknown item is reported, not silently absent")

	help, err := actor.Inspect(gen.PID{}, "help")
	check.NoError(t, err)
	for _, key := range []string{"ergo:state", "ergo:voted_for", "ergo:peers_list", "ergo:quorum", "ergo:election_timer_armed", "ergo:dropped_by_reason"} {
		check.Contains(t, help["ergo:help"], key)
	}
}

// N17: the framework-visible kind follows the role, so process_list and the
// observer stop showing a stuck candidate as a healthy follower.
func TestObservability_ProcessKindFollowsRole(t *testing.T) {
	actor, behavior := spawnObs(t, 2)

	actor.FireTimers()
	check.Equal(t, []gen.ProcessKind{processKindCandidate}, behavior.kinds)

	// A higher-term heartbeat stands the candidate down. The kind is now reported
	// unconditionally; it used to be set only when a *leader* was demoted, so a
	// candidate standing down kept reporting "candidate" forever.
	actor.SendMessage(testPeerPID(1), msgHeartbeat{ClusterID: "obs-cluster", Term: 9, Leader: testPeerPID(1)})
	check.Equal(t, []gen.ProcessKind{processKindCandidate, gen.ProcessKindFollower}, behavior.kinds)
}

// N16: a ClusterID mismatch drops the message, the peer and the term it reports.
// It must never be silent - an empty ClusterID on an otherwise valid reply is L1,
// and until it is logged there is nothing on either node to point at it.
func TestObservability_ClusterIDMismatchIsLoggedAndCounted(t *testing.T) {
	actor, behavior := spawnObs(t, 0)

	stranger := gen.PID{Node: "stranger@host", ID: 1, Creation: 1}
	actor.SendMessage(stranger, msgVote{ClusterID: "other-cluster", Term: 3, Candidate: stranger})
	actor.SendMessage(stranger, msgVoteReply{ClusterID: "", Term: 3, Granted: false})
	actor.SendMessage(stranger, msgHeartbeat{ClusterID: "other-cluster", Term: 3, Leader: stranger})

	check.Equal(t, 0, len(behavior.peers), "a mismatching sender must not be discovered")
	check.Equal(t, uint64(0), behavior.term, "nor may its term be adopted")

	actor.ShouldLog().Level(gen.LogLevelWarning).Containing("does not match").Times(3).Assert()

	result, err := actor.Inspect(gen.PID{})
	check.NoError(t, err)
	for _, reason := range []string{
		"cluster_id_mismatch_vote=1",
		"cluster_id_mismatch_vote_reply=1",
		"cluster_id_mismatch_heartbeat=1",
	} {
		check.Contains(t, result["ergo:dropped_by_reason"], reason)
	}
}

// N2's receiver half, made visible: a stale heartbeat means the sender still
// believes it leads a term we have left. The correcting reply arrives in step 6;
// until then at least the drop is counted and logged.
func TestObservability_StaleHeartbeatIsLoggedAndCounted(t *testing.T) {
	actor, behavior := spawnObs(t, 0)

	behavior.term = 10
	stale := testPeerPID(1)
	actor.SendMessage(stale, msgHeartbeat{ClusterID: "obs-cluster", Term: 4, Leader: stale})

	actor.ShouldLog().Level(gen.LogLevelWarning).Containing("still claims leadership").Once().Assert()

	result, err := actor.Inspect(gen.PID{})
	check.NoError(t, err)
	check.Contains(t, result["ergo:dropped_by_reason"], "stale_heartbeat=1")
}

// N21, logging half: a peer that swallows every send becomes a counter and one log
// line per state change, instead of twenty discarded errors a second.
func TestObservability_FailingPeerSendsAreAttributedAndCounted(t *testing.T) {
	actor, behavior := spawnObs(t, 2)

	peer := testPeerPID(1)
	actor.OnSend(peer).Fail(gen.ErrNoConnection)

	// A 3-node view needs 2 votes, so leadership comes from peer2's grant while every
	// send to peer1 fails. The failure is logged once, on the transition into failing,
	// not once per attempt.
	actor.FireTimers()
	actor.SendMessage(testPeerPID(2), msgVoteReply{ClusterID: "obs-cluster", Term: 1, Granted: true})
	check.True(t, behavior.IsLeader(), "self plus one grant is a majority of three")

	actor.FireTimers()
	actor.FireTimers()

	actor.ShouldLog().Level(gen.LogLevelWarning).Containing("is failing").Once().Assert()

	result, err := actor.Inspect(gen.PID{})
	check.NoError(t, err)
	check.Contains(t, result["ergo:send_failing_peers"], peer.String())
	// One vote request, becomeLeader's immediate heartbeat, then two ticks.
	check.True(t, strings.Contains(result["ergo:send_failing_peers"], "=4"),
		"every failed send is attributed to the peer: "+result["ergo:send_failing_peers"])
}

// Join had 0.0% coverage, which is how L5 and N14 both went unnoticed in the one
// discovery API every consumer calls. It still populates nothing and returns
// nothing (L5, step 9); what is pinned here is that its target is recorded, so the
// latched membership a replica actually attempted is visible.
func TestObservability_JoinRecordsItsTargets(t *testing.T) {
	actor, behavior := spawnObs(t, 0)

	peer := gen.ProcessID{Name: "leader", Node: "peer1@host"}
	behavior.Join(peer)

	result, err := actor.Inspect(gen.PID{})
	check.NoError(t, err)
	check.Equal(t, "peer1@host=pending", result["ergo:declared"],
		"the peer is a member before it has answered")
	check.Equal(t, "2", result["ergo:view_size"], "and it counts toward the view")
	check.Equal(t, "0", result["ergo:peers"], "while its PID is still unknown")

	actor.ShouldSend().To(peer).Message(msgVote{
		ClusterID: "obs-cluster",
		Term:      0,
		Candidate: actor.PID(),
	}).Once().Assert()
}

// confirmLeader is a fixture whose HandleConfirmLeader answer is scripted, so the
// gate can be tested without any external system.
type confirmLeader struct {
	Actor

	confirm    bool
	confirmErr error
	calls      int
	becameLead int
}

func (c *confirmLeader) Init(args ...any) (Options, error) {
	// Fixed timeouts: the denial backoff is asserted in absolute terms below.
	return Options{
		ClusterID:          args[0].(string),
		ElectionTimeoutMin: 100,
		ElectionTimeoutMax: 200,
		HeartbeatInterval:  50,
		MinClusterSize:     1,
	}, nil
}

func (c *confirmLeader) HandleConfirmLeader() (bool, error) {
	c.calls++
	return c.confirm, c.confirmErr
}

func (c *confirmLeader) HandleBecomeLeader() error {
	c.becameLead++
	return nil
}

func (c *confirmLeader) HandleBecomeFollower(_ gen.PID) error { return nil }

func spawnConfirm(t *testing.T, confirm bool, confirmErr error) (*unit.Subject, *confirmLeader) {
	t.Helper()

	factory := func() gen.ProcessBehavior {
		return &confirmLeader{confirm: confirm, confirmErr: confirmErr}
	}
	actor, err := spawnLeader(t, factory, gen.ProcessOptions{}, "confirm-cluster")
	check.NoError(t, err)
	return actor, actor.Behavior().(*confirmLeader)
}

// The default implementation grants leadership, so embedding Actor and ignoring
// the callback keeps the previous behaviour exactly.
func TestConfirmLeader_DefaultGrants(t *testing.T) {
	actor, behavior := spawnObs(t, 0)

	actor.FireTimers()

	check.True(t, behavior.IsLeader(), "the default HandleConfirmLeader returns true")
	result, err := actor.Inspect(gen.PID{})
	check.NoError(t, err)
	check.Equal(t, "0", result["ergo:confirm_denied"])
}

// A denial withholds leadership entirely: HandleBecomeLeader must not run, since a
// consumer uses it to start the very work leadership authorises.
func TestConfirmLeader_DenialWithholdsLeadership(t *testing.T) {
	actor, behavior := spawnConfirm(t, false, nil)

	actor.FireTimers()

	check.Equal(t, 1, behavior.calls, "the gate is consulted once per won election")
	check.Equal(t, 0, behavior.becameLead, "HandleBecomeLeader must not run when leadership is withheld")
	check.False(t, behavior.IsLeader())
	check.Equal(t, gen.PID{}, behavior.Leader(), "no leader is published")

	actor.ShouldLog().Level(gen.LogLevelWarning).Containing("leadership withheld").Once().Assert()

	result, err := actor.Inspect(gen.PID{})
	check.NoError(t, err)
	check.Equal(t, "1", result["ergo:confirm_denied"])
	check.Equal(t, "follower", result["ergo:state"])
	check.NotEqual(t, "never", result["ergo:last_denied_at"])
}

// An error is not consent. Failing closed matters because the common failure is a
// timeout talking to the authority, which is exactly when another node may hold it.
func TestConfirmLeader_ErrorIsTreatedAsDenial(t *testing.T) {
	actor, behavior := spawnConfirm(t, true, gen.ErrTimeout)

	actor.FireTimers()

	check.False(t, behavior.IsLeader(), "an error must not grant leadership even with confirm=true")
	check.Equal(t, 0, behavior.becameLead)
	actor.ShouldLog().Level(gen.LogLevelWarning).Containing("HandleConfirmLeader failed").Once().Assert()
}

// A persistently denied node must settle into slow polling. Re-campaigning at the
// normal election timeout would hammer the authority for as long as the partition
// lasts, and the denial says nothing about this candidate being wrong.
func TestConfirmLeader_DenialBacksOff(t *testing.T) {
	actor, behavior := spawnConfirm(t, false, nil)

	// ElectionTimeoutMax is 100..200ms, so backoff is 200ms * consecutive denials.
	for i := 1; i <= 3; i++ {
		actor.FireTimers()
		// No Message filter: the message carries a generation, so matching it by value
		// would need the current generation. The delay is what this test is about.
		actor.ShouldSendAfter().
			After(time.Duration(i) * 200 * time.Millisecond).
			Once().Assert()
	}

	check.Equal(t, 3, behavior.calls)
	result, err := actor.Inspect(gen.PID{})
	check.NoError(t, err)
	check.Equal(t, "3", result["ergo:confirm_denied"])
	check.Equal(t, "true", result["ergo:election_timer_armed"], "the node keeps polling, slowly")
}

// N1: the quorum threshold must be a majority of the view, which includes this node.
// Taken over len(peers) instead it is short by one for every even cluster size and
// degenerates to 1 at a single peer.
func TestQuorum_MajorityOfTheViewIncludingSelf(t *testing.T) {
	for _, tc := range []struct{ peers, view, quorum int }{
		{0, 1, 1},
		{1, 2, 2},
		{2, 3, 2},
		{3, 4, 3},
		{4, 5, 3},
		{5, 6, 4},
		{6, 7, 4},
	} {
		_, behavior := spawnObs(t, tc.peers)
		check.Equal(t, tc.view, behavior.viewSize(),
			fmt.Sprintf("%d peers is a view of %d", tc.peers, tc.view))
		check.Equal(t, tc.quorum, behavior.quorum(),
			fmt.Sprintf("view %d needs %d votes", tc.view, tc.quorum))
	}
}

// The case the old arithmetic got wrong: with one peer the threshold was 1, which the
// self-vote alone satisfied, so a node took leadership having heard from nobody.
func TestQuorum_SinglePeerDoesNotSelfElect(t *testing.T) {
	actor, behavior := spawnObs(t, 1)

	actor.FireTimers()

	check.False(t, behavior.IsLeader(), "one grant is still needed in a 2-node view")
	check.Equal(t, "candidate", inspectKey(t, actor, "ergo:state"))

	actor.SendMessage(testPeerPID(1), msgVoteReply{ClusterID: "obs-cluster", Term: 1, Granted: true})
	check.True(t, behavior.IsLeader(), "and the grant makes it leader")
}

// The floor applies continuously. Without that, one survivor of a larger cluster is a
// majority of its own view and keeps leadership indefinitely.
func TestMinClusterSize_LeaderStepsDownWhenViewShrinksBelowFloor(t *testing.T) {
	factory := func() gen.ProcessBehavior { return &obsLeader{minClusterSize: 3} }
	actor, err := spawnLeader(t, factory, gen.ProcessOptions{}, "obs-cluster")
	check.NoError(t, err)
	behavior := actor.Behavior().(*obsLeader)

	for i := 1; i <= 2; i++ {
		seedPeer(&behavior.Actor, testPeerPID(i))
	}

	actor.FireTimers()
	actor.SendMessage(testPeerPID(1), msgVoteReply{ClusterID: "obs-cluster", Term: 1, Granted: true})
	check.True(t, behavior.IsLeader(), "a view of 3 with 2 votes elects")

	// One peer goes: view 2, below the floor.
	actor.SendMessage(actor.PID(), gen.MessageDownPID{PID: testPeerPID(1), Reason: gen.TerminateReasonNormal})

	check.False(t, behavior.IsLeader(), "leadership must not survive the view falling below the floor")
	check.Equal(t, "unclustered", inspectKey(t, actor, "ergo:state"))
	actor.ShouldLog().Level(gen.LogLevelWarning).Containing("stepping down").Once().Assert()
}

// A node below the floor must not campaign at all, and must say why.
func TestMinClusterSize_BelowFloorDoesNotCampaign(t *testing.T) {
	factory := func() gen.ProcessBehavior { return &obsLeader{minClusterSize: 3} }
	actor, err := spawnLeader(t, factory, gen.ProcessOptions{}, "obs-cluster")
	check.NoError(t, err)
	behavior := actor.Behavior().(*obsLeader)

	seedPeer(&behavior.Actor, testPeerPID(1)) // view of 2

	actor.FireTimers()

	check.Equal(t, uint64(0), behavior.term, "no term is burned below the floor")
	check.False(t, behavior.IsLeader())
	check.Equal(t, "unclustered", inspectKey(t, actor, "ergo:state"))
	actor.ShouldLog().Level(gen.LogLevelWarning).Containing("not campaigning").Once().Assert()
	check.Equal(t, "true", inspectKey(t, actor, "ergo:election_timer_armed"), "it keeps waiting for peers")
}

func inspectKey(t *testing.T, actor *unit.Subject, key string) string {
	t.Helper()
	result, err := actor.Inspect(gen.PID{}, key)
	check.NoError(t, err)
	return result[key]
}

// A behavior that overrides HandleInspect adds to the election state and may replace an
// individual field, but cannot erase the rest - the actor's own data is computed first
// and the behavior's map is merged over it.
type inspectLeader struct {
	Actor
}

func (i *inspectLeader) Init(args ...any) (Options, error) {
	return Options{ClusterID: args[0].(string), MinClusterSize: 1}, nil
}

func (i *inspectLeader) HandleBecomeLeader() error            { return nil }
func (i *inspectLeader) HandleBecomeFollower(_ gen.PID) error { return nil }

func (i *inspectLeader) HandleInspect(from gen.PID, item ...string) map[string]string {
	return map[string]string{
		"consumer_field": "mine",
		// Not a collision any more: the election state lives under "ergo:cluster".
		"cluster": "mine too",
		// A reserved key, overridden on purpose - which is the only way to do it.
		"ergo:state": "deliberately-replaced",
	}
}

func TestObservability_ConsumerInspectDoesNotEraseElectionState(t *testing.T) {
	actor, err := spawnLeader(t, func() gen.ProcessBehavior { return &inspectLeader{} },
		gen.ProcessOptions{}, "obs-cluster")
	check.NoError(t, err)

	result, err := actor.Inspect(gen.PID{})
	check.NoError(t, err)

	check.Equal(t, "mine", result["consumer_field"], "the consumer's own field is present")
	check.Equal(t, "mine too", result["cluster"], "and its own key stays its own")
	check.Equal(t, "obs-cluster", result["ergo:cluster"],
		"an unprefixed lookalike does not clobber a reserved key")
	check.Equal(t, "deliberately-replaced", result["ergo:state"],
		"a reserved key is overridden only by naming it")

	for _, key := range []string{"ergo:term", "ergo:view_size", "ergo:quorum", "ergo:min_cluster_size"} {
		check.NotEqual(t, "", result[key], "the rest of the election state survives: "+key)
	}
}

// The rolling-deploy wedge: with unique node names, every replaced pod leaves an
// unreachable member behind. Keeping those forever grows the quorum past the number of
// nodes that exist, and no leader can ever be elected again.
func TestGhostTTL_RollingDeployDoesNotInflateQuorum(t *testing.T) {
	factory := func() gen.ProcessBehavior {
		return &obsLeader{minClusterSize: 3, ghostTTL: 1}
	}
	actor, err := spawnLeader(t, factory, gen.ProcessOptions{}, "obs-cluster")
	check.NoError(t, err)
	behavior := actor.Behavior().(*obsLeader)

	live := []gen.PID{}
	for i := 1; i <= 4; i++ { // four peers plus self is a five-node cluster
		p := gen.PID{Node: gen.Atom(fmt.Sprintf("gen1-%d@ip", i)), ID: uint64(i), Creation: 1}
		seedPeer(&behavior.Actor, p)
		live = append(live, p)
	}
	check.Equal(t, 5, behavior.viewSize())
	check.Equal(t, 3, behavior.quorum())

	// Two rollouts. Each pod is deleted - the framework reports every process on a lost
	// node with ErrNoConnection - and its replacement joins under a brand new name.
	for round := 2; round <= 3; round++ {
		previous := live
		live = nil
		for i, old := range previous {
			actor.SendMessage(actor.PID(),
				gen.MessageDownPID{PID: old, Reason: gen.ErrNoConnection})

			p := gen.PID{Node: gen.Atom(fmt.Sprintf("gen%d-%d@ip", round, i+1)), ID: uint64(i), Creation: 1}
			seedPeer(&behavior.Actor, p)
			live = append(live, p)
		}

		// The harness has no clock to advance, so age the marks instead: what is under
		// test is the sweep, not the wall clock.
		for node := range behavior.Actor.unreachableSince {
			behavior.Actor.unreachableSince[node] = time.Now().Add(-time.Hour)
		}
		// A timer tick is what sweeps expired ghosts.
		actor.FireTimers()

		check.Equal(t, 5, behavior.viewSize(),
			fmt.Sprintf("rollout %d: the view must not carry names that will never return", round))
		check.Equal(t, 3, behavior.quorum(),
			fmt.Sprintf("rollout %d: quorum must stay assemblable by the nodes that exist", round))
	}
}

// The other half: a peer that is briefly unreachable stays in the view, so a blip does
// not lower quorum and hand a fragment the ability to elect.
func TestGhostTTL_BlipKeepsTheMemberInTheView(t *testing.T) {
	actor, behavior := spawnObs(t, 2) // three-node view
	peer := testPeerPID(1)

	actor.SendMessage(actor.PID(), gen.MessageDownPID{PID: peer, Reason: gen.ErrNoConnection})

	check.Equal(t, 3, behavior.viewSize(), "a dropped connection is not a departure")
	check.Equal(t, 2, behavior.quorum(), "so quorum does not move")
	check.Contains(t, inspectKey(t, actor, "ergo:unreachable"), string(peer.Node))

	// It comes back before the TTL expires.
	actor.SendMessage(peer, msgHeartbeat{ClusterID: "obs-cluster", Term: 1, Leader: peer})

	check.Equal(t, 3, behavior.viewSize())
	check.Equal(t, "", inspectKey(t, actor, "ergo:unreachable"), "no longer counted as unreachable")
}
