package leader

// ProcessRun completeness and the behavior surface: the classes of message the loop
// used to drop, the trap it did not have, and the defaults that made embedding
// leader.Actor insufficient. Every path here was at zero coverage.

import (
	"errors"
	"strings"
	"testing"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/check"
	"ergo.services/ergo/testing/unit"
)

// bareLeader embeds Actor and implements nothing else. It exists to prove that
// embedding is enough to satisfy ActorBehavior - it was not, and the resulting
// assertion failure went on to nil-dereference in ProcessTerminate.
type bareLeader struct {
	Actor
}

func (b *bareLeader) Init(args ...any) (Options, error) {
	return Options{ClusterID: args[0].(string), MinClusterSize: 1}, nil
}

func TestRuntime_EmbeddingActorIsEnough(t *testing.T) {
	actor, err := spawnLeader(t, func() gen.ProcessBehavior { return &bareLeader{} },
		gen.ProcessOptions{}, "bare-cluster")
	check.NoError(t, err, "embedding leader.Actor must satisfy ActorBehavior")

	// The defaults warn rather than stay silent: winning leadership and doing nothing
	// with it is a missing implementation, not an intention.
	actor.FireTimers()
	actor.ShouldLog().Level(gen.LogLevelWarning).
		Containing("does not implement it").Once().Assert()

	// The same holds for the framework message classes: unhandled, but reported and
	// survivable rather than dropped or fatal.
	actor.DeliverEvent(gen.Event{Name: "e", Node: "peer1@host"}, "payload")
	actor.DeliverSpan(gen.TracingSpan{TraceID: [2]uint64{1, 2}})
	actor.DeliverLog(gen.MessageLog{Level: gen.LogLevelInfo, Format: "hi"})

	for _, unhandled := range []string{"unhandled event", "unhandled span", "unhandled log message"} {
		actor.ShouldLog().Level(gen.LogLevelWarning).Containing(unhandled).Once().Assert()
	}
	actor.ShouldTerminate().None().Assert()
}

// runtimeLeader records everything the loop can deliver.
type runtimeLeader struct {
	Actor

	events   []gen.MessageEvent
	spans    []gen.TracingSpan
	logs     []gen.MessageLog
	messages []any
}

func (r *runtimeLeader) Init(args ...any) (Options, error) {
	return Options{ClusterID: args[0].(string), MinClusterSize: 1}, nil
}

func (r *runtimeLeader) HandleBecomeLeader() error            { return nil }
func (r *runtimeLeader) HandleBecomeFollower(_ gen.PID) error { return nil }

func (r *runtimeLeader) HandleEvent(event gen.MessageEvent) error {
	r.events = append(r.events, event)
	return nil
}

func (r *runtimeLeader) HandleSpan(span gen.TracingSpan) error {
	r.spans = append(r.spans, span)
	return nil
}

func (r *runtimeLeader) HandleLog(message gen.MessageLog) error {
	r.logs = append(r.logs, message)
	return nil
}

func (r *runtimeLeader) HandleMessage(from gen.PID, message any) error {
	r.messages = append(r.messages, message)
	return nil
}

func spawnRuntime(t *testing.T) (*unit.Subject, *runtimeLeader) {
	t.Helper()

	actor, err := spawnLeader(t, func() gen.ProcessBehavior { return &runtimeLeader{} },
		gen.ProcessOptions{}, "runtime-cluster")
	check.NoError(t, err)
	return actor, actor.Behavior().(*runtimeLeader)
}

// N11: the Event arm did not exist, so events were dropped on the floor. A consumer
// that needed them had to host a separate actor to receive them.
func TestRuntime_EventsReachTheBehavior(t *testing.T) {
	actor, behavior := spawnRuntime(t)

	actor.DeliverEvent(gen.Event{Name: "resources", Node: "peer1@host"}, "payload")

	check.Equal(t, 1, len(behavior.events), "the event must reach HandleEvent")
	check.Equal(t, gen.Atom("resources"), behavior.events[0].Event.Name)
	actor.ShouldTerminate().None().Assert()
}

// N11, the other half: spans were dropped the same way.
func TestRuntime_SpansReachTheBehavior(t *testing.T) {
	actor, behavior := spawnRuntime(t)

	actor.DeliverSpan(gen.TracingSpan{TraceID: [2]uint64{1, 2}, Point: gen.TracingPointProcessed})

	check.Equal(t, 1, len(behavior.spans), "the span must reach HandleSpan")
	actor.ShouldTerminate().None().Assert()
}

// N22: the log queue was never popped, so a leader.Actor registered as a node logger
// accumulated messages nothing would ever consume.
func TestRuntime_LogQueueIsDrained(t *testing.T) {
	actor, behavior := spawnRuntime(t)

	actor.DeliverLog(gen.MessageLog{Level: gen.LogLevelInfo, Format: "hello"})

	check.Equal(t, 1, len(behavior.logs), "the log message must reach HandleLog")
	actor.ShouldTerminate().None().Assert()
}

