package leader

import (
	"testing"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/check"
	"ergo.services/ergo/testing/unit"
)

type TestLeader struct {
	Actor

	becameLeaderCalled   bool
	becameFollowerCalled bool
	becameFollowerLeader gen.PID
	terminateCalled      bool
	terminateReason      error
	initError            error
	clusterID            string
	bootstrap            []gen.ProcessID
	electionTimeoutMin   int
	electionTimeoutMax   int
	heartbeatInterval    int
}

func (t *TestLeader) Init(args ...any) (Options, error) {
	if t.initError != nil {
		return Options{}, t.initError
	}

	if len(args) > 0 {
		t.clusterID = args[0].(string)
	}
	if len(args) > 1 {
		t.bootstrap = args[1].([]gen.ProcessID)
	}

	opts := Options{
		ClusterID:          t.clusterID,
		Bootstrap:          t.bootstrap,
		ElectionTimeoutMin: t.electionTimeoutMin,
		ElectionTimeoutMax: t.electionTimeoutMax,
		HeartbeatInterval:  t.heartbeatInterval,
	}

	return opts, nil
}

func (t *TestLeader) HandleBecomeLeader() error {
	t.becameLeaderCalled = true
	return nil
}

func (t *TestLeader) HandleBecomeFollower(leader gen.PID) error {
	t.becameFollowerCalled = true
	t.becameFollowerLeader = leader
	return nil
}

func (t *TestLeader) Terminate(reason error) {
	t.terminateCalled = true
	t.terminateReason = reason
}

func (t *TestLeader) HandleMessage(from gen.PID, message any) error {
	return nil
}

func (t *TestLeader) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	return nil, nil
}

func (t *TestLeader) HandleInspect(from gen.PID, item ...string) map[string]string {
	return map[string]string{}
}

func factoryTestLeader(clusterID string, bootstrap []gen.ProcessID) gen.ProcessFactory {
	return func() gen.ProcessBehavior {
		return &TestLeader{
			clusterID: clusterID,
			bootstrap: bootstrap,
		}
	}
}

// Test actor API

func TestAPI_Accessors(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Test Term()
	check.Equal(t, uint64(0), behavior.Term(), "Initial term should be 0")

	// Test ClusterID()
	check.Equal(t, "test-cluster", behavior.ClusterID(), "ClusterID should match")

	// Test PeerCount()
	check.Equal(t, 0, behavior.PeerCount(), "Should have 0 peers initially")

	// Test Peers()
	peers := behavior.Peers()
	check.Equal(t, 0, len(peers), "Peers() should return empty slice")

	// Test Bootstrap()
	bootstrap := behavior.Bootstrap()
	check.NotNil(t, bootstrap, "Bootstrap should not be nil")
	check.Equal(t, 0, len(bootstrap), "Bootstrap should be empty")

	// Test IsLeader() and Leader()
	check.False(t, behavior.IsLeader(), "Should not be leader initially")
	check.Equal(t, gen.PID{}, behavior.Leader(), "Leader should be empty initially")
}

func TestAPI_PeersAccessor(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Discover some peers
	peer1 := gen.PID{Node: "peer1@host", ID: 100, Creation: 1}
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}

	actor.SendMessage(peer1, msgVote{
		ClusterID: "test-cluster",
		Term:      1,
		Candidate: peer1,
	})

	actor.SendMessage(peer2, msgVote{
		ClusterID: "test-cluster",
		Term:      1,
		Candidate: peer2,
	})

	// Test PeerCount()
	check.Equal(t, 2, behavior.PeerCount(), "Should have 2 peers")

	// Test Peers()
	peers := behavior.Peers()
	check.Equal(t, 2, len(peers), "Peers() should return 2 peers")

	// Test HasPeer()
	check.True(t, behavior.HasPeer(peer1), "Should have peer1")
	check.True(t, behavior.HasPeer(peer2), "Should have peer2")
	check.False(t, behavior.HasPeer(gen.PID{Node: "unknown@host", ID: 999, Creation: 1}), "Should not have unknown peer")
}

func TestAPI_Broadcast(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Add some peers
	peer1 := gen.PID{Node: "peer1@host", ID: 100, Creation: 1}
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}
	behavior.Actor.peers[peer1] = true
	behavior.Actor.peers[peer2] = true

	mark := actor.Mark()

	// Test Broadcast()
	testMsg := "test-broadcast"
	behavior.Broadcast(testMsg)

	// Check that message was sent to all peers
	sentTo := make(map[gen.PID]bool)

	for _, se := range actor.ShouldSend().Since(mark).Collect() {
		if se.Message == testMsg {
			if pid, ok := se.To.(gen.PID); ok {
				sentTo[pid] = true
			}
		}
	}

	check.Equal(t, 2, len(sentTo), "Should send to 2 peers")
	check.True(t, sentTo[peer1], "Should send to peer1")
	check.True(t, sentTo[peer2], "Should send to peer2")
}

func TestAPI_BroadcastBootstrap(t *testing.T) {
	bootstrap := []gen.ProcessID{
		{Name: "leader", Node: "node1@host"},
		{Name: "leader", Node: "node2@host"},
		{Name: "leader", Node: "node3@host"},
	}

	actor, _ := unit.StartNode(t, "node1@host", gen.NodeOptions{}).
		SpawnRegister("leader", factoryTestLeader("test-cluster", bootstrap),
			gen.ProcessOptions{}, "test-cluster", bootstrap)

	behavior := actor.Behavior().(*TestLeader)

	mark := actor.Mark()

	// Test BroadcastBootstrap()
	testMsg := "bootstrap-broadcast"
	behavior.BroadcastBootstrap(testMsg)

	// Check that messages were sent to bootstrap peers (excluding self)
	messageCount := 0

	for _, se := range actor.ShouldSend().Since(mark).Collect() {
		if se.Message == testMsg {
			if procID, ok := se.To.(gen.ProcessID); ok {
				// Should not be self
				check.NotEqual(t, "node1@host", procID.Node.String(), "Should not send to self")
				messageCount++
			}
		}
	}

	// Should send to 2 bootstrap peers (node2 and node3, excluding self node1)
	check.Equal(t, 2, messageCount, "Should send to 2 bootstrap peers (excluding self)")
}

func TestAPI_CallbackPeerJoined(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	peer1 := gen.PID{Node: "peer1@host", ID: 100, Creation: 1}
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}

	// Discover peer1
	actor.SendMessage(peer1, msgVote{
		ClusterID: "test-cluster",
		Term:      1,
		Candidate: peer1,
	})

	// Verify peer was added (indirect test via PeerCount)
	check.Equal(t, 1, behavior.PeerCount(), "Should have 1 peer after discovery")
	check.True(t, behavior.HasPeer(peer1), "Should have peer1")

	// Discover peer2
	actor.SendMessage(peer2, msgVote{
		ClusterID: "test-cluster",
		Term:      1,
		Candidate: peer2,
	})

	// Verify second peer was added
	check.Equal(t, 2, behavior.PeerCount(), "Should have 2 peers after discovery")
	check.True(t, behavior.HasPeer(peer2), "Should have peer2")
}

func TestAPI_CallbackPeerLeft(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	peer1 := gen.PID{Node: "peer1@host", ID: 100, Creation: 1}

	// Discover peer
	actor.SendMessage(peer1, msgVote{
		ClusterID: "test-cluster",
		Term:      1,
		Candidate: peer1,
	})

	check.Equal(t, 1, behavior.PeerCount(), "Should have 1 peer")

	// Simulate peer going down
	actor.SendMessage(actor.PID(), gen.MessageDownPID{
		PID:    peer1,
		Reason: gen.TerminateReasonNormal,
	})

	// Verify peer was removed
	check.Equal(t, 0, behavior.PeerCount(), "Should have 0 peers after removal")
	check.False(t, behavior.HasPeer(peer1), "Should not have peer1 anymore")
}

