package metrics

import (
	"fmt"
	"testing"
	"time"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/check"
	"ergo.services/ergo/testing/unit"

	"github.com/prometheus/client_golang/prometheus"
)

func makeOwnerPID() gen.PID {
	return gen.PID{Node: "test@localhost", ID: 9999, Creation: 1}
}

func startTopNNode(t *testing.T) *unit.MockNode {
	t.Helper()
	return unit.StartNode(t, "test@localhost", gen.NodeOptions{})
}

func spawnTopNActor(t *testing.T, name string, topN int, order TopNOrder, shared *Shared) *unit.Subject {
	t.Helper()
	ta, err := startTopNNode(t).Spawn(TopNActorFactory, gen.ProcessOptions{}, TopNActorOptions{
		Name:     name,
		Help:     "test help",
		Labels:   []string{"pid", "name"},
		TopN:     topN,
		Order:    order,
		Shared:   shared,
		Interval: 100 * time.Millisecond,
		Owner:    makeOwnerPID(),
	})
	if err != nil {
		t.Fatalf("failed to spawn topN actor: %v", err)
	}
	return ta
}

// --- Init tests ---

func TestTopNActorInit(t *testing.T) {
	shared := NewShared()
	ta := spawnTopNActor(t, "test_init_metric", 5, TopNMax, shared)

	if ta.Terminated() {
		t.Fatal("actor should not be terminated after init")
	}

	// verify RegisterName event
	if _, err := ta.Node().ProcessPID("radar_topn_test_init_metric"); err != nil {
		t.Fatal("expected RegisterName event with name 'radar_topn_test_init_metric'")
	}

	// verify MonitorPID event for owner
	ta.ShouldMonitor().Target(makeOwnerPID()).Must()

	// verify SendAfter for flush scheduling
	ta.ShouldSendAfter().Message(messageFlush{}).Must()
}

func TestTopNActorInitTooFewArgs(t *testing.T) {
	_, err := startTopNNode(t).Spawn(TopNActorFactory, gen.ProcessOptions{}, "only_one")
	if err == nil {
		t.Fatal("expected error for too few args")
	}
}

func TestTopNActorInitNoArgs(t *testing.T) {
	_, err := startTopNNode(t).Spawn(TopNActorFactory, gen.ProcessOptions{})
	if err == nil {
		t.Fatal("expected error for no args")
	}
}

func TestTopNActorInitAlreadyRegisteredGaugeVec(t *testing.T) {
	shared := NewShared()

	// pre-register a GaugeVec with the same name, help, and labels
	// (Prometheus descriptor must match exactly for AlreadyRegisteredError)
	gv := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name:        "test_preregistered",
		Help:        "test help",
		ConstLabels: prometheus.Labels{"node": "test@localhost"},
	}, []string{"pid", "name"})
	shared.registry.MustRegister(gv)

	// spawn actor -- should reuse existing collector
	ta := spawnTopNActor(t, "test_preregistered", 5, TopNMax, shared)
	if ta.Terminated() {
		t.Fatal("actor should handle already-registered GaugeVec gracefully")
	}
}

// --- HandleMessage tests ---

func TestTopNActorObserve(t *testing.T) {
	shared := NewShared()
	ta := spawnTopNActor(t, "test_observe", 3, TopNMax, shared)

	sender := gen.PID{Node: "test@localhost", ID: 5000, Creation: 1}

	// send observations
	ta.SendMessage(sender, MessageTopNObserve{Value: 10, Labels: []string{"pid1", "actor1"}})
	ta.SendMessage(sender, MessageTopNObserve{Value: 20, Labels: []string{"pid2", "actor2"}})
	ta.SendMessage(sender, MessageTopNObserve{Value: 30, Labels: []string{"pid3", "actor3"}})

	if ta.Terminated() {
		t.Fatal("actor should not terminate after observations")
	}

	// flush and verify observations were actually stored
	ta.SendMessage(sender, messageFlush{})

	families, err := shared.registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather: %v", err)
	}

	var found bool
	for _, mf := range families {
		if mf.GetName() == "test_observe" {
			found = true
			if len(mf.GetMetric()) != 3 {
				t.Fatalf("expected 3 metric samples, got %d", len(mf.GetMetric()))
			}
			values := make(map[float64]bool)
			for _, m := range mf.GetMetric() {
				values[m.Gauge.GetValue()] = true
			}
			if values[10] == false || values[20] == false || values[30] == false {
				t.Fatalf("expected values {10, 20, 30}, got %v", values)
			}
		}
	}
	if found == false {
		t.Fatal("metric family test_observe not found")
	}
}

