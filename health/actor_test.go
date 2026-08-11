package health

// The actor's whole purpose is the answer it gives an orchestrator on /live and /ready, so
// these tests assert that answer rather than the internal signal map: a probe that returns
// 200 while a signal is down is the failure that matters, and it is reachable from every
// path below - explicit down, missed heartbeat, and a dead registrant.
//
// Options.Mux takes an external mux, so the handlers are driven with httptest and no port
// is ever bound.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/check"
	"ergo.services/ergo/testing/unit"
)

// probe is the health actor under test: it keeps the mux the handlers land on and records
// the callbacks, which are the actor's only outward signal besides the HTTP body.
type probe struct {
	Actor

	mux      *http.ServeMux
	interval time.Duration

	up   []gen.Atom
	down []gen.Atom
}

func (p *probe) Init(args ...any) (Options, error) {
	return Options{Mux: p.mux, CheckInterval: p.interval}, nil
}

func (p *probe) HandleSignalUp(signal gen.Atom) error {
	p.up = append(p.up, signal)
	return nil
}

func (p *probe) HandleSignalDown(signal gen.Atom) error {
	p.down = append(p.down, signal)
	return nil
}

// spawnProbe brings the actor up with its wire types registered, as an application's Load
// does - Init refuses to start without them.
func spawnProbe(t *testing.T, interval time.Duration) (*unit.Subject, *probe) {
	t.Helper()

	behavior := &probe{mux: http.NewServeMux(), interval: interval}
	sub := unit.Prepare(t, func() gen.ProcessBehavior { return behavior }, gen.ProcessOptions{})
	check.NoError(t, sub.Node().Network().RegisterTypes(NetworkTypes()))
	check.NoError(t, sub.Run())
	return sub, behavior
}

// get drives one probe endpoint through the mux and returns the status and decoded body.
func get(t *testing.T, p *probe, endpoint string) (int, probeResponse) {
	t.Helper()

	rec := httptest.NewRecorder()
	p.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, DefaultPath+endpoint, nil))

	var body probeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s body %q is not a probeResponse: %s", endpoint, rec.Body.String(), err)
	}
	return rec.Code, body
}

func register(sub *unit.Subject, from gen.PID, signal gen.Atom, p Probe, timeout time.Duration) any {
	result, _ := sub.Call(from, RegisterRequest{Signal: signal, Probe: p, Timeout: timeout})
	return result
}

// A node with no signals is healthy: the orchestrator must not be told to restart a
// process just because nothing has registered yet.
func TestProbe_NoSignalsIsHealthy(t *testing.T) {
	_, p := spawnProbe(t, time.Second)

	for _, endpoint := range []string{"/live", "/ready", "/startup"} {
		code, body := get(t, p, endpoint)
		if code != http.StatusOK {
			t.Fatalf("%s = %d on a fresh actor, want 200", endpoint, code)
		}
		if len(body.Signals) != 0 {
			t.Fatalf("%s reports signals %v on a fresh actor", endpoint, body.Signals)
		}
	}
}

// Registering with no explicit probe defaults to liveness, and the signal starts up.
func TestProbe_RegisterDefaultsToLiveness(t *testing.T) {
	sub, p := spawnProbe(t, time.Second)

	resp, ok := register(sub, gen.PID{ID: 1}, "db", 0, 0).(RegisterResponse)
	check.True(t, ok, "Register must answer a RegisterResponse")
	if resp.Error != "" {
		t.Fatalf("Register errored: %s", resp.Error)
	}

	code, body := get(t, p, "/live")
	if code != http.StatusOK || len(body.Signals) != 1 || body.Signals[0].Signal != "db" {
		t.Fatalf("/live = %d %+v, want 200 with the db signal", code, body)
	}
	// Not a readiness signal, so readiness must not mention it.
	if _, ready := get(t, p, "/ready"); len(ready.Signals) != 0 {
		t.Fatalf("/ready reports %v for a liveness-only signal", ready.Signals)
	}
}