// N9: every exit signal was fatal and there was no trap. An application group member
// is not restarted, so one linked child taking this process down removed the node from
// the cluster for good.
func TestRuntime_TrapExitTurnsAnExitIntoAMessage(t *testing.T) {
	actor, behavior := spawnRuntime(t)
	behavior.SetTrapExit(true)
	check.True(t, behavior.TrapExit())

	child := gen.PID{Node: "runtime@host", ID: 42, Creation: 1}
	actor.DeliverExit(child, errors.New("child died"))

	check.Equal(t, 1, len(behavior.messages), "with the trap set the exit arrives as a message")
	exit, ok := behavior.messages[0].(gen.MessageExitPID)
	check.True(t, ok, "and it keeps its type")
	check.Equal(t, child, exit.PID)
	actor.ShouldTerminate().None().Assert()
}

// The default is unchanged: without the trap, an exit still terminates.
func TestRuntime_WithoutTrapAnExitStillTerminates(t *testing.T) {
	actor, _ := spawnRuntime(t)

	actor.DeliverExit(gen.PID{Node: "runtime@host", ID: 42, Creation: 1}, errors.New("boom"))

	actor.ShouldTerminate().Once().Assert()
}

// N26 - tracing propagation - has no unit-layer test. The fix carries the incoming
// message.Tracing into the callback and emits a Processed span, but the harness has no
// way to deliver a message with Tracing set: there is no SendMessageWithTracing on
// unit.Subject, and DeliverSpan exercises the Span arm rather than a traced regular
// message. Covering it needs either a harness addition or a stage test with a real
// tracing exporter.

// N23: the two election timeouts were defaulted independently, so a partial
// configuration was rejected at startup with an error about a value the caller never
// set.
func TestRuntime_PartialTimeoutConfigurationResolves(t *testing.T) {
	for _, tc := range []struct {
		name     string
		min, max int
		wantMin  int
		wantMax  int
	}{
		{"only min", 1000, 0, 1000, 2000},
		{"only max", 0, 1000, 500, 1000},
		{"both", 400, 900, 400, 900},
		{"neither", 0, 0, 150, 300},
	} {
		factory := func() gen.ProcessBehavior {
			return &TestLeader{
				clusterID:          "test-cluster",
				electionTimeoutMin: tc.min,
				electionTimeoutMax: tc.max,
			}
		}
		actor, err := spawnLeader(t, factory, gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})
		check.NoError(t, err, tc.name+": a partial configuration must resolve, not fail")

		behavior := actor.Behavior().(*TestLeader)
		check.Equal(t, tc.wantMin, behavior.Actor.electionTimeoutMin, tc.name+": min")
		check.Equal(t, tc.wantMax, behavior.Actor.electionTimeoutMax, tc.name+": max")
	}
}

// A withdrawn peer must not come back through a message that was already in flight,
// or the consumer's decision is undone by the protocol.
func TestRuntime_LeaveIsNotUndoneByInFlightTraffic(t *testing.T) {
	actor, behavior := spawnObs(t, 0)

	peer := testPeerPID(1)
	behavior.Join(gen.ProcessID{Name: "leader", Node: peer.Node})
	check.Equal(t, 2, behavior.viewSize())

	behavior.Leave(peer.Node)
	check.Equal(t, 1, behavior.viewSize(), "withdrawn immediately")

	// A vote request that was already queued when Leave ran.
	actor.SendMessage(peer, msgVote{ClusterID: "obs-cluster", Term: 1, Candidate: peer})

	check.Equal(t, 1, behavior.viewSize(),
		"a peer the consumer withdrew must not be re-admitted by its own traffic")
	check.Contains(t, inspectKey(t, actor, "ergo:dropped_by_reason"), "withdrawn_peer")
}

// Without the protocol types on the node every vote fails to encode and the cluster never
// converges, with nothing at startup to say why. The actor refuses to start instead.
func TestRuntime_RefusesToStartWithoutRegisteredTypes(t *testing.T) {
	sub := unit.Prepare(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})

	err := sub.Run()
	if err == nil {
		t.Fatal("expected Init to refuse an unregistered protocol")
	}
	for _, want := range []string{"msgVote", "NetworkTypes()", docsURL} {
		if strings.Contains(err.Error(), want) == false {
			t.Fatalf("error %q does not carry %q", err, want)
		}
	}

	// The same actor starts once the node knows its protocol.
	ok := unit.Prepare(t, factoryTestLeader("test-cluster", []gen.ProcessID{}),
		gen.ProcessOptions{}, "test-cluster", []gen.ProcessID{})
	check.NoError(t, ok.Node().Network().RegisterTypes(NetworkTypes()))
	check.NoError(t, ok.Run())
}