func TestTopNActorObserveRespectTopN(t *testing.T) {
	shared := NewShared()
	ta := spawnTopNActor(t, "test_observe_topn", 2, TopNMax, shared)

	sender := gen.PID{Node: "test@localhost", ID: 5000, Creation: 1}

	// send more observations than topN
	ta.SendMessage(sender, MessageTopNObserve{Value: 10, Labels: []string{"pid1", "actor1"}})
	ta.SendMessage(sender, MessageTopNObserve{Value: 20, Labels: []string{"pid2", "actor2"}})
	ta.SendMessage(sender, MessageTopNObserve{Value: 30, Labels: []string{"pid3", "actor3"}})
	ta.SendMessage(sender, MessageTopNObserve{Value: 5, Labels: []string{"pid4", "actor4"}})

	// trigger flush to write metrics
	ta.SendMessage(sender, messageFlush{})

	if ta.Terminated() {
		t.Fatal("actor should not terminate after flush")
	}

	// verify only top-2 values are in the gauge
	families, err := shared.registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather: %v", err)
	}

	var found bool
	for _, mf := range families {
		if mf.GetName() == "test_observe_topn" {
			found = true
			if len(mf.GetMetric()) != 2 {
				t.Fatalf("expected 2 metric samples (topN=2), got %d", len(mf.GetMetric()))
			}
			// collect values
			values := make(map[float64]bool)
			for _, m := range mf.GetMetric() {
				values[m.Gauge.GetValue()] = true
			}
			if values[20] == false || values[30] == false {
				t.Fatalf("expected top-2 values {20, 30}, got %v", values)
			}
		}
	}
	if found == false {
		t.Fatal("metric family test_observe_topn not found")
	}
}

func TestTopNActorFlush(t *testing.T) {
	shared := NewShared()
	ta := spawnTopNActor(t, "test_flush", 5, TopNMax, shared)

	sender := gen.PID{Node: "test@localhost", ID: 5000, Creation: 1}

	ta.SendMessage(sender, MessageTopNObserve{Value: 100, Labels: []string{"pid1", "worker1"}})
	ta.SendMessage(sender, MessageTopNObserve{Value: 200, Labels: []string{"pid2", "worker2"}})

	// flush
	ta.SendMessage(sender, messageFlush{})

	if ta.Terminated() {
		t.Fatal("actor should not terminate after flush")
	}

	// verify metrics were written
	families, err := shared.registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather: %v", err)
	}

	var found bool
	for _, mf := range families {
		if mf.GetName() == "test_flush" {
			found = true
			if len(mf.GetMetric()) != 2 {
				t.Fatalf("expected 2 samples, got %d", len(mf.GetMetric()))
			}
		}
	}
	if found == false {
		t.Fatal("metric family test_flush not found after flush")
	}
}

func TestTopNActorFlushReschedulesTimer(t *testing.T) {
	shared := NewShared()
	ta := spawnTopNActor(t, "test_flush_timer", 5, TopNMax, shared)

	sender := gen.PID{Node: "test@localhost", ID: 5000, Creation: 1}

	mark := ta.Mark()

	// flush
	ta.SendMessage(sender, messageFlush{})

	// verify a new SendAfter(messageFlush) was scheduled
	ta.ShouldSendAfter().
		Message(messageFlush{}).
		Where(func(r check.SendAfter) bool { return r.After > 0 }).
		Since(mark).
		Must()
}