func TestProbe_RegisterRejectsEmptySignal(t *testing.T) {
	sub, _ := spawnProbe(t, time.Second)

	resp := register(sub, gen.PID{ID: 1}, "", ProbeLiveness, 0).(RegisterResponse)
	if resp.Error == "" {
		t.Fatal("an empty signal name must be rejected")
	}
}

// The bitmask is the point of Probe: a readiness signal going down must stop traffic
// without getting the container killed.
func TestProbe_ReadinessDownDoesNotFailLiveness(t *testing.T) {
	sub, p := spawnProbe(t, time.Second)
	register(sub, gen.PID{ID: 1}, "cache", ProbeReadiness, 0)

	sub.SendMessage(gen.PID{ID: 1}, MessageSignalDown{Signal: "cache"})

	if code, _ := get(t, p, "/ready"); code != http.StatusServiceUnavailable {
		t.Fatalf("/ready = %d with a down readiness signal, want 503", code)
	}
	if code, _ := get(t, p, "/live"); code != http.StatusOK {
		t.Fatalf("/live = %d, want 200 - a readiness signal must not fail liveness", code)
	}
	check.Equal(t, []gen.Atom{"cache"}, p.down)
}

// Down then up, with the callbacks firing once each: a flapping signal that reported up
// twice would have the consumer starting work it never stopped.
func TestProbe_SignalDownThenUp(t *testing.T) {
	sub, p := spawnProbe(t, time.Second)
	register(sub, gen.PID{ID: 1}, "db", ProbeLiveness, 0)

	sub.SendMessage(gen.PID{ID: 1}, MessageSignalDown{Signal: "db"})
	sub.SendMessage(gen.PID{ID: 1}, MessageSignalDown{Signal: "db"})
	if code, _ := get(t, p, "/live"); code != http.StatusServiceUnavailable {
		t.Fatalf("/live = %d after a down signal, want 503", code)
	}
	check.Equal(t, []gen.Atom{"db"}, p.down)

	sub.SendMessage(gen.PID{ID: 1}, MessageSignalUp{Signal: "db"})
	sub.SendMessage(gen.PID{ID: 1}, MessageSignalUp{Signal: "db"})
	if code, _ := get(t, p, "/live"); code != http.StatusOK {
		t.Fatalf("/live = %d after recovery, want 200", code)
	}
	check.Equal(t, []gen.Atom{"db"}, p.up)
}

// An unknown signal is not an error the caller can act on, so it is dropped rather than
// answered - but it must not resurrect a probe either.
func TestProbe_SignalsForUnknownNameAreIgnored(t *testing.T) {
	sub, p := spawnProbe(t, time.Second)
	register(sub, gen.PID{ID: 1}, "db", ProbeLiveness, 0)
	sub.SendMessage(gen.PID{ID: 1}, MessageSignalDown{Signal: "db"})

	sub.SendMessage(gen.PID{ID: 1}, MessageSignalUp{Signal: "nonexistent"})
	sub.SendMessage(gen.PID{ID: 1}, MessageHeartbeat{Signal: "nonexistent"})

	if code, _ := get(t, p, "/live"); code != http.StatusServiceUnavailable {
		t.Fatalf("/live = %d, want the down signal to stand", code)
	}
	check.Equal(t, 0, len(p.up))
}

// A heartbeat is the recovery path for a signal the sweep took down, distinct from an
// explicit SignalUp: the process is alive again and says so by beating.
func TestProbe_HeartbeatRecoversATimedOutSignal(t *testing.T) {
	sub, p := spawnProbe(t, time.Second)
	register(sub, gen.PID{ID: 1}, "worker", ProbeLiveness, time.Millisecond)

	// The sweep runs on the actor's own timer; firing it after the timeout has elapsed
	// is what an idle process looks like.
	time.Sleep(2 * time.Millisecond)
	sub.FireTimers()
	if code, _ := get(t, p, "/live"); code != http.StatusServiceUnavailable {
		t.Fatalf("/live = %d after a missed heartbeat, want 503", code)
	}
	check.Equal(t, []gen.Atom{"worker"}, p.down)

	sub.SendMessage(gen.PID{ID: 1}, MessageHeartbeat{Signal: "worker"})
	if code, _ := get(t, p, "/live"); code != http.StatusOK {
		t.Fatalf("/live = %d after a heartbeat, want 200", code)
	}
	check.Equal(t, []gen.Atom{"worker"}, p.up)
}