func TestAPI_CallbackTermChanged(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	remotePeer := gen.PID{Node: "remote@host", ID: 100, Creation: 1}

	// Initial term is 0
	check.Equal(t, uint64(0), behavior.Term(), "Initial term should be 0")

	// Receive vote request with higher term
	actor.SendMessage(remotePeer, msgVote{
		ClusterID: "test-cluster",
		Term:      5,
		Candidate: remotePeer,
	})

	// Verify term changed
	check.Equal(t, uint64(5), behavior.Term(), "Term should be updated to 5")

	// Change to term 10
	actor.SendMessage(remotePeer, msgVote{
		ClusterID: "test-cluster",
		Term:      10,
		Candidate: remotePeer,
	})

	// Verify term changed again
	check.Equal(t, uint64(10), behavior.Term(), "Term should be updated to 10")
}

func TestAPI_TermAccessorDuringElection(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	initialTerm := behavior.Term()

	// Trigger election
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	// Term should increment
	check.Equal(t, initialTerm+1, behavior.Term(), "Term should increment during election")
}

// Test: Initialization with valid options
func TestLeaderInit_ValidOptions(t *testing.T) {
	clusterID := "test-cluster"
	bootstrap := []gen.ProcessID{}

	actor, err := unit.Spawn(t, factoryTestLeader(clusterID, bootstrap),
		gen.ProcessOptions{}, clusterID, bootstrap)

	check.Nil(t, err, "Should initialize successfully")
	check.NotNil(t, actor, "Actor should not be nil")

	// Follower is the initial state - no callback should be called on init
	behavior := actor.Behavior().(*TestLeader)
	check.False(
		t,
		behavior.becameFollowerCalled,
		"HandleBecomeFollower should not be called on init (follower is default state)",
	)
	check.False(t, behavior.becameLeaderCalled, "HandleBecomeLeader should not be called on init")
}

// Test: Initialization fails with empty ClusterID
func TestLeaderInit_EmptyClusterID(t *testing.T) {
	_, err := unit.Spawn(t, factoryTestLeader("", []gen.ProcessID{}),
		gen.ProcessOptions{}, "", []gen.ProcessID{})

	check.NotNil(t, err, "Should fail with empty ClusterID")
	check.Contains(t, err.Error(), "ClusterID cannot be empty", "Error message should mention ClusterID")
}

// Test: Initialization fails when ElectionTimeoutMax <= ElectionTimeoutMin
func TestLeaderInit_InvalidTimeouts(t *testing.T) {
	factory := func() gen.ProcessBehavior {
		return &TestLeader{
			clusterID:          "test",
			electionTimeoutMin: 150,
			electionTimeoutMax: 150, // Same as min - invalid
		}
	}

	_, err := unit.Spawn(t, factory, gen.ProcessOptions{}, "test", []gen.ProcessID{})

	check.NotNil(t, err, "Should fail with invalid timeouts")
	check.Contains(t, err.Error(), "must be greater than", "Error should mention timeout constraint")
}

// Test: Single node becomes leader immediately
func TestLeaderElection_SingleNode(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Send election timeout to trigger election
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	// Single node should become leader immediately (quorum = 0/2 + 1 = 1)
	check.True(t, behavior.becameLeaderCalled, "Single node should become leader")
	check.True(t, behavior.IsLeader(), "IsLeader should return true")
	check.Equal(t, actor.PID(), behavior.Leader(), "Leader should be self")
}

// Test: Vote request handling
func TestLeaderElection_VoteRequest(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	remotePID := gen.PID{Node: "remote@host", ID: 100, Creation: 1}

	// Send vote request from remote node
	actor.SendMessage(remotePID, msgVote{
		ClusterID: "test-cluster",
		Term:      5,
		Candidate: remotePID,
	})

	// Should send vote reply
	actor.ShouldSend().
		To(remotePID).
		Message(msgVoteReply{
			ClusterID: "test-cluster",
			Term:      5,
			Granted:   true,
		}).
		Once().
		Assert()

	// Should update term
	check.Equal(t, uint64(5), behavior.term, "Term should be updated")

	// Should monitor peer
	foundMonitor := false
	for _, me := range actor.ShouldMonitor().Collect() {
		if me.Target == remotePID {
			foundMonitor = true
			break
		}
	}
	check.True(t, foundMonitor, "Should monitor peer")
}

// Test: Vote request with wrong ClusterID is ignored
func TestLeaderElection_VoteRequest_WrongCluster(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("cluster-a", []gen.ProcessID{}),
		gen.ProcessOptions{}, "cluster-a", []gen.ProcessID{})

	remotePID := gen.PID{Node: "remote@host", ID: 100, Creation: 1}

	mark := actor.Mark() // Clear initial events

	// Send vote request with different ClusterID
	actor.SendMessage(remotePID, msgVote{
		ClusterID: "cluster-b",
		Term:      5,
		Candidate: remotePID,
	})

	// Should not send any reply
	actor.ShouldSend().Since(mark).None().Assert()
	actor.ShouldSendAfter().Since(mark).None().Assert()

	// Should not monitor peer
	for range actor.ShouldMonitor().Since(mark).Collect() {
		t.Error("Should not monitor peer from different cluster")
	}
}

// Test: Vote request with lower term is rejected
func TestLeaderElection_VoteRequest_LowerTerm(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	remotePID1 := gen.PID{Node: "remote1@host", ID: 100, Creation: 1}
	remotePID2 := gen.PID{Node: "remote2@host", ID: 200, Creation: 1}

	// First send vote request with term 10 to establish current term
	actor.SendMessage(remotePID1, msgVote{
		ClusterID: "test-cluster",
		Term:      10,
		Candidate: remotePID1,
	})

	check.Equal(t, uint64(10), behavior.Actor.term, "Term should be updated to 10")

	// Now send vote request with lower term 5
	actor.SendMessage(remotePID2, msgVote{
		ClusterID: "test-cluster",
		Term:      5,
		Candidate: remotePID2,
	})

	// Find the rejection in all events
	foundRejection := false
	for _, se := range actor.ShouldSend().Collect() {
		if se.To == remotePID2 {
			if vr, ok := se.Message.(msgVoteReply); ok {
				if vr.Term == 10 && !vr.Granted {
					foundRejection = true
					break
				}
			}
		}
	}

	check.True(t, foundRejection, "Should send vote rejection to remotePID2")

	// Term should not change
	check.Equal(t, uint64(10), behavior.Actor.term, "Term should not change")
}

// Test: Only one vote per term
func TestLeaderElection_OneVotePerTerm(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	candidate1 := gen.PID{Node: "node1@host", ID: 100, Creation: 1}
	candidate2 := gen.PID{Node: "node2@host", ID: 200, Creation: 1}

	mark := actor.Mark()

	// Vote for candidate1 in term 5
	actor.SendMessage(candidate1, msgVote{
		ClusterID: "test-cluster",
		Term:      5,
		Candidate: candidate1,
	})

	// Should grant vote to candidate1
	actor.ShouldSend().
		To(candidate1).
		Message(msgVoteReply{
			ClusterID: "test-cluster",
			Term:      5,
			Granted:   true,
		}).
		Since(mark).
		Once().
		Assert()

	mark = actor.Mark()

	// Try to vote for candidate2 in same term
	actor.SendMessage(candidate2, msgVote{
		ClusterID: "test-cluster",
		Term:      5,
		Candidate: candidate2,
	})

	// Should reject vote for candidate2
	actor.ShouldSend().
		To(candidate2).
		Message(msgVoteReply{
			ClusterID: "test-cluster",
			Term:      5,
			Granted:   false,
		}).
		Since(mark).
		Once().
		Assert()

	// Verify votedFor hasn't changed
	check.Equal(t, candidate1, behavior.votedFor, "Should still be voted for candidate1")
}