func TestTopNActorFlushClearsHeap(t *testing.T) {
	shared := NewShared()
	ta := spawnTopNActor(t, "test_flush_clear", 5, TopNMax, shared)

	sender := gen.PID{Node: "test@localhost", ID: 5000, Creation: 1}

	// first cycle
	ta.SendMessage(sender, MessageTopNObserve{Value: 100, Labels: []string{"pid1", "a"}})
	ta.SendMessage(sender, MessageTopNObserve{Value: 200, Labels: []string{"pid2", "b"}})
	ta.SendMessage(sender, messageFlush{})

	// second cycle: only 1 observation
	ta.SendMessage(sender, MessageTopNObserve{Value: 999, Labels: []string{"pid3", "c"}})
	ta.SendMessage(sender, messageFlush{})

	families, err := shared.registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather: %v", err)
	}

	for _, mf := range families {
		if mf.GetName() == "test_flush_clear" {
			// only the second cycle's entry should remain
			if len(mf.GetMetric()) != 1 {
				t.Fatalf("expected 1 metric sample after second flush, got %d", len(mf.GetMetric()))
			}
			if mf.GetMetric()[0].Gauge.GetValue() != 999 {
				t.Fatalf("expected value 999, got %f", mf.GetMetric()[0].Gauge.GetValue())
			}
			return
		}
	}
	t.Fatal("metric family test_flush_clear not found")
}

func TestTopNActorMinOrder(t *testing.T) {
	shared := NewShared()
	ta := spawnTopNActor(t, "test_min_order", 2, TopNMin, shared)

	sender := gen.PID{Node: "test@localhost", ID: 5000, Creation: 1}

	ta.SendMessage(sender, MessageTopNObserve{Value: 100, Labels: []string{"pid1", "a"}})
	ta.SendMessage(sender, MessageTopNObserve{Value: 50, Labels: []string{"pid2", "b"}})
	ta.SendMessage(sender, MessageTopNObserve{Value: 10, Labels: []string{"pid3", "c"}})
	ta.SendMessage(sender, MessageTopNObserve{Value: 200, Labels: []string{"pid4", "d"}})
	ta.SendMessage(sender, messageFlush{})

	families, err := shared.registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather: %v", err)
	}

	for _, mf := range families {
		if mf.GetName() == "test_min_order" {
			if len(mf.GetMetric()) != 2 {
				t.Fatalf("expected 2 metric samples, got %d", len(mf.GetMetric()))
			}
			values := make(map[float64]bool)
			for _, m := range mf.GetMetric() {
				values[m.Gauge.GetValue()] = true
			}
			// bottom-2: 10, 50
			if values[10] == false || values[50] == false {
				t.Fatalf("expected bottom-2 values {10, 50}, got %v", values)
			}
			return
		}
	}
	t.Fatal("metric family test_min_order not found")
}

// --- Owner down / termination tests ---

func TestTopNActorOwnerDown(t *testing.T) {
	shared := NewShared()
	ta := spawnTopNActor(t, "test_owner_down", 5, TopNMax, shared)

	owner := makeOwnerPID()

	// send MessageDownPID for the owner
	ta.SendMessage(owner, gen.MessageDownPID{
		PID:    owner,
		Reason: gen.TerminateReasonNormal,
	})

	if ta.Terminated() == false {
		t.Fatal("actor should terminate when owner goes down")
	}

	if ta.Reason() != gen.TerminateReasonNormal {
		t.Fatalf("expected TerminateReasonNormal, got %v", ta.Reason())
	}

	// verify GaugeVec was unregistered from prometheus
	// trying to register same name/help/labels again should succeed
	gv := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name:        "test_owner_down",
		Help:        "test help",
		ConstLabels: prometheus.Labels{"node": "test@localhost"},
	}, []string{"pid", "name"})
	if err := shared.registry.Register(gv); err != nil {
		t.Fatalf("expected GaugeVec to be unregistered after owner down, got: %v", err)
	}
}

