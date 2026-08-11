package metrics

// What matters about this actor is the text an exporter scrapes: a registration that
// silently does not appear, or an observation that lands on the wrong label set, is
// indistinguishable from a flat metric until someone reads a dashboard wrong. So every
// test here drives the real message path and then reads /metrics.
//
// Options.Mux takes an external mux, so the handler is exercised with httptest and no port
// is ever bound.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/check"
	"ergo.services/ergo/testing/unit"
)

// exporter is the metrics actor under test, holding the mux its handler lands on.
type exporter struct {
	Actor

	mux *http.ServeMux
}

func (e *exporter) Init(args ...any) (Options, error) {
	return Options{Mux: e.mux, CollectInterval: time.Hour}, nil
}

// spawnExporter brings the actor up with its wire types registered, as an application's
// Load does - Init refuses to start without them.
func spawnExporter(t *testing.T) (*unit.Subject, *exporter) {
	t.Helper()

	behavior := &exporter{mux: http.NewServeMux()}
	sub := unit.Prepare(t, func() gen.ProcessBehavior { return behavior }, gen.ProcessOptions{})
	check.NoError(t, sub.Node().Network().RegisterTypes(NetworkTypes()))
	check.NoError(t, sub.Run())
	return sub, behavior
}

// scrape returns the exporter's output the way Prometheus would read it.
func scrape(t *testing.T, e *exporter) string {
	t.Helper()

	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, DefaultPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", DefaultPath, rec.Code)
	}
	return rec.Body.String()
}

func callRegister(sub *unit.Subject, from gen.PID, req RegisterRequest) RegisterResponse {
	result, _ := sub.Call(from, req)
	resp, _ := result.(RegisterResponse)
	return resp
}

// requireSample fails unless the scrape carries a series whose text contains every
// fragment - name, labels and value together, so a value landing on the wrong label set
// cannot pass.
func requireSample(t *testing.T, scraped string, fragments ...string) {
	t.Helper()

	for _, line := range strings.Split(scraped, "\n") {
		matched := true
		for _, f := range fragments {
			if strings.Contains(line, f) == false {
				matched = false
				break
			}
		}
		if matched {
			return
		}
	}
	t.Fatalf("no series matching %v in:\n%s", fragments, scraped)
}

// A gauge takes the last value written, and it has to reach the scrape.
func TestMetrics_GaugeSetAndAdd(t *testing.T) {
	sub, e := spawnExporter(t)
	resp := callRegister(sub, gen.PID{ID: 1}, RegisterRequest{
		Name: "queue_depth", Help: "depth", Type: MetricGauge,
	})
	if resp.Error != "" {
		t.Fatalf("register errored: %s", resp.Error)
	}

	sub.SendMessage(gen.PID{ID: 1}, MessageGaugeSet{Name: "queue_depth", Value: 7})
	requireSample(t, scrape(t, e), "queue_depth", "7")

	sub.SendMessage(gen.PID{ID: 1}, MessageGaugeAdd{Name: "queue_depth", Value: -2})
	requireSample(t, scrape(t, e), "queue_depth", "5")
}

// A counter accumulates: two adds are one series at their sum, not the last value.
func TestMetrics_CounterAccumulates(t *testing.T) {
	sub, e := spawnExporter(t)
	callRegister(sub, gen.PID{ID: 1}, RegisterRequest{
		Name: "orders_total", Help: "orders", Type: MetricCounter,
	})

	sub.SendMessage(gen.PID{ID: 1}, MessageCounterAdd{Name: "orders_total", Value: 2})
	sub.SendMessage(gen.PID{ID: 1}, MessageCounterAdd{Name: "orders_total", Value: 3})

	requireSample(t, scrape(t, e), "orders_total", "5")
}

// A histogram renders its buckets, count and sum - the three things a latency panel reads.
func TestMetrics_HistogramObserve(t *testing.T) {
	sub, e := spawnExporter(t)
	callRegister(sub, gen.PID{ID: 1}, RegisterRequest{
		Name: "request_seconds", Help: "latency", Type: MetricHistogram,
		Buckets: []float64{0.1, 1},
	})

	sub.SendMessage(gen.PID{ID: 1}, MessageHistogramObserve{Name: "request_seconds", Value: 0.05})
	sub.SendMessage(gen.PID{ID: 1}, MessageHistogramObserve{Name: "request_seconds", Value: 5})

	scraped := scrape(t, e)
	requireSample(t, scraped, `request_seconds_bucket`, `le="0.1"`, "1")
	requireSample(t, scraped, "request_seconds_count", "2")
	requireSample(t, scraped, "request_seconds_sum", "5.05")
}

