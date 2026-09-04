package health

// The client side and the defaults. The helpers are what a consumer actually calls -
// nothing in the actor's own tests exercises them - and the defaults are what makes
// embedding health.Actor enough to satisfy ActorBehavior.

import (
	"net/http"
	"testing"
	"time"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/check"
	"ergo.services/ergo/testing/unit"
)

// bare embeds Actor and implements nothing else, so the defaults carry it.
type bare struct {
	Actor
}

// client drives the package helpers against a target, so their Call/Send shapes are
// exercised through a real process rather than constructed by hand.
type client struct {
	Actor

	target gen.Atom
	err    error
}

func (c *client) HandleMessage(from gen.PID, message any) error {
	switch message {
	case "register":
		c.err = Register(c, c.target, "db", ProbeReadiness, time.Second)
	case "register-empty":
		c.err = Register(c, c.target, "", ProbeLiveness, 0)
	case "unregister":
		c.err = Unregister(c, c.target, "db")
	case "unregister-unknown":
		c.err = Unregister(c, c.target, "nope")
	case "heartbeat":
		c.err = Heartbeat(c, c.target, "db")
	case "up":
		c.err = SignalUp(c, c.target, "db")
	case "down":
		c.err = SignalDown(c, c.target, "db")
	}
	return nil
}

// Embedding is enough: only Init has no default, and the framework message classes must
// be survivable rather than fatal.
func TestDefaults_EmbeddingIsEnough(t *testing.T) {
	sub := unit.Prepare(t, func() gen.ProcessBehavior { return &bare{} }, gen.ProcessOptions{},
		Options{Mux: http.NewServeMux()})
	check.NoError(t, sub.Node().Network().RegisterTypes(NetworkTypes()))
	check.NoError(t, sub.Run())

	sub.SendMessage(gen.PID{ID: 1}, "anything")
	result, err := sub.Call(gen.PID{ID: 1}, "anything")
	check.NoError(t, err)
	if result != nil {
		t.Fatalf("the default HandleCall answered %#v, want nil", result)
	}
	sub.ShouldTerminate().None().Assert()
}

// The default Init takes Options from args, and rejects anything else rather than
// starting with a zero configuration the caller did not ask for.
func TestDefaults_InitTakesOptionsFromArgs(t *testing.T) {
	sub := unit.Prepare(t, func() gen.ProcessBehavior { return &bare{} }, gen.ProcessOptions{},
		Options{Mux: http.NewServeMux(), Path: "/custom", CheckInterval: time.Minute})
	check.NoError(t, sub.Node().Network().RegisterTypes(NetworkTypes()))
	check.NoError(t, sub.Run())

	state, err := sub.Inspect(gen.PID{})
	check.NoError(t, err)
	check.Equal(t, "1m0s", state["check_interval"])

	wrong := unit.Prepare(t, func() gen.ProcessBehavior { return &bare{} }, gen.ProcessOptions{},
		"not options")
	check.NoError(t, wrong.Node().Network().RegisterTypes(NetworkTypes()))
	if err := wrong.Run(); err == nil {
		t.Fatal("Init must reject an argument that is not Options")
	}
}

// The inspect surface is what an operator reads in the observer: how many signals, which
// are down, and where the endpoints are.
func TestInspect_ReportsSignalsAndEndpoint(t *testing.T) {
	sub, _ := spawnProbe(t, time.Second)
	register(sub, gen.PID{ID: 1}, "db", ProbeLiveness, 0)
	register(sub, gen.PID{ID: 1}, "cache", ProbeReadiness, 0)
	sub.SendMessage(gen.PID{ID: 1}, MessageSignalDown{Signal: "cache"})

	state, err := sub.Inspect(gen.PID{})
	check.NoError(t, err)

	check.Equal(t, "2", state["signals"])
	check.Equal(t, "up", state["db"])
	check.Equal(t, "down", state["cache"])
	// An external mux means no server of our own, so the path is reported rather than a
	// host:port that nothing is listening on.
	check.Equal(t, DefaultPath+"/*", state["path"])
	if _, reported := state["endpoint"]; reported == true {
		t.Fatalf("endpoint reported for an external mux: %v", state)
	}
}