func TestTopNActorOwnerDownAfterObservations(t *testing.T) {
	shared := NewShared()
	ta := spawnTopNActor(t, "test_owner_down_obs", 5, TopNMax, shared)

	owner := makeOwnerPID()
	sender := gen.PID{Node: "test@localhost", ID: 5000, Creation: 1}

	// send observations
	ta.SendMessage(sender, MessageTopNObserve{Value: 42, Labels: []string{"pid1", "worker"}})
	ta.SendMessage(sender, messageFlush{})

	// then owner goes down
	ta.SendMessage(owner, gen.MessageDownPID{
		PID:    owner,
		Reason: gen.TerminateReasonNormal,
	})

	if ta.Terminated() == false {
		t.Fatal("actor should terminate when owner goes down")
	}
}

// --- HandleCall tests ---

func TestTopNActorHandleCallDefault(t *testing.T) {
	shared := NewShared()
	ta := spawnTopNActor(t, "test_call_default", 5, TopNMax, shared)

	caller := gen.PID{Node: "test@localhost", ID: 5000, Creation: 1}

	if _, err := ta.Call(caller, "unknown request"); err != nil {
		t.Fatalf("HandleCall should not return error for unknown request, got %v", err)
	}
	if ta.Terminated() {
		t.Fatal("actor should not terminate from HandleCall with unknown request")
	}
}

// --- Unrelated messages are ignored ---

func TestTopNActorIgnoresUnknownMessages(t *testing.T) {
	shared := NewShared()
	ta := spawnTopNActor(t, "test_unknown_msg", 5, TopNMax, shared)

	sender := gen.PID{Node: "test@localhost", ID: 5000, Creation: 1}

	ta.SendMessage(sender, "some random string message")
	ta.SendMessage(sender, 12345)
	ta.SendMessage(sender, struct{ Foo string }{Foo: "bar"})

	if ta.Terminated() {
		t.Fatal("actor should not terminate from unknown messages")
	}
}

// --- Multiple flush cycles ---

func TestTopNActorMultipleFlushCycles(t *testing.T) {
	shared := NewShared()
	ta := spawnTopNActor(t, "test_multi_flush", 3, TopNMax, shared)

	sender := gen.PID{Node: "test@localhost", ID: 5000, Creation: 1}

	// cycle 1
	ta.SendMessage(sender, MessageTopNObserve{Value: 10, Labels: []string{"a", "x"}})
	ta.SendMessage(sender, MessageTopNObserve{Value: 20, Labels: []string{"b", "y"}})
	ta.SendMessage(sender, messageFlush{})

	families, _ := shared.registry.Gather()
	for _, mf := range families {
		if mf.GetName() == "test_multi_flush" {
			if len(mf.GetMetric()) != 2 {
				t.Fatalf("cycle 1: expected 2 samples, got %d", len(mf.GetMetric()))
			}
		}
	}

	// cycle 2: different data
	ta.SendMessage(sender, MessageTopNObserve{Value: 100, Labels: []string{"c", "z"}})
	ta.SendMessage(sender, messageFlush{})

	families, _ = shared.registry.Gather()
	for _, mf := range families {
		if mf.GetName() == "test_multi_flush" {
			if len(mf.GetMetric()) != 1 {
				t.Fatalf("cycle 2: expected 1 sample, got %d", len(mf.GetMetric()))
			}
			if mf.GetMetric()[0].Gauge.GetValue() != 100 {
				t.Fatalf("cycle 2: expected 100, got %f", mf.GetMetric()[0].Gauge.GetValue())
			}
		}
	}

	// cycle 3: empty
	ta.SendMessage(sender, messageFlush{})

	families, _ = shared.registry.Gather()
	for _, mf := range families {
		if mf.GetName() == "test_multi_flush" {
			t.Fatalf("cycle 3: expected no metric family (all cleared), but got %d samples", len(mf.GetMetric()))
		}
	}

	if ta.Terminated() {
		t.Fatal("actor should not terminate after multiple flush cycles")
	}
}