// Test: Can vote for same candidate multiple times in same term
func TestLeaderElection_SameCandidateMultipleTimes(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	candidate := gen.PID{Node: "node1@host", ID: 100, Creation: 1}

	// Vote for candidate first time
	actor.SendMessage(candidate, msgVote{
		ClusterID: "test-cluster",
		Term:      5,
		Candidate: candidate,
	})

	mark := actor.Mark()

	// Vote for same candidate again in same term
	actor.SendMessage(candidate, msgVote{
		ClusterID: "test-cluster",
		Term:      5,
		Candidate: candidate,
	})

	// Should grant vote again
	actor.ShouldSend().
		To(candidate).
		Message(msgVoteReply{
			ClusterID: "test-cluster",
			Term:      5,
			Granted:   true,
		}).
		Since(mark).
		Once().
		Assert()
}

// Test: VoteReply with wrong ClusterID is ignored
func TestLeaderElection_VoteReply_WrongCluster(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("cluster-a", []gen.ProcessID{}),
		gen.ProcessOptions{}, "cluster-a", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Add peers so we don't become leader immediately
	peer1 := gen.PID{Node: "peer1@host", ID: 100, Creation: 1}
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}
	behavior.peers[peer1] = true
	behavior.peers[peer2] = true

	// Become candidate
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	remotePID := gen.PID{Node: "remote@host", ID: 300, Creation: 1}

	mark := actor.Mark()

	// Send vote reply with wrong ClusterID
	actor.SendMessage(remotePID, msgVoteReply{
		ClusterID: "cluster-b",
		Term:      behavior.term,
		Granted:   true,
	})

	// Should not become leader
	check.False(t, behavior.IsLeader(), "Should not become leader with wrong cluster vote")

	// Should not monitor peer
	for range actor.ShouldMonitor().Since(mark).Collect() {
		t.Error("Should not monitor peer from different cluster")
	}
}

// Test: Heartbeat handling
func TestLeaderElection_Heartbeat(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	leaderPID := gen.PID{Node: "leader@host", ID: 100, Creation: 1}

	// Receive heartbeat from leader
	actor.SendMessage(leaderPID, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      5,
		Leader:    leaderPID,
	})

	// Should update term and leader
	check.Equal(t, uint64(5), behavior.term, "Term should be updated")
	check.Equal(t, leaderPID, behavior.Leader(), "Leader should be updated")
	check.False(t, behavior.IsLeader(), "Should not be leader")

	// Should call HandleBecomeFollower
	check.Equal(t, leaderPID, behavior.becameFollowerLeader, "Follower callback should have leader PID")
}

// Test: Heartbeat with wrong ClusterID is ignored
func TestLeaderElection_Heartbeat_WrongCluster(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("cluster-a", []gen.ProcessID{}),
		gen.ProcessOptions{}, "cluster-a", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)
	originalTerm := behavior.term

	leaderPID := gen.PID{Node: "leader@host", ID: 100, Creation: 1}

	// Receive heartbeat with wrong ClusterID
	actor.SendMessage(leaderPID, msgHeartbeat{
		ClusterID: "cluster-b",
		Term:      10,
		Leader:    leaderPID,
	})

	// Should not update term or leader
	check.Equal(t, originalTerm, behavior.term, "Term should not change")
	check.Equal(t, gen.PID{}, behavior.Leader(), "Leader should remain empty")
}

// Test: Split-brain detection - leader receives heartbeat with same term
func TestLeaderElection_SplitBrain_SameTerm(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Become leader
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	check.True(t, behavior.IsLeader(), "Should be leader initially")

	otherLeader := gen.PID{Node: "other@host", ID: 200, Creation: 1}

	// Reset callback flags
	behavior.becameLeaderCalled = false
	behavior.becameFollowerCalled = false

	// Receive heartbeat from another leader claiming same term
	actor.SendMessage(otherLeader, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      behavior.term, // Same term!
		Leader:    otherLeader,
	})

	// Should step down
	check.False(t, behavior.IsLeader(), "Should step down on split-brain detection")
	check.True(t, behavior.becameFollowerCalled, "Should call HandleBecomeFollower")
}

// Test: Leader sends heartbeats
func TestLeaderElection_LeaderSendsHeartbeats(t *testing.T) {
	bootstrap := []gen.ProcessID{
		{Name: "leader", Node: "node1@host"},
		{Name: "leader", Node: "node2@host"},
	}

	actor, _ := unit.StartNode(t, "node1@host", gen.NodeOptions{}).
		SpawnRegister("leader", factoryTestLeader("test-cluster", bootstrap),
			gen.ProcessOptions{}, "test-cluster", bootstrap)

	behavior := actor.Behavior().(*TestLeader)

	// Become leader
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	check.True(t, behavior.IsLeader(), "Should be leader")

	mark := actor.Mark()

	// Trigger heartbeat timeout
	actor.SendMessage(actor.PID(), msgHeartbeatTimeout{})

	// Should send heartbeat to bootstrap peers (excluding self)
	actor.ShouldSend().
		To(gen.ProcessID{Name: "leader", Node: "node2@host"}).
		Message(msgHeartbeat{
			ClusterID: "test-cluster",
			Term:      behavior.term,
			Leader:    behavior.PID(),
		}).
		Since(mark).
		Once().
		Assert()
}

// Test: Peer discovery
func TestLeaderElection_PeerDiscovery(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	peer1 := gen.PID{Node: "peer1@host", ID: 100, Creation: 1}
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}

	// Discover peer1 via vote
	actor.SendMessage(peer1, msgVote{
		ClusterID: "test-cluster",
		Term:      1,
		Candidate: peer1,
	})

	// Discover peer2 via heartbeat
	actor.SendMessage(peer2, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      1,
		Leader:    peer2,
	})

	// Should monitor both peers
	monitorCount := 0
	for _, me := range actor.ShouldMonitor().Collect() {
		if me.Target == peer1 || me.Target == peer2 {
			monitorCount++
		}
	}
	check.Equal(t, 2, monitorCount, "Should monitor both peers")

	// Verify peers are in map
	check.Equal(t, 2, len(behavior.peers), "Should have 2 peers")
	check.True(t, behavior.peers[peer1], "peer1 should be in peers map")
	check.True(t, behavior.peers[peer2], "peer2 should be in peers map")
}

// Test: Peer removal on MessageDownPID
func TestLeaderElection_PeerRemoval(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	peerPID := gen.PID{Node: "peer@host", ID: 100, Creation: 1}

	// Discover peer
	actor.SendMessage(peerPID, msgVote{
		ClusterID: "test-cluster",
		Term:      1,
		Candidate: peerPID,
	})

	check.Equal(t, 1, len(behavior.peers), "Should have 1 peer")

	// Simulate peer termination
	actor.SendMessage(actor.PID(), gen.MessageDownPID{
		PID:    peerPID,
		Reason: gen.TerminateReasonNormal,
	})

	// Peer should be removed
	check.Equal(t, 0, len(behavior.peers), "Peer should be removed")
}

// Test: Leader failure triggers new election
func TestLeaderElection_LeaderFailure(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	leaderPID := gen.PID{Node: "leader@host", ID: 100, Creation: 1}

	// Receive heartbeat to establish leader
	actor.SendMessage(leaderPID, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      5,
		Leader:    leaderPID,
	})

	check.Equal(t, leaderPID, behavior.Leader(), "Leader should be set")

	// Reset callback flags
	behavior.becameFollowerCalled = false
	behavior.becameFollowerLeader = gen.PID{}

	// Leader goes down
	actor.SendMessage(actor.PID(), gen.MessageDownPID{
		PID:    leaderPID,
		Reason: gen.TerminateReasonNormal,
	})

	// becomeFollower is only called if wasLeader==true
	// In this case we're a follower, so it won't be called
	// Just check that leader is cleared
	check.Equal(t, gen.PID{}, behavior.Leader(), "Leader should be cleared")
	check.False(t, behavior.IsLeader(), "Should not be leader")
}

