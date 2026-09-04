package metrics

// Three things the actor-level tests do not reach: the collect cycle that turns node stats
// into the ergo_* series, the bucketing a distribution panel is drawn from, and the client
// helpers a consumer actually calls.

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/check"
	"ergo.services/ergo/testing/unit"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The collect tick is what makes the exporter say anything about the node at all. A cycle
// that errors or exports nothing looks identical to a healthy idle node on a dashboard.
func TestMetrics_CollectCycleExportsNodeMetrics(t *testing.T) {
	sub, e := spawnExporter(t)

	// The mock does not invent node stats: a cycle reads Node().Info, so the test supplies
	// the numbers it should export.
	sub.Node().OnInfo(func() (gen.NodeInfo, error) {
		return gen.NodeInfo{Uptime: 42, ProcessesTotal: 7, MemoryUsed: 1024}, nil
	})

	// Init queues the first cycle as a self-send, which the harness does not drain on its
	// own; delivering it is what runs a cycle.
	sub.SendMessage(gen.PID{}, messageCollectMetrics{})

	scraped := scrape(t, e)
	// The values, not just the names: a cycle that exports zeros for a live node is the
	// failure a dashboard cannot distinguish from an idle one.
	requireSample(t, scraped, "ergo_node_uptime_seconds", "42")
	requireSample(t, scraped, "ergo_processes_total", "7")
	requireSample(t, scraped, "ergo_memory_used_bytes", "1024")
	// A GaugeVec renders nothing until a child is set, so its presence proves the cycle
	// walked the per-level loop rather than just registering the metric at init.
	requireSample(t, scraped, "ergo_log_messages_total", `level="error"`)
	sub.ShouldTerminate().None().Assert()
}

// The mailbox-depth distribution is the one collector with arithmetic worth pinning: an
// off-by-one on a boundary shifts every row of the panel, and the overflow bucket is the
// only signal that a mailbox is running away.
func TestDepth_BucketsByBoundaryAndTracksMax(t *testing.T) {
	var cm sync.Map
	dm := &depthMetrics{}
	dm.init(&cm, prometheus.NewRegistry(), prometheus.Labels{"node": "test@host"})

	dm.begin()
	for _, depth := range []uint64{
		0,     // ignored: an empty mailbox is not a data point
		1,     // first bucket, inclusive
		5,     // boundary is inclusive, so this is the "5" bucket and not "10"
		6,     // next bucket up
		20000, // past the last boundary: overflow
	} {
		dm.observe(gen.ProcessShortInfo{PID: gen.PID{ID: depth}, MessagesMailbox: depth}, 10)
	}

	check.Equal(t, uint64(20000), dm.max)
	// depthBoundaries is 1,5,10,50,...; the counts land per boundary, with one overflow.
	check.Equal(t, float64(1), dm.buckets[0])                    // depth 1
	check.Equal(t, float64(1), dm.buckets[1])                    // depth 5
	check.Equal(t, float64(1), dm.buckets[2])                    // depth 6 -> "10"
	check.Equal(t, float64(1), dm.buckets[len(depthBoundaries)]) // depth 20000 -> overflow

	// flush writes what it accumulated, and must not panic on the vec labels.
	dm.flush()
	check.Equal(t, float64(20000), testutilGauge(t, &cm, "ergo_mailbox_depth_max"))
}

// testutilGauge reads a plain internal gauge back out of the metric map.
func testutilGauge(t *testing.T, cm *sync.Map, name string) float64 {
	t.Helper()
	return testutil.ToFloat64(gaugeFromMap(cm, name))
}

// helperClient drives the package helpers against a target so their Call/Send shapes are
// exercised through a real process.
type helperClient struct {
	Actor

	target gen.Atom
	err    error
}

func (c *helperClient) HandleMessage(from gen.PID, message any) error {
	switch message {
	case "gauge":
		c.err = RegisterGauge(c, c.target, "g", "help", nil)
	case "counter":
		c.err = RegisterCounter(c, c.target, "c", "help", []string{"venue"})
	case "histogram":
		c.err = RegisterHistogram(c, c.target, "h", "help", nil, []float64{1})
	case "set":
		c.err = GaugeSet(c, c.target, "g", 3, nil)
	case "add":
		c.err = GaugeAdd(c, c.target, "g", 1, nil)
	case "count":
		c.err = CounterAdd(c, c.target, "c", 2, []string{"binance"})
	case "observe":
		c.err = HistogramObserve(c, c.target, "h", 0.5, nil)
	case "unregister":
		c.err = Unregister(c, c.target, "g")
	}
	return nil
}

// The register helpers unwrap the response's Error field into a real error: a caller that
// only checks err must not carry on believing its metric exists.
func TestHelpers_RegisterSurfacesTheResponseError(t *testing.T) {
	target := gen.Atom("metrics")
	behavior := &helperClient{target: target}
	sub := unit.Prepare(t, func() gen.ProcessBehavior { return behavior }, gen.ProcessOptions{},
		Options{Mux: http.NewServeMux()})
	check.NoError(t, sub.Node().Network().RegisterTypes(NetworkTypes()))
	check.NoError(t, sub.Run())

	sub.OnCall(target).Respond(RegisterResponse{})
	for _, trigger := range []string{"gauge", "counter", "histogram"} {
		sub.SendMessage(gen.PID{ID: 1}, trigger)
		check.NoError(t, behavior.err)
	}

	sub.OnCall(target).Respond(RegisterResponse{Error: "already registered"})
	sub.SendMessage(gen.PID{ID: 1}, "gauge")
	if behavior.err == nil {
		t.Fatal("RegisterGauge must surface the response error")
	}

	sub.OnCall(target).Respond("surprise")
	sub.SendMessage(gen.PID{ID: 1}, "counter")
	if behavior.err == nil {
		t.Fatal("RegisterCounter must reject a response it cannot interpret")
	}
}