// A signal registered without a timeout never times out: a process that reports its own
// state explicitly must not be marked down for staying quiet.
func TestProbe_ZeroTimeoutNeverTimesOut(t *testing.T) {
	sub, p := spawnProbe(t, time.Millisecond)
	register(sub, gen.PID{ID: 1}, "quiet", ProbeLiveness, 0)

	time.Sleep(2 * time.Millisecond)
	sub.FireTimers()

	if code, _ := get(t, p, "/live"); code != http.StatusOK {
		t.Fatalf("/live = %d for a signal with no timeout, want 200", code)
	}
	check.Equal(t, 0, len(p.down))
}

// The registrant dying is the case a heartbeat timeout cannot cover quickly: the monitor
// fires at once and the signal must follow.
func TestProbe_RegistrantDownTakesItsSignalsDown(t *testing.T) {
	sub, p := spawnProbe(t, time.Second)
	owner := gen.PID{Node: "peer@host", ID: 7, Creation: 1}
	register(sub, owner, "db", ProbeLiveness, 0)
	register(sub, owner, "queue", ProbeReadiness, 0)
	register(sub, gen.PID{ID: 2}, "other", ProbeLiveness, 0)

	sub.DeliverDown(owner, gen.TerminateReasonKill)

	if code, _ := get(t, p, "/live"); code != http.StatusServiceUnavailable {
		t.Fatalf("/live = %d after the registrant died, want 503", code)
	}
	if code, _ := get(t, p, "/ready"); code != http.StatusServiceUnavailable {
		t.Fatalf("/ready = %d after the registrant died, want 503", code)
	}
	check.Equal(t, 2, len(p.down))
	// The other registrant's signal is untouched.
	for _, signal := range p.down {
		if signal == "other" {
			t.Fatal("a signal owned by a live process was taken down")
		}
	}
}

// Unregister removes the signal from the probe answer; an unknown one is reported back
// rather than silently accepted.
func TestProbe_Unregister(t *testing.T) {
	sub, p := spawnProbe(t, time.Second)
	register(sub, gen.PID{ID: 1}, "db", ProbeLiveness, 0)
	sub.SendMessage(gen.PID{ID: 1}, MessageSignalDown{Signal: "db"})

	result, _ := sub.Call(gen.PID{ID: 1}, UnregisterRequest{Signal: "db"})
	resp, ok := result.(UnregisterResponse)
	check.True(t, ok, "Unregister must answer an UnregisterResponse")
	if resp.Error != "" {
		t.Fatalf("Unregister errored: %s", resp.Error)
	}

	// The down signal is gone, so the probe is healthy again.
	code, body := get(t, p, "/live")
	if code != http.StatusOK || len(body.Signals) != 0 {
		t.Fatalf("/live = %d %+v after unregistering the only signal, want 200 with none", code, body)
	}

	result, _ = sub.Call(gen.PID{ID: 1}, UnregisterRequest{Signal: "db"})
	if resp := result.(UnregisterResponse); resp.Error == "" {
		t.Fatal("unregistering an unknown signal must be reported")
	}
}

// Without the wire types on the node a remote Register cannot be decoded, so the signal
// would be silently missing from the probe answer. The actor refuses to start instead.
func TestProbe_RefusesToStartWithoutRegisteredTypes(t *testing.T) {
	behavior := &probe{mux: http.NewServeMux(), interval: time.Second}
	sub := unit.Prepare(t, func() gen.ProcessBehavior { return behavior }, gen.ProcessOptions{})

	err := sub.Run()
	if err == nil {
		t.Fatal("expected Init to refuse an unregistered wire surface")
	}
	for _, want := range []string{"NetworkTypes()", docsURL} {
		if strings.Contains(err.Error(), want) == false {
			t.Fatalf("error %q does not carry %q", err, want)
		}
	}
}