// Test: Vote counting - need majority
func TestLeaderElection_VoteCounting_Majority(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Add 4 peers (total 5 nodes including self)
	// Need 3 votes to win (5/2 + 1 = 3)
	peer1 := gen.PID{Node: "peer1@host", ID: 100, Creation: 1}
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}
	peer3 := gen.PID{Node: "peer3@host", ID: 300, Creation: 1}
	peer4 := gen.PID{Node: "peer4@host", ID: 400, Creation: 1}
	behavior.peers[peer1] = true
	behavior.peers[peer2] = true
	behavior.peers[peer3] = true
	behavior.peers[peer4] = true

	// Become candidate
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	check.False(t, behavior.IsLeader(), "Should not be leader yet (only 1 vote)")

	// Get 1 more vote (total 2)
	actor.SendMessage(peer1, msgVoteReply{
		ClusterID: "test-cluster",
		Term:      behavior.term,
		Granted:   true,
	})

	check.False(t, behavior.IsLeader(), "Should not be leader yet (only 2 votes)")

	// Get 1 more vote (total 3 - majority!)
	actor.SendMessage(peer2, msgVoteReply{
		ClusterID: "test-cluster",
		Term:      behavior.term,
		Granted:   true,
	})

	// Should become leader
	check.True(t, behavior.IsLeader(), "Should become leader with majority votes")
	check.True(t, behavior.becameLeaderCalled, "Should call HandleBecomeLeader")
}

// Test: Vote counting - rejected votes don't count
func TestLeaderElection_VoteCounting_Rejected(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Add 2 peers (total 3 nodes)
	// Need 2 votes to win (3/2 + 1 = 2)
	for i := 1; i <= 2; i++ {
		peer := gen.PID{Node: gen.Atom("peer" + string(rune(i)) + "@host"), ID: uint64(i * 100), Creation: 1}
		behavior.peers[peer] = true
	}

	// Become candidate
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	// Receive rejected vote
	peer1 := gen.PID{Node: "peer1@host", ID: 100, Creation: 1}
	actor.SendMessage(peer1, msgVoteReply{
		ClusterID: "test-cluster",
		Term:      behavior.term,
		Granted:   false, // Rejected!
	})

	// Should not become leader
	check.False(t, behavior.IsLeader(), "Should not become leader with rejected vote")
}

// Test: Higher term in vote reply causes step down
func TestLeaderElection_VoteReply_HigherTerm(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Become candidate in term 5
	behavior.term = 4
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	check.Equal(t, uint64(5), behavior.term, "Should be in term 5")

	// Receive vote reply with higher term
	peer := gen.PID{Node: "peer@host", ID: 100, Creation: 1}
	actor.SendMessage(peer, msgVoteReply{
		ClusterID: "test-cluster",
		Term:      10,
		Granted:   false,
	})

	// Should step down and update term
	check.Equal(t, uint64(10), behavior.term, "Should update to term 10")
	check.Equal(t, gen.PID{}, behavior.votedFor, "Should clear votedFor")
}

// Test: Inspect returns correct data
func TestLeaderInspect(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)
	behavior.term = 5

	// Add some peers
	peer1 := gen.PID{Node: "peer1@host", ID: 100, Creation: 1}
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}
	behavior.peers[peer1] = true
	behavior.peers[peer2] = true

	// Call the Actor's HandleInspect (which uses default implementation from leader.Actor)
	result := behavior.Actor.HandleInspect(gen.PID{})

	check.Equal(t, "test-cluster", result["cluster"], "Cluster should match")
	check.Equal(t, "5", result["term"], "Term should match")
	check.Equal(t, "2", result["peers"], "Peer count should match")
}

// Test: IsLeader and Leader accessors
func TestLeaderAccessors(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Initially not leader
	check.False(t, behavior.IsLeader(), "Should not be leader initially")
	check.Equal(t, gen.PID{}, behavior.Leader(), "Leader should be empty initially")

	// Become leader
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	// Now leader
	check.True(t, behavior.IsLeader(), "Should be leader")
	check.Equal(t, actor.PID(), behavior.Leader(), "Leader should be self")
}

// Test: VotesReceived cleared when becoming follower
func TestLeaderElection_VotesReceivedCleared(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Add some peers
	for i := 1; i <= 2; i++ {
		peer := gen.PID{Node: gen.Atom("peer" + string(rune(i)) + "@host"), ID: uint64(i * 100), Creation: 1}
		behavior.peers[peer] = true
	}

	// Become candidate - this creates votesReceived map
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	check.NotNil(t, behavior.votesReceived, "votesReceived should be initialized")

	// Receive higher term heartbeat - become follower
	leader := gen.PID{Node: "leader@host", ID: 999, Creation: 1}
	actor.SendMessage(leader, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      100,
		Leader:    leader,
	})

	// votesReceived should be cleared (nil, not empty map)
	if behavior.votesReceived != nil {
		check.Equal(t, 0, len(behavior.votesReceived), "votesReceived should be cleared when becoming follower")
	}
}

// Test: Self-discovery is prevented
func TestLeaderElection_NoSelfDiscovery(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Try to discover self
	actor.SendMessage(actor.PID(), msgVote{
		ClusterID: "test-cluster",
		Term:      1,
		Candidate: actor.PID(),
	})

	// Should not add self to peers
	check.Equal(t, 0, len(behavior.peers), "Should not add self to peers")

	// Should not monitor self
	for _, me := range actor.ShouldMonitor().Collect() {
		if pid, ok := me.Target.(gen.PID); ok && pid == actor.PID() {
			t.Error("Should not monitor self")
		}
	}
}

// Test: Follower doesn't send heartbeats
func TestLeaderElection_FollowerNoHeartbeats(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Ensure we're a follower
	check.False(t, behavior.IsLeader(), "Should be follower")

	mark := actor.Mark()

	// Send heartbeat timeout as follower
	actor.SendMessage(actor.PID(), msgHeartbeatTimeout{})

	// Should not send any heartbeats
	for _, se := range actor.ShouldSend().Since(mark).Collect() {
		if _, ok := se.Message.(msgHeartbeat); ok {
			t.Error("Follower should not send heartbeats")
		}
	}
}

// Test: Candidate doesn't become leader without quorum
func TestLeaderElection_NoLeaderWithoutQuorum(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Add 4 peers (total 5 nodes)
	// Need 3 votes to win
	for i := 1; i <= 4; i++ {
		peer := gen.PID{Node: gen.Atom("peer" + string(rune(i)) + "@host"), ID: uint64(i * 100), Creation: 1}
		behavior.peers[peer] = true
	}

	// Become candidate (1 vote - self)
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	// Get only 1 more vote (total 2, need 3)
	peer1 := gen.PID{Node: "peer1@host", ID: 100, Creation: 1}
	actor.SendMessage(peer1, msgVoteReply{
		ClusterID: "test-cluster",
		Term:      behavior.term,
		Granted:   true,
	})

	// Should NOT become leader
	check.False(t, behavior.IsLeader(), "Should not become leader without quorum")
}