// --- Factory test ---

func TestTopNActorFactory(t *testing.T) {
	b := TopNActorFactory()
	if b == nil {
		t.Fatal("TopNActorFactory returned nil")
	}
	if _, ok := b.(*topNActor); ok == false {
		t.Fatalf("expected *topNActor, got %T", b)
	}
}

// --- Separate shared instances ---

func TestTopNActorSeparateShared(t *testing.T) {
	shared1 := NewShared()
	shared2 := NewShared()

	owner := makeOwnerPID()
	labels := []string{"id"}

	ta1, err := startTopNNode(t).Spawn(TopNActorFactory, gen.ProcessOptions{}, TopNActorOptions{
		Name: "metric_a", Help: "help a", Labels: labels,
		TopN: 3, Order: TopNMax, Shared: shared1,
		Interval: 100 * time.Millisecond, Owner: owner,
	})
	if err != nil {
		t.Fatalf("failed to spawn ta1: %v", err)
	}

	ta2, err := startTopNNode(t).Spawn(TopNActorFactory, gen.ProcessOptions{}, TopNActorOptions{
		Name: "metric_b", Help: "help b", Labels: labels,
		TopN: 3, Order: TopNMin, Shared: shared2,
		Interval: 100 * time.Millisecond, Owner: owner,
	})
	if err != nil {
		t.Fatalf("failed to spawn ta2: %v", err)
	}

	sender := gen.PID{Node: "test@localhost", ID: 5000, Creation: 1}

	ta1.SendMessage(sender, MessageTopNObserve{Value: 50, Labels: []string{"x"}})
	ta1.SendMessage(sender, messageFlush{})

	ta2.SendMessage(sender, MessageTopNObserve{Value: 25, Labels: []string{"y"}})
	ta2.SendMessage(sender, messageFlush{})

	// shared1 should only have metric_a
	families1, _ := shared1.registry.Gather()
	for _, mf := range families1 {
		if mf.GetName() == "metric_b" {
			t.Fatal("shared1 should not contain metric_b")
		}
	}

	// shared2 should only have metric_b
	families2, _ := shared2.registry.Gather()
	for _, mf := range families2 {
		if mf.GetName() == "metric_a" {
			t.Fatal("shared2 should not contain metric_a")
		}
	}

	if ta1.Terminated() || ta2.Terminated() {
		t.Fatal("neither actor should be terminated")
	}
}

// --- High volume test ---

func TestTopNActorHighVolume(t *testing.T) {
	shared := NewShared()
	ta := spawnTopNActor(t, "test_high_volume", 10, TopNMax, shared)

	sender := gen.PID{Node: "test@localhost", ID: 5000, Creation: 1}

	// send 1000 observations
	for i := 0; i < 1000; i++ {
		ta.SendMessage(sender, MessageTopNObserve{
			Value:  float64(i),
			Labels: []string{fmt.Sprintf("pid_%d", i), fmt.Sprintf("actor_%d", i)},
		})
	}
	ta.SendMessage(sender, messageFlush{})

	if ta.Terminated() {
		t.Fatal("actor should handle high volume without termination")
	}

	families, err := shared.registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather: %v", err)
	}

	for _, mf := range families {
		if mf.GetName() == "test_high_volume" {
			if len(mf.GetMetric()) != 10 {
				t.Fatalf("expected 10 metric samples (topN=10), got %d", len(mf.GetMetric()))
			}
			// all values should be >= 990
			for _, m := range mf.GetMetric() {
				if m.Gauge.GetValue() < 990 {
					t.Fatalf("unexpected value %f in top-10 (should be >= 990)", m.Gauge.GetValue())
				}
			}
			return
		}
	}
	t.Fatal("metric family test_high_volume not found")
}