// The observation helpers must send the messages the actor handles - a mismatch is a
// metric that stays flat while the code reports success.
func TestHelpers_SendTheMessagesTheActorHandles(t *testing.T) {
	target := gen.Atom("metrics")
	behavior := &helperClient{target: target}
	sub := unit.Prepare(t, func() gen.ProcessBehavior { return behavior }, gen.ProcessOptions{},
		Options{Mux: http.NewServeMux()})
	check.NoError(t, sub.Node().Network().RegisterTypes(NetworkTypes()))
	check.NoError(t, sub.Run())

	for _, tc := range []struct {
		trigger string
		want    any
	}{
		{"set", MessageGaugeSet{Name: "g", Value: 3}},
		{"add", MessageGaugeAdd{Name: "g", Value: 1}},
		{"count", MessageCounterAdd{Name: "c", Value: 2, Labels: []string{"binance"}}},
		{"observe", MessageHistogramObserve{Name: "h", Value: 0.5}},
		{"unregister", MessageUnregister{Name: "g"}},
	} {
		sub.SendMessage(gen.PID{ID: 1}, tc.trigger)
		check.NoError(t, behavior.err)
		sub.ShouldSend().To(target).Message(tc.want).Once().Assert()
	}
}

// A process that registered metrics and then died leaves series behind that read as live
// values forever. Its own metrics go with it - and nobody else's do.
func TestMetrics_OwnerDownUnregistersItsMetricsOnly(t *testing.T) {
	sub, e := spawnExporter(t)
	owner := gen.PID{Node: "peer@host", ID: 7, Creation: 1}
	other := gen.PID{ID: 2}

	callRegister(sub, owner, RegisterRequest{Name: "owned", Help: "h", Type: MetricGauge})
	callRegister(sub, other, RegisterRequest{Name: "kept", Help: "h", Type: MetricGauge})
	sub.SendMessage(owner, MessageGaugeSet{Name: "owned", Value: 1})
	sub.SendMessage(other, MessageGaugeSet{Name: "kept", Value: 2})

	sub.DeliverDown(owner, gen.TerminateReasonKill)

	scraped := scrape(t, e)
	if strings.Contains(scraped, "owned") {
		t.Fatalf("the dead owner's metric survived:\n%s", scraped)
	}
	requireSample(t, scraped, "kept", "2")
	// The node's own series are not owned by any consumer and must never be swept.
	if strings.Contains(scraped, "ergo_processes_total") == false {
		t.Fatal("an internal metric was swept along with the owner's")
	}
}

// The inspect surface is what an operator reads in the observer: every metric by name with
// its current value, the registered/custom counts, and the endpoint being served.
func TestMetrics_InspectReportsRegistrationsAndEndpoint(t *testing.T) {
	sub, _ := spawnExporter(t)
	callRegister(sub, gen.PID{ID: 1}, RegisterRequest{Name: "one", Help: "h", Type: MetricGauge})
	sub.SendMessage(gen.PID{ID: 1}, MessageGaugeSet{Name: "one", Value: 3})

	state, err := sub.Inspect(gen.PID{})
	check.NoError(t, err)

	// The consumer's metric is reported under its own name, with the value it was set to.
	check.Equal(t, "3.00", state["one"])
	check.Equal(t, "1", state["custom_metrics"])
	check.Equal(t, DefaultPath, state["http_path"])
	// Internal series are reported too - the same list the scrape carries.
	if _, ok := state["ergo_processes_total"]; ok == false {
		t.Fatalf("inspect omits the node's own metrics: %v", state)
	}
	if state["total_metrics"] == "" {
		t.Fatalf("inspect omits the registration count: %v", state)
	}
}

// The top-N helpers are the other half of the public API: a mismatch between what they send
// and what the actor handles is a metric that never appears.
func TestHelpers_TopN(t *testing.T) {
	target := gen.Atom("metrics")
	behavior := &topNClient{target: target}
	sub := unit.Prepare(t, func() gen.ProcessBehavior { return behavior }, gen.ProcessOptions{},
		Options{Mux: http.NewServeMux()})
	check.NoError(t, sub.Node().Network().RegisterTypes(NetworkTypes()))
	check.NoError(t, sub.Run())

	sub.OnCall(target).Respond(RegisterResponse{})
	sub.SendMessage(gen.PID{ID: 1}, "register")
	check.NoError(t, behavior.err)

	sub.OnCall(target).Respond(RegisterResponse{Error: "taken"})
	sub.SendMessage(gen.PID{ID: 1}, "register")
	if behavior.err == nil {
		t.Fatal("RegisterTopN must surface the response error")
	}

	sub.SendMessage(gen.PID{ID: 1}, "observe")
	check.NoError(t, behavior.err)
	sub.ShouldSend().To(target).
		Message(MessageTopNObserve{Value: 1.5, Labels: []string{"a"}}).Once().Assert()
}

type topNClient struct {
	Actor

	target gen.Atom
	err    error
}

func (c *topNClient) HandleMessage(from gen.PID, message any) error {
	switch message {
	case "register":
		c.err = RegisterTopN(c, c.target, "slow", "help", 10, TopNMax, []string{"pid"})
	case "observe":
		c.err = TopNObserve(c, c.target, 1.5, []string{"a"})
	}
	return nil
}