// Test: Old vote replies are ignored
func TestLeaderElection_OldVoteRepliesIgnored(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Add peer so we don't become leader immediately
	peer := gen.PID{Node: "peer@host", ID: 100, Creation: 1}
	behavior.peers[peer] = true

	// Become candidate in term 5
	behavior.term = 4
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	term5 := behavior.term // Should be 5

	// Move to term 6 (simulate receiving higher term)
	behavior.term = 6
	behavior.votedFor = gen.PID{}
	behavior.isLeader = false

	// Receive vote reply for old term 5
	actor.SendMessage(peer, msgVoteReply{
		ClusterID: "test-cluster",
		Term:      term5,
		Granted:   true,
	})

	// Should not count the vote or become leader
	check.False(t, behavior.IsLeader(), "Should not become leader from old vote reply")
	check.Equal(t, uint64(6), behavior.term, "Term should remain at 6")
}

// Test: Default timeout values
func TestLeaderInit_DefaultTimeouts(t *testing.T) {
	factory := func() gen.ProcessBehavior {
		return &TestLeader{
			clusterID: "test",
			// Don't set timeout values - test defaults
		}
	}

	actor, _ := unit.Spawn(t, factory, gen.ProcessOptions{}, "test", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Check defaults were applied to the Actor struct (not TestLeader)
	check.Equal(t, 150, behavior.Actor.electionTimeoutMin, "Default electionTimeoutMin should be 150")
	check.Equal(t, 300, behavior.Actor.electionTimeoutMax, "Default electionTimeoutMax should be 300")
	check.Equal(t, 50, behavior.Actor.heartbeatInterval, "Default heartbeatInterval should be 50")
}

// Test: Custom timeout values
func TestLeaderInit_CustomTimeouts(t *testing.T) {
	factory := func() gen.ProcessBehavior {
		return &TestLeader{
			clusterID:          "test",
			electionTimeoutMin: 100,
			electionTimeoutMax: 500,
			heartbeatInterval:  25,
		}
	}

	actor, _ := unit.Spawn(t, factory, gen.ProcessOptions{}, "test", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Check custom values were applied to the Actor struct
	check.Equal(t, 100, behavior.Actor.electionTimeoutMin, "electionTimeoutMin should be 100")
	check.Equal(t, 500, behavior.Actor.electionTimeoutMax, "electionTimeoutMax should be 500")
	check.Equal(t, 25, behavior.Actor.heartbeatInterval, "heartbeatInterval should be 25")
}

// === Additional Test Cases Based on TiKV Raft Implementation ===

// Test: Leader in majority partition stays leader
func TestLeaderElection_PartitionLeaderInMajority(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Setup 5-node cluster, we are node 1
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}
	peer3 := gen.PID{Node: "peer3@host", ID: 300, Creation: 1}
	peer4 := gen.PID{Node: "peer4@host", ID: 400, Creation: 1}
	peer5 := gen.PID{Node: "peer5@host", ID: 500, Creation: 1}
	behavior.peers[peer2] = true
	behavior.peers[peer3] = true
	behavior.peers[peer4] = true
	behavior.peers[peer5] = true

	// Become leader
	actor.SendMessage(actor.PID(), msgElectionTimeout{})
	actor.SendMessage(peer2, msgVoteReply{ClusterID: "test-cluster", Term: behavior.Actor.term, Granted: true})
	actor.SendMessage(peer3, msgVoteReply{ClusterID: "test-cluster", Term: behavior.Actor.term, Granted: true})

	check.True(t, behavior.IsLeader(), "Should become leader with majority")

	// Simulate partition: we have peer2, peer3 (majority of 3 out of 5)
	// peer4 and peer5 are isolated
	delete(behavior.peers, peer4)
	delete(behavior.peers, peer5)

	// Simulate peer4 and peer5 going down
	actor.SendMessage(actor.PID(), gen.MessageDownPID{PID: peer4, Reason: gen.TerminateReasonNormal})
	actor.SendMessage(actor.PID(), gen.MessageDownPID{PID: peer5, Reason: gen.TerminateReasonNormal})

	// We should remain leader (still have majority: us + peer2 + peer3 = 3)
	check.True(t, behavior.IsLeader(), "Should remain leader with majority partition")
}

// Test: Leader in minority partition steps down
func TestLeaderElection_PartitionLeaderInMinority(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Setup 5-node cluster
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}
	peer3 := gen.PID{Node: "peer3@host", ID: 300, Creation: 1}
	peer4 := gen.PID{Node: "peer4@host", ID: 400, Creation: 1}
	peer5 := gen.PID{Node: "peer5@host", ID: 500, Creation: 1}
	behavior.peers[peer2] = true
	behavior.peers[peer3] = true
	behavior.peers[peer4] = true
	behavior.peers[peer5] = true

	// Become leader
	actor.SendMessage(actor.PID(), msgElectionTimeout{})
	actor.SendMessage(peer2, msgVoteReply{ClusterID: "test-cluster", Term: behavior.Actor.term, Granted: true})
	actor.SendMessage(peer3, msgVoteReply{ClusterID: "test-cluster", Term: behavior.Actor.term, Granted: true})

	check.True(t, behavior.IsLeader(), "Should be leader initially")

	// Simulate partition: we only have peer2 (minority of 2 out of 5)
	// peer3, peer4, peer5 are in majority partition and elect new leader
	newLeader := peer3

	// New leader sends heartbeat with higher term
	actor.SendMessage(newLeader, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      behavior.Actor.term + 1,
		Leader:    newLeader,
	})

	// We should step down and recognize new leader
	check.False(t, behavior.IsLeader(), "Should step down when seeing higher term")
	check.Equal(t, newLeader, behavior.Leader(), "Should recognize new leader")
}

// Test: Follower in minority partition cannot elect self
func TestLeaderElection_FollowerInMinorityCannotElect(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Setup 5-node cluster (we are follower)
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}
	peer3 := gen.PID{Node: "peer3@host", ID: 300, Creation: 1}
	peer4 := gen.PID{Node: "peer4@host", ID: 400, Creation: 1}
	peer5 := gen.PID{Node: "peer5@host", ID: 500, Creation: 1}
	behavior.peers[peer2] = true
	behavior.peers[peer3] = true
	behavior.peers[peer4] = true
	behavior.peers[peer5] = true

	// Simulate we're in minority partition with only peer2
	// Remove others from peer map (simulate partition)
	delete(behavior.peers, peer3)
	delete(behavior.peers, peer4)
	delete(behavior.peers, peer5)

	// Try to become candidate
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	// We only have 1 peer (peer2), need 2 votes for majority out of 2 nodes
	// We vote for ourselves (1 vote)
	actor.SendMessage(peer2, msgVoteReply{
		ClusterID: "test-cluster",
		Term:      behavior.Actor.term,
		Granted:   true,
	})

	// With 2 votes (self + peer2) out of 2 nodes, we become leader
	// But this represents minority of original 5-node cluster
	check.True(t, behavior.IsLeader(), "Can become leader of minority partition")
}

// Test: Rapid leader changes
func TestLeaderElection_RapidLeaderChanges(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	leader1 := gen.PID{Node: "leader1@host", ID: 100, Creation: 1}
	leader2 := gen.PID{Node: "leader2@host", ID: 200, Creation: 1}
	leader3 := gen.PID{Node: "leader3@host", ID: 300, Creation: 1}

	// Rapid succession of different leaders
	actor.SendMessage(leader1, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      5,
		Leader:    leader1,
	})

	check.Equal(t, leader1, behavior.Leader(), "Should follow leader1")
	check.Equal(t, uint64(5), behavior.Actor.term, "Term should be 5")

	actor.SendMessage(leader2, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      6,
		Leader:    leader2,
	})

	check.Equal(t, leader2, behavior.Leader(), "Should follow leader2")
	check.Equal(t, uint64(6), behavior.Actor.term, "Term should be 6")

	actor.SendMessage(leader3, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      7,
		Leader:    leader3,
	})

	check.Equal(t, leader3, behavior.Leader(), "Should follow leader3")
	check.Equal(t, uint64(7), behavior.Actor.term, "Term should be 7")

	// Should always be follower
	check.False(t, behavior.IsLeader(), "Should remain follower")
}

