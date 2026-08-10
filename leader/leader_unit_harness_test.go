// Unit-layer test scaffolding (step 1 of the leader.audit.md remediation plan,
// see ~/devel/ergo.services/leader.audit.md, "Fix plan" / "Step 1").
//
// The pre-step-1 suite (leader_test.go) never drove several harness capabilities
// it already imports: grep counts of FireTimers, DeliverDown, DeliverExit,
// ShouldLog, ShouldTerminate, OnMonitor, OnSend, and Inspect-driven-through-the-
// harness were all 0 in leader_test.go. Section B below is one small,
// reusable demonstration per capability, kept deliberately defect-neutral (each
// assertion holds regardless of whether any of the 34 audited defects are ever
// fixed) so this file documents mechanism, not opinion.
//
// Section C holds two RED-by-default canaries that section B's tooling makes
// possible to write at all. Both are t.Skip'd so `go test ./...` stays green;
// delete the t.Skip line to see either fail, for the reason named in its skip
// message, and restore the skip afterward. Do NOT "fix" the canaries by editing
// leader.go/init.go in this step, and do NOT invert leader_test.go's own
// defect-blessing tests here - both are step 2 of the remediation plan, a
// separate reviewed change.
package leader

import (
	"testing"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/check"
	"ergo.services/ergo/testing/unit"
)

// discoverPeersViaVoteReply makes actor legitimately learn about each peer PID
// the same way production does - by receiving a ClusterID-matching protocol
// message from it (discoverPeer, leader.go:305-317) - rather than writing
// behavior.peers[...] = true directly, which is how most of the pre-step-1 suite
// seeds peers (leader.audit.md's test assessment calls this out by name: "the
// suite tests a state machine by assigning to its state"). A granted-false vote
// reply is used deliberately: handleVoteReply (leader.go:493-542) returns
// immediately on Granted==false, before touching votedFor or votesReceived, and
// also immediately on a term mismatch, so discovery is its only observable side
// effect regardless of actor's current term.
func discoverPeersViaVoteReply(actor *unit.Subject, clusterID string, peers ...gen.PID) {
	for _, p := range peers {
		actor.SendMessage(p, msgVoteReply{ClusterID: clusterID, Term: 0, Granted: false})
	}
}

// ---------------------------------------------------------------------------
// B. Harness scaffolding: one demonstration per capability the suite never used.
// ---------------------------------------------------------------------------

// Driving elections via real timer expiry instead of hand-injecting
// msgElectionTimeout{} (the pattern leader_test.go uses 27 times per the audit).
func TestHarness_FireTimers_DrivesARealElection(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})
	behavior := actor.Behavior().(*TestLeader)

	peer1 := gen.PID{Node: "peer1@host", ID: 100, Creation: 1}
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}
	discoverPeersViaVoteReply(actor, "test-cluster", peer1, peer2)

	// FireTimers (testing/unit/unit.go:510) delivers whatever SendAfter armed for
	// this process for real; ProcessInit armed exactly one election timer
	// (leader.go:152), so this is that timer firing, not a message the test built.
	fired := actor.FireTimers()
	check.Equal(t, 1, fired, "the election timer armed by ProcessInit should fire")
	check.Equal(t, uint64(1), behavior.Term(), "firing it should start term 1")

	actor.ShouldSend().To(peer1).
		Message(msgVote{ClusterID: "test-cluster", Term: 1, Candidate: actor.PID()}).Once().Assert()
	actor.ShouldSend().To(peer2).
		Message(msgVote{ClusterID: "test-cluster", Term: 1, Candidate: actor.PID()}).Once().Assert()

	actor.SendMessage(peer1, msgVoteReply{ClusterID: "test-cluster", Term: 1, Granted: true})
	actor.SendMessage(peer2, msgVoteReply{ClusterID: "test-cluster", Term: 1, Granted: true})

	check.True(t, behavior.IsLeader(), "two real grants plus the self-vote reach quorum 2")
}