// Labelled metrics must route by label value: this is the assertion that catches an
// observation applied to the wrong series.
func TestMetrics_LabelsRouteToTheRightSeries(t *testing.T) {
	sub, e := spawnExporter(t)
	callRegister(sub, gen.PID{ID: 1}, RegisterRequest{
		Name: "fills_total", Help: "fills", Type: MetricCounter, Labels: []string{"venue"},
	})

	sub.SendMessage(gen.PID{ID: 1}, MessageCounterAdd{
		Name: "fills_total", Value: 4, Labels: []string{"binance"},
	})
	sub.SendMessage(gen.PID{ID: 1}, MessageCounterAdd{
		Name: "fills_total", Value: 1, Labels: []string{"bybit"},
	})

	scraped := scrape(t, e)
	requireSample(t, scraped, "fills_total", `venue="binance"`, "4")
	requireSample(t, scraped, "fills_total", `venue="bybit"`, "1")
}

// Re-registering the same shape is idempotent - two processes exporting the same metric is
// normal - but a different type under the same name is a conflict the caller must hear
// about, not a silent overwrite of someone else's series.
func TestMetrics_RegisterIsIdempotentAndRejectsConflicts(t *testing.T) {
	sub, _ := spawnExporter(t)
	req := RegisterRequest{Name: "shared", Help: "h", Type: MetricGauge, Labels: []string{"a"}}

	if resp := callRegister(sub, gen.PID{ID: 1}, req); resp.Error != "" {
		t.Fatalf("first register errored: %s", resp.Error)
	}
	if resp := callRegister(sub, gen.PID{ID: 2}, req); resp.Error != "" {
		t.Fatalf("re-registering the same shape must succeed: %s", resp.Error)
	}

	conflict := RegisterRequest{Name: "shared", Help: "h", Type: MetricCounter, Labels: []string{"a"}}
	if resp := callRegister(sub, gen.PID{ID: 2}, conflict); resp.Error == "" {
		t.Fatal("a different type under the same name must be rejected")
	}

	relabelled := RegisterRequest{Name: "shared", Help: "h", Type: MetricGauge, Labels: []string{"b"}}
	if resp := callRegister(sub, gen.PID{ID: 2}, relabelled); resp.Error == "" {
		t.Fatal("different labels under the same name must be rejected")
	}
}

// The actor's own metrics are not up for grabs: letting a consumer redefine one would
// corrupt the series the framework itself exports.
func TestMetrics_InternalNamesAreReserved(t *testing.T) {
	sub, e := spawnExporter(t)

	// Take a name straight from the scrape, so this cannot drift from what is internal.
	var internal string
	for _, line := range strings.Split(scrape(t, e), "\n") {
		if strings.HasPrefix(line, "# TYPE ergo_") {
			internal = strings.Fields(line)[2]
			break
		}
	}
	if internal == "" {
		t.Skip("no internal ergo_ metric in the scrape to test against")
	}

	resp := callRegister(sub, gen.PID{ID: 1}, RegisterRequest{
		Name: internal, Help: "hijack", Type: MetricGauge,
	})
	if resp.Error == "" {
		t.Fatalf("%s is internal and must not be registrable", internal)
	}
}

// Unregister removes the series. A stale metric left in the output reads as a live value
// long after the process producing it is gone.
func TestMetrics_UnregisterRemovesTheSeries(t *testing.T) {
	sub, e := spawnExporter(t)
	callRegister(sub, gen.PID{ID: 1}, RegisterRequest{
		Name: "temp_gauge", Help: "h", Type: MetricGauge,
	})
	sub.SendMessage(gen.PID{ID: 1}, MessageGaugeSet{Name: "temp_gauge", Value: 1})
	requireSample(t, scrape(t, e), "temp_gauge", "1")

	sub.SendMessage(gen.PID{ID: 1}, MessageUnregister{Name: "temp_gauge"})

	if strings.Contains(scrape(t, e), "temp_gauge") {
		t.Fatalf("temp_gauge still in the scrape after unregister:\n%s", scrape(t, e))
	}
}

// An observation for a metric nobody registered is dropped: a mistyped name must not take
// the exporter down and lose every other metric with it.
func TestMetrics_ObservationsForUnknownMetricsAreDropped(t *testing.T) {
	sub, e := spawnExporter(t)
	callRegister(sub, gen.PID{ID: 1}, RegisterRequest{
		Name: "known", Help: "h", Type: MetricCounter,
	})

	sub.SendMessage(gen.PID{ID: 1}, MessageGaugeSet{Name: "typo", Value: 1})
	sub.SendMessage(gen.PID{ID: 1}, MessageCounterAdd{Name: "typo", Value: 1})
	sub.SendMessage(gen.PID{ID: 1}, MessageHistogramObserve{Name: "typo", Value: 1})
	sub.SendMessage(gen.PID{ID: 1}, MessageUnregister{Name: "typo"})

	sub.ShouldTerminate().None().Assert()
	sub.SendMessage(gen.PID{ID: 1}, MessageCounterAdd{Name: "known", Value: 1})
	requireSample(t, scrape(t, e), "known", "1")
}

// Without the wire types on the node an observation cannot be decoded, so the metric
// silently stays flat. The actor refuses to start instead.
func TestMetrics_RefusesToStartWithoutRegisteredTypes(t *testing.T) {
	behavior := &exporter{mux: http.NewServeMux()}
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