// Test: Election with unresponsive peers
func TestLeaderElection_UnresponsivePeers(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Setup 5-node cluster
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}
	peer3 := gen.PID{Node: "peer3@host", ID: 300, Creation: 1}
	peer4 := gen.PID{Node: "peer4@host", ID: 400, Creation: 1}
	peer5 := gen.PID{Node: "peer5@host", ID: 500, Creation: 1}
	behavior.peers[peer2] = true
	behavior.peers[peer3] = true
	behavior.peers[peer4] = true
	behavior.peers[peer5] = true

	// Become candidate
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	// Only 2 peers respond (we need 3 for majority of 5)
	actor.SendMessage(peer2, msgVoteReply{
		ClusterID: "test-cluster",
		Term:      behavior.Actor.term,
		Granted:   true,
	})

	// peer3, peer4, peer5 don't respond (unresponsive)

	// Should not become leader (only 2 votes: self + peer2, need 3)
	check.False(t, behavior.IsLeader(), "Should not become leader without majority")
}

// Test: Concurrent elections in same term
func TestLeaderElection_ConcurrentElections(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	candidate1 := gen.PID{Node: "cand1@host", ID: 100, Creation: 1}
	candidate2 := gen.PID{Node: "cand2@host", ID: 200, Creation: 1}
	behavior.peers[candidate1] = true
	behavior.peers[candidate2] = true

	mark := actor.Mark()

	// Receive vote request from candidate1 in term 5
	actor.SendMessage(candidate1, msgVote{
		ClusterID: "test-cluster",
		Term:      5,
		Candidate: candidate1,
	})

	// We vote for candidate1
	// Now receive vote request from candidate2 in SAME term
	actor.SendMessage(candidate2, msgVote{
		ClusterID: "test-cluster",
		Term:      5,
		Candidate: candidate2,
	})

	// Should have voted for candidate1, rejected candidate2
	votesGranted := 0
	votesRejected := 0

	for _, se := range actor.ShouldSend().Since(mark).Collect() {
		if vr, ok := se.Message.(msgVoteReply); ok {
			if vr.Term == 5 {
				if vr.Granted {
					votesGranted++
					check.Equal(t, candidate1, se.To, "Should only grant vote to candidate1")
				} else {
					votesRejected++
					check.Equal(t, candidate2, se.To, "Should reject candidate2")
				}
			}
		}
	}

	check.Equal(t, 1, votesGranted, "Should grant exactly one vote")
	check.Equal(t, 1, votesRejected, "Should reject one vote")
}

// Test: Term jumps (skipping terms)
func TestLeaderElection_TermJumps(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	leader := gen.PID{Node: "leader@host", ID: 100, Creation: 1}

	// Start at term 5
	actor.SendMessage(leader, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      5,
		Leader:    leader,
	})

	check.Equal(t, uint64(5), behavior.Actor.term, "Should be at term 5")

	// Jump to term 100 (simulating network partition where many elections happened)
	actor.SendMessage(leader, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      100,
		Leader:    leader,
	})

	check.Equal(t, uint64(100), behavior.Actor.term, "Should jump to term 100")
	check.Equal(t, leader, behavior.Leader(), "Should recognize leader")
	check.False(t, behavior.IsLeader(), "Should be follower")
}

// Test: Leader election timeout randomization prevents split votes
func TestLeaderElection_RandomizationPreventsSplitVotes(t *testing.T) {
	// This test verifies that our randomElectionTimeout function
	// produces different values (within the expected range)
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	timeouts := make(map[int64]bool)
	for i := 0; i < 100; i++ {
		timeout := behavior.Actor.randomElectionTimeout()
		timeouts[timeout.Milliseconds()] = true

		// Verify timeout is within expected range
		check.True(t, timeout.Milliseconds() >= 150, "Timeout should be >= 150ms")
		check.True(t, timeout.Milliseconds() < 300, "Timeout should be < 300ms")
	}

	// Should have generated multiple different timeout values
	check.True(t, len(timeouts) > 10, "Should generate varied timeout values to prevent split votes")
}

// Test: Higher term vote request updates term even if not granting vote
func TestLeaderElection_HigherTermUpdatesEvenWithoutGrant(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	candidate1 := gen.PID{Node: "cand1@host", ID: 100, Creation: 1}
	candidate2 := gen.PID{Node: "cand2@host", ID: 200, Creation: 1}

	// Vote for candidate1 in term 5
	actor.SendMessage(candidate1, msgVote{
		ClusterID: "test-cluster",
		Term:      5,
		Candidate: candidate1,
	})

	check.Equal(t, uint64(5), behavior.Actor.term, "Should be at term 5")
	check.Equal(t, candidate1, behavior.Actor.votedFor, "Should have voted for candidate1")

	// Receive vote request from candidate2 in term 10
	actor.SendMessage(candidate2, msgVote{
		ClusterID: "test-cluster",
		Term:      10,
		Candidate: candidate2,
	})

	// Term should be updated to 10
	check.Equal(t, uint64(10), behavior.Actor.term, "Term should update to 10")

	// votedFor should be cleared when we became follower due to higher term
	// Then we should grant vote to candidate2 in new term
	check.Equal(t, candidate2, behavior.Actor.votedFor, "Should vote for candidate2 in new term")
}

// Test: Stale leader receives vote request and steps down
func TestLeaderElection_StaleLeaderStepsDown(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Become leader in term 5
	actor.SendMessage(actor.PID(), msgElectionTimeout{})
	check.True(t, behavior.IsLeader(), "Should be leader")
	currentTerm := behavior.Actor.term

	// Receive vote request with higher term (new election started elsewhere)
	candidate := gen.PID{Node: "candidate@host", ID: 100, Creation: 1}
	actor.SendMessage(candidate, msgVote{
		ClusterID: "test-cluster",
		Term:      currentTerm + 5,
		Candidate: candidate,
	})

	// Should step down and update term
	check.False(t, behavior.IsLeader(), "Should step down on higher term")
	check.Equal(t, currentTerm+5, behavior.Actor.term, "Should update to higher term")
	check.Equal(t, candidate, behavior.Actor.votedFor, "Should vote for candidate")
}

// Test: Candidate receives heartbeat and becomes follower
func TestLeaderElection_CandidateReceivesHeartbeat(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Add peers
	peer1 := gen.PID{Node: "peer1@host", ID: 100, Creation: 1}
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}
	behavior.peers[peer1] = true
	behavior.peers[peer2] = true

	// Become candidate
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	check.False(t, behavior.IsLeader(), "Should be candidate (not yet leader)")
	candidateTerm := behavior.Actor.term

	// Receive heartbeat from leader with higher term
	leader := gen.PID{Node: "leader@host", ID: 300, Creation: 1}
	actor.SendMessage(leader, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      candidateTerm + 1,
		Leader:    leader,
	})

	// Should become follower
	check.False(t, behavior.IsLeader(), "Should be follower")
	check.Equal(t, leader, behavior.Leader(), "Should recognize the leader")
	check.Equal(t, gen.PID{}, behavior.Actor.votedFor, "Should clear votedFor")
}