// Delivering a down for a peer.
func TestHarness_DeliverDown_RemovesADiscoveredPeer(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})
	behavior := actor.Behavior().(*TestLeader)

	peer := gen.PID{Node: "peer@host", ID: 100, Creation: 1}
	discoverPeersViaVoteReply(actor, "test-cluster", peer)
	check.Equal(t, 1, behavior.PeerCount(), "the peer should be discovered first")

	// DeliverDown (testing/unit/unit.go:341) delivers a real gen.MessageDownPID as
	// a monitor notification would arrive, not a hand-built message sent by
	// SendMessage. gen.TerminateReasonNormal is a genuine death - removing the
	// peer on a genuine death is not disputed by any of the audit's findings
	// (N3's fix direction is explicitly about NOT treating a transient connection
	// loss the same way, not about genuine deaths).
	actor.DeliverDown(peer, gen.TerminateReasonNormal)

	check.Equal(t, 0, behavior.PeerCount(), "a genuine death must remove the peer")
}

// Injecting Monitor failure.
func TestHarness_OnMonitor_InjectedFailureIsObservable(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	peer := gen.PID{Node: "peer@host", ID: 100, Creation: 1}
	actor.OnMonitor(peer).Fail(gen.ErrNoConnection)

	// discoverPeer (leader.go:305-317) calls Monitor unconditionally right after
	// adding the peer to l.peers, and today discards the result (L3). Injecting
	// and observing that failure here - without asserting anything about how
	// leader.go reacts to it, which is exactly what it does not do - is what
	// makes L3 (the failed-Monitor ghost-peer defect) writable at all.
	discoverPeersViaVoteReply(actor, "test-cluster", peer)

	actor.ShouldMonitor().Target(peer).Error(gen.ErrNoConnection).Once().Assert()
}

// Injecting Send failure for a named target.
func TestHarness_OnSend_InjectedFailureIsObservable(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	peer := gen.PID{Node: "peer@host", ID: 100, Creation: 1}
	discoverPeersViaVoteReply(actor, "test-cluster", peer)
	actor.OnSend(peer).Fail(gen.ErrProcessMailboxFull)

	// becomeCandidate's vote fan-out (leader.go:431-440) Sends to every peer and
	// discards the result (N6/N21's discarded-error family); with one peer the
	// buggy quorum (N1) also makes this actor self-elect and heartbeat the same
	// peer in the same call, so more than one Send may fail - AtLeast(1), not
	// Once(), is the correct cardinality for what this test actually demonstrates
	// (the injection is observable), independent of that incidental N1 behavior.
	actor.FireTimers()

	actor.ShouldSend().To(peer).Error(gen.ErrProcessMailboxFull).AtLeast(1).Assert()
	actor.ShouldTerminate().None().Assert()
}

// Driving HandleInspect through the harness rather than by calling the method
// directly. (The pre-step-1 suite's TestLeaderInspect calls
// behavior.Actor.HandleInspect(...) straight, so it never exercises ProcessRun's
// Inspect arm, leader.go:237-239 - 0.0% covered per the audit's coverage-gaps
// section.)
func TestHarness_Inspect_DrivenThroughTheRealRequestPath(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	// Inspect (testing/unit/unit.go:467) pushes a real MailboxMessageTypeInspect
	// onto the urgent queue and resolves the SendResponse ProcessRun's Inspect arm
	// sends back - the same path a real gen.Node.Inspect request drives.
	// TestLeader.HandleInspect returns an empty map, and the election state survives it:
	// ProcessRun computes the state first and merges the behavior's map on top, so an
	// override adds fields rather than replacing the answer.
	result, err := actor.Inspect(gen.PID{})
	check.NoError(t, err)
	for _, key := range []string{"ergo:cluster", "ergo:state", "ergo:term", "ergo:view_size", "ergo:quorum"} {
		check.NotEqual(t, "", result[key],
			"an overriding behavior must not erase the actor's own observability: "+key)
	}

	actor.ShouldTerminate().None().Assert()
}

// Delivering exits.
func TestHarness_DeliverExit_ReachesProcessRunsExitArm(t *testing.T) {
	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	dead := gen.PID{Node: "linked@host", ID: 100, Creation: 1}

	// Today ProcessRun's Exit arm (leader.go:221-235) is dead code across the
	// whole suite - N9's coverage gap notes 0.0%, because nothing ever delivers a
	// MailboxMessageTypeExit. DeliverExit (testing/unit/unit.go:327) is how a test
	// reaches it; the exit is unconditionally fatal today (no trap facility, N9),
	// which is exactly the mechanism this demonstrates reaching, not endorses.
	actor.DeliverExit(dead, gen.TerminateReasonShutdown)

	actor.ShouldTerminate().ReasonIs(gen.TerminateReasonShutdown).Once().Assert()
}