// Without an external mux the actor owns a server, and inspect reports the address an
// operator would curl. Rendered directly: standing up the actor would bind a real port,
// and the answer depends on nothing but the options.
func TestInspect_ReportsEndpointWithoutExternalMux(t *testing.T) {
	a := &Actor{}
	a.options = Options{Host: "127.0.0.1", Port: 12345, Path: DefaultPath}

	state := a.HandleInspect(gen.PID{})
	check.Equal(t, "http://127.0.0.1:12345/health/*", state["endpoint"])
	if _, reported := state["path"]; reported == true {
		t.Fatalf("path reported for an owned server: %v", state)
	}
}

// spawnClient brings up a client actor pointed at the registered health actor.
func spawnClient(t *testing.T, target gen.Atom) (*unit.Subject, *client) {
	t.Helper()

	behavior := &client{target: target}
	sub := unit.Prepare(t, func() gen.ProcessBehavior { return behavior }, gen.ProcessOptions{},
		Options{Mux: http.NewServeMux()})
	check.NoError(t, sub.Node().Network().RegisterTypes(NetworkTypes()))
	check.NoError(t, sub.Run())
	return sub, behavior
}

// Register / Unregister unwrap the response's Error field into a real error, so a caller
// that only checks err does not carry on believing its signal is registered.
func TestHelpers_RegisterUnregisterSurfaceTheResponseError(t *testing.T) {
	target := gen.Atom("health")
	sub, c := spawnClient(t, target)

	sub.OnCall(target).Respond(RegisterResponse{})
	sub.SendMessage(gen.PID{ID: 1}, "register")
	check.NoError(t, c.err)

	sub.OnCall(target).Respond(RegisterResponse{Error: "empty signal name"})
	sub.SendMessage(gen.PID{ID: 1}, "register-empty")
	if c.err == nil {
		t.Fatal("Register must surface the response error")
	}

	sub.OnCall(target).Respond(UnregisterResponse{Error: "unknown signal"})
	sub.SendMessage(gen.PID{ID: 1}, "unregister-unknown")
	if c.err == nil {
		t.Fatal("Unregister must surface the response error")
	}
}

// A response of the wrong type is a protocol mismatch, not a success.
func TestHelpers_RegisterRejectsAnUnexpectedResponse(t *testing.T) {
	target := gen.Atom("health")
	sub, c := spawnClient(t, target)

	sub.OnCall(target).Respond("surprise")
	sub.SendMessage(gen.PID{ID: 1}, "register")
	if c.err == nil {
		t.Fatal("Register must reject a response it cannot interpret")
	}
}

// The fire-and-forget helpers must send the message the actor handles - a mismatch here
// is a heartbeat that never lands and a probe that fails on a healthy process.
func TestHelpers_SendTheMessagesTheActorHandles(t *testing.T) {
	target := gen.Atom("health")
	sub, c := spawnClient(t, target)

	for _, tc := range []struct {
		trigger string
		want    any
	}{
		{"heartbeat", MessageHeartbeat{Signal: "db"}},
		{"up", MessageSignalUp{Signal: "db"}},
		{"down", MessageSignalDown{Signal: "db"}},
	} {
		sub.SendMessage(gen.PID{ID: 1}, tc.trigger)
		check.NoError(t, c.err)
		sub.ShouldSend().To(target).Message(tc.want).Once().Assert()
	}
}

// The default signal callbacks are no-ops, but they are on the path of every consumer that
// embeds Actor without overriding them: a signal flip must not take the actor down.
func TestDefaults_SignalCallbacksAreSurvivable(t *testing.T) {
	mux := http.NewServeMux()
	sub := unit.Prepare(t, func() gen.ProcessBehavior { return &bare{} }, gen.ProcessOptions{},
		Options{Mux: mux})
	check.NoError(t, sub.Node().Network().RegisterTypes(NetworkTypes()))
	check.NoError(t, sub.Run())

	sub.Call(gen.PID{ID: 1}, RegisterRequest{Signal: "db", Probe: ProbeLiveness})
	sub.SendMessage(gen.PID{ID: 1}, MessageSignalDown{Signal: "db"})
	sub.SendMessage(gen.PID{ID: 1}, MessageSignalUp{Signal: "db"})

	state, err := sub.Inspect(gen.PID{})
	check.NoError(t, err)
	check.Equal(t, "up", state["db"])
	sub.ShouldTerminate().None().Assert()
}