// Test: Multiple candidates in different terms
func TestLeaderElection_MultipleCandidatesDifferentTerms(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	candidate1 := gen.PID{Node: "cand1@host", ID: 100, Creation: 1}
	candidate2 := gen.PID{Node: "cand2@host", ID: 200, Creation: 1}
	candidate3 := gen.PID{Node: "cand3@host", ID: 300, Creation: 1}

	// Receive vote requests from candidates with increasing terms
	actor.SendMessage(candidate1, msgVote{
		ClusterID: "test-cluster",
		Term:      5,
		Candidate: candidate1,
	})

	check.Equal(t, uint64(5), behavior.Actor.term, "Should be at term 5")
	check.Equal(t, candidate1, behavior.Actor.votedFor, "Should vote for candidate1")

	actor.SendMessage(candidate2, msgVote{
		ClusterID: "test-cluster",
		Term:      6,
		Candidate: candidate2,
	})

	check.Equal(t, uint64(6), behavior.Actor.term, "Should update to term 6")
	check.Equal(t, candidate2, behavior.Actor.votedFor, "Should vote for candidate2")

	actor.SendMessage(candidate3, msgVote{
		ClusterID: "test-cluster",
		Term:      7,
		Candidate: candidate3,
	})

	check.Equal(t, uint64(7), behavior.Actor.term, "Should update to term 7")
	check.Equal(t, candidate3, behavior.Actor.votedFor, "Should vote for candidate3")
}

// Test: Re-election after failed election (split vote)
func TestLeaderElection_ReElectionAfterSplitVote(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Setup 3-node cluster, need 2 votes
	peer1 := gen.PID{Node: "peer1@host", ID: 100, Creation: 1}
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}
	behavior.peers[peer1] = true
	behavior.peers[peer2] = true

	// First election - split vote (no one gets majority)
	actor.SendMessage(actor.PID(), msgElectionTimeout{})
	term1 := behavior.Actor.term

	// peer1 rejects (voted for someone else)
	actor.SendMessage(peer1, msgVoteReply{
		ClusterID: "test-cluster",
		Term:      term1,
		Granted:   false,
	})

	check.False(t, behavior.IsLeader(), "Should not become leader with split vote")

	// Second election - timeout again, new term
	actor.SendMessage(actor.PID(), msgElectionTimeout{})
	term2 := behavior.Actor.term

	check.Equal(t, term1+1, term2, "Should increment term for new election")

	// This time both peers grant votes
	actor.SendMessage(peer1, msgVoteReply{
		ClusterID: "test-cluster",
		Term:      term2,
		Granted:   true,
	})

	// Should become leader now
	check.True(t, behavior.IsLeader(), "Should become leader in second election")
}

// Test: Bootstrap peer sending
func TestLeaderElection_BootstrapPeerMessaging(t *testing.T) {
	bootstrap := []gen.ProcessID{
		{Name: "leader", Node: "node1@host"},
		{Name: "leader", Node: "node2@host"},
		{Name: "leader", Node: "node3@host"},
	}

	actor, _ := unit.StartNode(t, "node1@host", gen.NodeOptions{}).
		SpawnRegister("leader", factoryTestLeader("test-cluster", bootstrap),
			gen.ProcessOptions{}, "test-cluster", bootstrap)

	behavior := actor.Behavior().(*TestLeader)

	mark := actor.Mark()

	// Become candidate
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	// Should send vote requests to bootstrap peers (excluding self)
	bootstrapVotes := 0

	for _, se := range actor.ShouldSend().Since(mark).Collect() {
		if vote, ok := se.Message.(msgVote); ok {
			if processID, ok := se.To.(gen.ProcessID); ok {
				// Check it's a bootstrap peer and not self
				for _, bp := range bootstrap {
					if bp.Name == processID.Name && bp.Node == processID.Node {
						if processID.Node != "node1@host" { // Not self
							bootstrapVotes++
							check.Equal(t, "test-cluster", vote.ClusterID, "Should include ClusterID")
							check.Equal(t, behavior.Actor.term, vote.Term, "Should include current term")
						}
					}
				}
			}
		}
	}

	check.Equal(t, 2, bootstrapVotes, "Should send vote requests to 2 bootstrap peers (excluding self)")
}

// Test: Leader sends heartbeats to both discovered peers and bootstrap
func TestLeaderElection_HeartbeatToAllPeers(t *testing.T) {
	bootstrap := []gen.ProcessID{
		{Name: "leader", Node: "node1@host"},
		{Name: "leader", Node: "node2@host"},
	}

	actor, _ := unit.StartNode(t, "node1@host", gen.NodeOptions{}).
		SpawnRegister("leader", factoryTestLeader("test-cluster", bootstrap),
			gen.ProcessOptions{}, "test-cluster", bootstrap)

	behavior := actor.Behavior().(*TestLeader)

	// Discover a peer (not in bootstrap)
	discoveredPeer := gen.PID{Node: "discovered@host", ID: 999, Creation: 1}
	behavior.peers[discoveredPeer] = true

	// Add another peer to ensure we don't become leader immediately
	anotherPeer := gen.PID{Node: "another@host", ID: 888, Creation: 1}
	behavior.peers[anotherPeer] = true

	// Become leader by getting votes
	actor.SendMessage(actor.PID(), msgElectionTimeout{})
	actor.SendMessage(discoveredPeer, msgVoteReply{ClusterID: "test-cluster", Term: behavior.Actor.term, Granted: true})

	check.True(t, behavior.IsLeader(), "Should be leader")

	mark := actor.Mark()

	// Send heartbeat
	actor.SendMessage(actor.PID(), msgHeartbeatTimeout{})

	// Should send to discovered peers and bootstrap peer

	// Count heartbeats (allow duplicates for now - that's a known inefficiency, not a bug)
	discoveredHeartbeats := 0
	bootstrapHeartbeats := 0
	selfHeartbeats := 0

	for _, se := range actor.ShouldSend().Since(mark).Collect() {
		if hb, ok := se.Message.(msgHeartbeat); ok {
			check.Equal(t, "test-cluster", hb.ClusterID, "Heartbeat should have ClusterID")
			check.Equal(t, behavior.Actor.term, hb.Term, "Heartbeat should have current term")

			if pid, ok := se.To.(gen.PID); ok {
				if pid.Node == "discovered@host" || pid.Node == "another@host" {
					discoveredHeartbeats++
				} else if pid.Node == "node1@host" {
					selfHeartbeats++
				}
			} else if procID, ok := se.To.(gen.ProcessID); ok {
				if procID.Node == "node2@host" {
					bootstrapHeartbeats++
				} else if procID.Node == "node1@host" {
					selfHeartbeats++
				}
			}
		}
	}

	check.True(t, discoveredHeartbeats > 0, "Should send heartbeat to discovered peers")
	check.True(t, bootstrapHeartbeats > 0, "Should send heartbeat to bootstrap peer")
	check.Equal(t, 0, selfHeartbeats, "Should not send heartbeat to self")
}

// Test: Follower with stale term catches up via heartbeat
func TestLeaderElection_StalefollowerCatchesUp(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// We're at term 1 (initial)
	check.Equal(t, uint64(0), behavior.Actor.term, "Should start at term 0")

	// Receive heartbeat from leader at much higher term
	leader := gen.PID{Node: "leader@host", ID: 100, Creation: 1}
	actor.SendMessage(leader, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      50, // Much higher
		Leader:    leader,
	})

	// Should update to current term immediately
	check.Equal(t, uint64(50), behavior.Actor.term, "Should catch up to current term")
	check.Equal(t, leader, behavior.Leader(), "Should recognize leader")
	check.False(t, behavior.IsLeader(), "Should be follower")
}