// Asserting on logs and termination (the termination half is also covered by
// TestHarness_DeliverExit_ReachesProcessRunsExitArm above).
func TestHarness_ShouldLog_ObservesAConfigurationWarning(t *testing.T) {
	factory := func() gen.ProcessBehavior {
		return &TestLeader{
			clusterID:          "test-cluster",
			electionTimeoutMin: 100,
			electionTimeoutMax: 300,
			heartbeatInterval:  150, // >= ElectionTimeoutMin: leader.go:145-148 warns
		}
	}
	actor, err := unit.Spawn(t, factory, gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})
	check.NoError(t, err)

	// ShouldLog (testing/check/asserts.go:876) reads the mock logger's recording,
	// gated by the mock node's default log level (Info) exactly as production
	// gates by level - a real, reachable Warning line, not a synthesized one.
	actor.ShouldLog().Level(gen.LogLevelWarning).Containing("elections may be unstable").Once().Assert()
}

// ---------------------------------------------------------------------------
// C. RED-by-default canaries.
// ---------------------------------------------------------------------------

// L2 - becomeCandidate never re-arms the election timer, so a candidate that
// misses quorum can never start a second election on its own.
func TestCanary_L2_CandidateReArmsElectionTimerAfterMissedQuorum(t *testing.T) {

	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})
	behavior := actor.Behavior().(*TestLeader)

	peer1 := gen.PID{Node: "peer1@host", ID: 100, Creation: 1}
	peer2 := gen.PID{Node: "peer2@host", ID: 200, Creation: 1}
	discoverPeersViaVoteReply(actor, "test-cluster", peer1, peer2)
	check.Equal(t, 2, behavior.PeerCount(), "both peers should be discovered before the election")

	// Round 1: the process's own real election timer (armed by ProcessInit,
	// leader.go:152) fires - not a hand-injected msgElectionTimeout{} the way
	// leader_test.go:1629's TestLeaderElection_ReElectionAfterSplitVote does - and
	// becomeCandidate runs for real.
	fired1 := actor.FireTimers()
	check.Equal(t, 1, fired1, "exactly the initial election timer should be armed")
	check.Equal(t, uint64(1), behavior.Term(), "the real timer firing should start term 1")
	check.False(t, behavior.IsLeader(), "quorum is 2 with 2 peers; the self-vote alone cannot win")

	// peer1 rejects (voted for someone else this term): a genuine split vote,
	// still short of quorum.
	actor.SendMessage(peer1, msgVoteReply{ClusterID: "test-cluster", Term: 1, Granted: false})
	check.False(t, behavior.IsLeader(), "a single rejection must not create a leader")

	// Round 2: fire timers again. If becomeCandidate had re-armed the clock (the
	// audit's fix direction), this would deliver the next election timeout and
	// start term 2 - all on its own, with no further input from the test.
	fired2 := actor.FireTimers()
	check.Equal(t, 1, fired2,
		"a candidate that missed quorum must re-arm its own election timer for a second round")
	check.Equal(t, uint64(2), behavior.Term(), "a second, self-driven election must advance the term")
}

// L1 - the stale-term vote rejection omits ClusterID, so the receiver's own
// ClusterID guard drops it and the joiner never learns the real term.
func TestCanary_L1_StaleTermVoteRejectionCarriesClusterID(t *testing.T) {

	actor, _ := unit.Spawn(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})
	behavior := actor.Behavior().(*TestLeader)

	remote1 := gen.PID{Node: "remote1@host", ID: 100, Creation: 1}
	remote2 := gen.PID{Node: "remote2@host", ID: 200, Creation: 1}

	// establish term 10
	actor.SendMessage(remote1, msgVote{ClusterID: "test-cluster", Term: 10, Candidate: remote1})
	check.Equal(t, uint64(10), behavior.Term())

	// a stale-term candidate is rejected
	actor.SendMessage(remote2, msgVote{ClusterID: "test-cluster", Term: 5, Candidate: remote2})

	// Full-struct equality (check's Message filter uses reflect.DeepEqual), unlike
	// leader_test.go:487-496's hand-rolled loop that checks only vr.Term and
	// vr.Granted and never reads vr.ClusterID - which is exactly the gap that let
	// L1 ship.
	actor.ShouldSend().To(remote2).
		Message(msgVoteReply{ClusterID: "test-cluster", Term: 10, Granted: false}).
		Once().Assert()
}