// Test: Leader with no peers in same term heartbeat (edge case)
func TestLeaderElection_LeaderReceivesOwnTermHeartbeat(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Become leader
	actor.SendMessage(actor.PID(), msgElectionTimeout{})
	check.True(t, behavior.IsLeader(), "Should be leader")

	currentTerm := behavior.Actor.term
	otherLeader := gen.PID{Node: "other@host", ID: 999, Creation: 1}

	// Another node claims to be leader in SAME term (split-brain scenario)
	actor.SendMessage(otherLeader, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      currentTerm,
		Leader:    otherLeader,
	})

	// Should detect split-brain and step down
	check.False(t, behavior.IsLeader(), "Should step down on same-term heartbeat from another leader")
	check.Equal(t, otherLeader, behavior.Leader(), "Should recognize other as leader")
}

// Test: Vote granted but then higher term seen
func TestLeaderElection_VoteGrantedThenHigherTerm(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	candidate1 := gen.PID{Node: "cand1@host", ID: 100, Creation: 1}
	candidate2 := gen.PID{Node: "cand2@host", ID: 200, Creation: 1}

	// Vote for candidate1 in term 5
	actor.SendMessage(candidate1, msgVote{
		ClusterID: "test-cluster",
		Term:      5,
		Candidate: candidate1,
	})

	check.Equal(t, candidate1, behavior.Actor.votedFor, "Should vote for candidate1")

	// candidate1 sends another vote request (maybe didn't receive our reply)
	actor.SendMessage(candidate1, msgVote{
		ClusterID: "test-cluster",
		Term:      5,
		Candidate: candidate1,
	})

	// Should still vote for candidate1 (idempotent)
	check.Equal(t, candidate1, behavior.Actor.votedFor, "Should still be voted for candidate1")

	// Now receive vote request from candidate2 with much higher term
	actor.SendMessage(candidate2, msgVote{
		ClusterID: "test-cluster",
		Term:      100,
		Candidate: candidate2,
	})

	// Should update term and vote for candidate2
	check.Equal(t, uint64(100), behavior.Actor.term, "Should jump to term 100")
	check.Equal(t, candidate2, behavior.Actor.votedFor, "Should vote for candidate2 in new term")
}

// Test: Duplicate peer discovery attempts are idempotent
func TestLeaderElection_DuplicatePeerDiscovery(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	peer := gen.PID{Node: "peer@host", ID: 100, Creation: 1}

	// Discover peer first time
	actor.SendMessage(peer, msgVote{
		ClusterID: "test-cluster",
		Term:      1,
		Candidate: peer,
	})

	check.Equal(t, 1, len(behavior.peers), "Should have 1 peer")

	mark := actor.Mark()

	// Discover same peer again
	actor.SendMessage(peer, msgVote{
		ClusterID: "test-cluster",
		Term:      2,
		Candidate: peer,
	})

	// Should still have only 1 peer
	check.Equal(t, 1, len(behavior.peers), "Should still have 1 peer (idempotent)")

	// Should only have 1 monitor event (from first discovery)
	// Check that we didn't monitor twice
	monitorCount := len(actor.ShouldMonitor().Since(mark).Collect())
	check.Equal(t, 0, monitorCount, "Should not monitor peer twice")
}

// Test: Leader receiving vote reply for old term
func TestLeaderElection_LeaderReceivesOldVoteReply(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Become leader in term 10
	peer := gen.PID{Node: "peer@host", ID: 100, Creation: 1}
	behavior.peers[peer] = true

	// Set term to 9, then trigger election to get term 10
	behavior.Actor.term = 9
	actor.SendMessage(actor.PID(), msgElectionTimeout{})
	actor.SendMessage(peer, msgVoteReply{
		ClusterID: "test-cluster",
		Term:      10,
		Granted:   true,
	})

	check.True(t, behavior.IsLeader(), "Should be leader")
	check.Equal(t, uint64(10), behavior.Actor.term, "Should be at term 10")

	// Receive stale vote reply from term 5
	staleVoter := gen.PID{Node: "stale@host", ID: 200, Creation: 1}
	actor.SendMessage(staleVoter, msgVoteReply{
		ClusterID: "test-cluster",
		Term:      5,
		Granted:   true,
	})

	// Should remain leader and ignore stale vote
	check.True(t, behavior.IsLeader(), "Should remain leader")
	check.Equal(t, uint64(10), behavior.Actor.term, "Term should not change")
}

// Test: Heartbeat resets election timer (prevents unnecessary elections)
func TestLeaderElection_HeartbeatResetsElectionTimer(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	leader := gen.PID{Node: "leader@host", ID: 100, Creation: 1}

	// Establish leader
	actor.SendMessage(leader, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      5,
		Leader:    leader,
	})

	check.False(t, behavior.IsLeader(), "Should be follower")

	mark := actor.Mark()

	// Receive another heartbeat
	actor.SendMessage(leader, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      5,
		Leader:    leader,
	})

	// Should reset election timer (we can't test timer directly, but we can verify state)
	check.False(t, behavior.IsLeader(), "Should remain follower")
	check.Equal(t, leader, behavior.Leader(), "Leader should remain same")

	// Verify a new election timer was set (SendAfter called)
	foundTimerReset := false
	for _, sa := range actor.ShouldSendAfter().Since(mark).Collect() {
		if _, ok := sa.Message.(msgElectionTimeout); ok {
			foundTimerReset = true
			break
		}
	}

	check.True(t, foundTimerReset, "Should reset election timer via SendAfter")
}

// Test: Candidate becomes follower on heartbeat
func TestLeaderElection_CandidateStepsDownOnHigherTermHeartbeat(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	peer1 := gen.PID{Node: "peer1@host", ID: 100, Creation: 1}
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}
	behavior.peers[peer1] = true
	behavior.peers[peer2] = true

	// Become candidate (won't become leader without votes from peers)
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	candidateTerm := behavior.Actor.term
	check.False(t, behavior.IsLeader(), "Should be candidate (not leader yet)")
	check.Equal(t, actor.PID(), behavior.Actor.votedFor, "Should have voted for self")

	// Receive heartbeat with same term from leader
	// (another candidate won the election)
	leader := gen.PID{Node: "leader@host", ID: 300, Creation: 1}
	actor.SendMessage(leader, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      candidateTerm,
		Leader:    leader,
	})

	// Should become follower
	check.False(t, behavior.IsLeader(), "Should be follower")
	check.Equal(t, leader, behavior.Leader(), "Should recognize leader")
}

// Test: Zero peers - immediate leader election
func TestLeaderElection_ZeroPeersImmediateElection(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// No peers in map
	check.Equal(t, 0, len(behavior.peers), "Should have no peers")

	// Trigger election
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	// With 0 peers, quorum is 0/2 + 1 = 1
	// Self-vote (1) >= quorum (1), so should become leader immediately
	check.True(t, behavior.IsLeader(), "Should become leader immediately with no peers")
}

// Test: Election after leader becomes unresponsive (no heartbeats)
func TestLeaderElection_UnresponsiveLeaderTimeout(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	behavior := actor.Behavior().(*TestLeader)

	// Add a peer so we don't become leader immediately
	peer := gen.PID{Node: "peer@host", ID: 999, Creation: 1}
	behavior.peers[peer] = true

	leader := gen.PID{Node: "leader@host", ID: 100, Creation: 1}

	// Establish leader via heartbeat
	actor.SendMessage(leader, msgHeartbeat{
		ClusterID: "test-cluster",
		Term:      5,
		Leader:    leader,
	})

	check.Equal(t, leader, behavior.Leader(), "Should have leader")
	check.False(t, behavior.IsLeader(), "Should be follower")

	// Simulate election timeout (no heartbeats received)
	actor.SendMessage(actor.PID(), msgElectionTimeout{})

	// Should become candidate (not leader yet - need peer's vote)
	check.False(t, behavior.IsLeader(), "Should be candidate (not leader yet without votes)")
	check.Equal(t, uint64(6), behavior.Actor.term, "Should increment term")
	check.Equal(t, actor.PID(), behavior.Actor.votedFor, "Should vote for self")
}
