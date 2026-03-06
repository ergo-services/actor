package metrics

import (
	"container/heap"
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestNewTopNHeap(t *testing.T) {
	h := newTopNHeap(TopNMax)
	if h == nil {
		t.Fatal("newTopNHeap returned nil")
	}
	if h.order != TopNMax {
		t.Fatalf("expected order TopNMax, got %d", h.order)
	}
	if h.Len() != 0 {
		t.Fatalf("expected empty heap, got %d entries", h.Len())
	}

	h2 := newTopNHeap(TopNMin)
	if h2.order != TopNMin {
		t.Fatalf("expected order TopNMin, got %d", h2.order)
	}
}

func TestTopNHeapInterface(t *testing.T) {
	// compile-time check is in topn_heap.go, but verify at runtime too
	var _ heap.Interface = newTopNHeap(TopNMax)
}

func TestTopNMaxObserve(t *testing.T) {
	h := newTopNHeap(TopNMax)
	topN := 3

	// add fewer than topN entries
	h.Observe(10, []string{"a"}, topN)
	h.Observe(20, []string{"b"}, topN)
	if h.Len() != 2 {
		t.Fatalf("expected 2 entries, got %d", h.Len())
	}

	// fill to capacity
	h.Observe(30, []string{"c"}, topN)
	if h.Len() != 3 {
		t.Fatalf("expected 3 entries, got %d", h.Len())
	}

	// add value smaller than min -- should be rejected
	h.Observe(5, []string{"d"}, topN)
	if h.Len() != 3 {
		t.Fatalf("expected 3 entries after small value, got %d", h.Len())
	}

	// verify root is the minimum (min-heap for TopNMax)
	if h.entries[0].value != 10 {
		t.Fatalf("expected root value 10, got %f", h.entries[0].value)
	}

	// add value larger than current min -- should replace it
	h.Observe(50, []string{"e"}, topN)
	if h.Len() != 3 {
		t.Fatalf("expected 3 entries after replacement, got %d", h.Len())
	}

	// collect all values
	values := make(map[float64]bool)
	for _, e := range h.entries {
		values[e.value] = true
	}
	// should contain 20, 30, 50 (the top 3)
	for _, v := range []float64{20, 30, 50} {
		if values[v] == false {
			t.Fatalf("expected value %f to be in heap", v)
		}
	}
	if values[10] == true {
		t.Fatal("value 10 should have been evicted")
	}
	if values[5] == true {
		t.Fatal("value 5 should not have been added")
	}
}

func TestTopNMinObserve(t *testing.T) {
	h := newTopNHeap(TopNMin)
	topN := 3

	h.Observe(50, []string{"a"}, topN)
	h.Observe(40, []string{"b"}, topN)
	h.Observe(30, []string{"c"}, topN)

	// add value larger than max -- should be rejected
	h.Observe(100, []string{"d"}, topN)
	if h.Len() != 3 {
		t.Fatalf("expected 3 entries after large value, got %d", h.Len())
	}

	// root should be the maximum (max-heap for TopNMin)
	if h.entries[0].value != 50 {
		t.Fatalf("expected root value 50, got %f", h.entries[0].value)
	}

	// add value smaller than current max -- should replace
	h.Observe(10, []string{"e"}, topN)
	if h.Len() != 3 {
		t.Fatalf("expected 3 entries after replacement, got %d", h.Len())
	}

	values := make(map[float64]bool)
	for _, e := range h.entries {
		values[e.value] = true
	}
	// should contain 10, 30, 40 (the bottom 3)
	for _, v := range []float64{10, 30, 40} {
		if values[v] == false {
			t.Fatalf("expected value %f to be in heap", v)
		}
	}
	if values[50] == true {
		t.Fatal("value 50 should have been evicted")
	}
}

func TestTopNHeapObserveLabels(t *testing.T) {
	h := newTopNHeap(TopNMax)
	h.Observe(100, []string{"pid1", "actor1", "app1"}, 2)
	h.Observe(200, []string{"pid2", "actor2", "app2"}, 2)

	// labels should be preserved
	found := false
	for _, e := range h.entries {
		if e.value == 200 {
			if len(e.labels) != 3 || e.labels[0] != "pid2" || e.labels[1] != "actor2" || e.labels[2] != "app2" {
				t.Fatalf("unexpected labels for value 200: %v", e.labels)
			}
			found = true
		}
	}
	if found == false {
		t.Fatal("entry with value 200 not found")
	}
}

func TestTopNHeapObserveTopNZero(t *testing.T) {
	h := newTopNHeap(TopNMax)
	// topN=0 means no entries should be kept
	h.Observe(100, []string{"a"}, 0)
	if h.Len() != 0 {
		t.Fatalf("expected 0 entries with topN=0, got %d", h.Len())
	}
}

func TestTopNHeapObserveTopNOne(t *testing.T) {
	h := newTopNHeap(TopNMax)
	h.Observe(10, []string{"a"}, 1)
	h.Observe(20, []string{"b"}, 1)
	h.Observe(5, []string{"c"}, 1)

	if h.Len() != 1 {
		t.Fatalf("expected 1 entry with topN=1, got %d", h.Len())
	}
	if h.entries[0].value != 20 {
		t.Fatalf("expected value 20, got %f", h.entries[0].value)
	}
}

func TestTopNHeapReset(t *testing.T) {
	h := newTopNHeap(TopNMax)
	h.Observe(10, []string{"a"}, 5)
	h.Observe(20, []string{"b"}, 5)
	h.Observe(30, []string{"c"}, 5)

	if h.Len() != 3 {
		t.Fatalf("expected 3 entries before reset, got %d", h.Len())
	}

	h.Reset()
	if h.Len() != 0 {
		t.Fatalf("expected 0 entries after reset, got %d", h.Len())
	}

	// verify capacity is preserved (no reallocation needed for same size)
	if cap(h.entries) < 3 {
		t.Fatalf("expected capacity >= 3 after reset, got %d", cap(h.entries))
	}
}

func TestTopNHeapFlush(t *testing.T) {
	registry := prometheus.NewRegistry()
	gv := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "test_topn_flush",
		Help: "test",
	}, []string{"pid", "name"})
	registry.MustRegister(gv)

	h := newTopNHeap(TopNMax)
	h.Observe(100, []string{"pid1", "actor1"}, 3)
	h.Observe(200, []string{"pid2", "actor2"}, 3)
	h.Observe(300, []string{"pid3", "actor3"}, 3)

	h.Flush(gv)

	// heap should be empty after flush
	if h.Len() != 0 {
		t.Fatalf("expected 0 entries after flush, got %d", h.Len())
	}

	// verify gauge values were written
	metrics, err := registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	if len(metrics) != 1 {
		t.Fatalf("expected 1 metric family, got %d", len(metrics))
	}

	mf := metrics[0]
	if len(mf.GetMetric()) != 3 {
		t.Fatalf("expected 3 metric samples, got %d", len(mf.GetMetric()))
	}

	// collect values from gathered metrics
	gatheredValues := make(map[float64]bool)
	for _, m := range mf.GetMetric() {
		gatheredValues[m.Gauge.GetValue()] = true
	}
	for _, v := range []float64{100, 200, 300} {
		if gatheredValues[v] == false {
			t.Fatalf("expected gauge value %f to be present", v)
		}
	}
}

func TestTopNHeapFlushResetsGauge(t *testing.T) {
	registry := prometheus.NewRegistry()
	gv := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "test_topn_flush_reset",
		Help: "test",
	}, []string{"pid"})
	registry.MustRegister(gv)

	h := newTopNHeap(TopNMax)

	// first cycle: 3 entries
	h.Observe(10, []string{"pid1"}, 5)
	h.Observe(20, []string{"pid2"}, 5)
	h.Observe(30, []string{"pid3"}, 5)
	h.Flush(gv)

	// second cycle: only 1 entry -- stale labels should be removed
	h.Observe(100, []string{"pid4"}, 5)
	h.Flush(gv)

	metrics, err := registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	if len(metrics) != 1 {
		t.Fatalf("expected 1 metric family, got %d", len(metrics))
	}

	// only the second cycle's entry should remain
	mf := metrics[0]
	if len(mf.GetMetric()) != 1 {
		t.Fatalf("expected 1 metric sample after second flush, got %d", len(mf.GetMetric()))
	}

	if mf.GetMetric()[0].Gauge.GetValue() != 100 {
		t.Fatalf("expected value 100, got %f", mf.GetMetric()[0].Gauge.GetValue())
	}
}

func TestTopNMaxLargeDataset(t *testing.T) {
	h := newTopNHeap(TopNMax)
	topN := 5

	// insert 100 values
	for i := 0; i < 100; i++ {
		h.Observe(float64(i), []string{fmt.Sprintf("item_%d", i)}, topN)
	}

	if h.Len() != topN {
		t.Fatalf("expected %d entries, got %d", topN, h.Len())
	}

	// all entries should be >= 95 (top 5 of 0..99)
	for _, e := range h.entries {
		if e.value < 95 {
			t.Fatalf("unexpected value %f in top-5 (should be >= 95)", e.value)
		}
	}
}

func TestTopNMinLargeDataset(t *testing.T) {
	h := newTopNHeap(TopNMin)
	topN := 5

	// insert 100 values
	for i := 0; i < 100; i++ {
		h.Observe(float64(i), []string{fmt.Sprintf("item_%d", i)}, topN)
	}

	if h.Len() != topN {
		t.Fatalf("expected %d entries, got %d", topN, h.Len())
	}

	// all entries should be <= 4 (bottom 5 of 0..99)
	for _, e := range h.entries {
		if e.value > 4 {
			t.Fatalf("unexpected value %f in bottom-5 (should be <= 4)", e.value)
		}
	}
}

func TestTopNHeapEqualValues(t *testing.T) {
	h := newTopNHeap(TopNMax)
	topN := 3

	// insert entries with the same value but different labels
	for i := 0; i < 5; i++ {
		h.Observe(42, []string{fmt.Sprintf("item_%d", i)}, topN)
	}

	if h.Len() != topN {
		t.Fatalf("expected %d entries, got %d", topN, h.Len())
	}

	// all values should be 42
	for _, e := range h.entries {
		if e.value != 42 {
			t.Fatalf("unexpected value %f, expected 42", e.value)
		}
	}
}

func TestTopNHeapReusableAfterReset(t *testing.T) {
	h := newTopNHeap(TopNMax)
	topN := 3

	// first round
	h.Observe(10, []string{"a"}, topN)
	h.Observe(20, []string{"b"}, topN)
	h.Observe(30, []string{"c"}, topN)
	h.Reset()

	// second round with different values
	h.Observe(100, []string{"x"}, topN)
	h.Observe(200, []string{"y"}, topN)

	if h.Len() != 2 {
		t.Fatalf("expected 2 entries after reuse, got %d", h.Len())
	}

	values := make(map[float64]bool)
	for _, e := range h.entries {
		values[e.value] = true
	}
	if values[100] == false || values[200] == false {
		t.Fatal("expected values 100 and 200 after reuse")
	}
}

func TestTopNOrderConstants(t *testing.T) {
	if TopNMax != 0 {
		t.Fatalf("expected TopNMax=0, got %d", TopNMax)
	}
	if TopNMin != 1 {
		t.Fatalf("expected TopNMin=1, got %d", TopNMin)
	}
}

func TestTopNHeapPushPop(t *testing.T) {
	// test the raw heap.Interface Push/Pop methods
	h := newTopNHeap(TopNMax)

	heap.Push(h, topNEntry{value: 30, labels: []string{"c"}})
	heap.Push(h, topNEntry{value: 10, labels: []string{"a"}})
	heap.Push(h, topNEntry{value: 20, labels: []string{"b"}})

	if h.Len() != 3 {
		t.Fatalf("expected 3 entries, got %d", h.Len())
	}

	// min-heap: Pop should return smallest first
	first := heap.Pop(h).(topNEntry)
	if first.value != 10 {
		t.Fatalf("expected first pop value 10, got %f", first.value)
	}

	second := heap.Pop(h).(topNEntry)
	if second.value != 20 {
		t.Fatalf("expected second pop value 20, got %f", second.value)
	}

	third := heap.Pop(h).(topNEntry)
	if third.value != 30 {
		t.Fatalf("expected third pop value 30, got %f", third.value)
	}
}

func TestTopNMinHeapPushPop(t *testing.T) {
	h := newTopNHeap(TopNMin)

	heap.Push(h, topNEntry{value: 30, labels: []string{"c"}})
	heap.Push(h, topNEntry{value: 10, labels: []string{"a"}})
	heap.Push(h, topNEntry{value: 20, labels: []string{"b"}})

	// max-heap: Pop should return largest first
	first := heap.Pop(h).(topNEntry)
	if first.value != 30 {
		t.Fatalf("expected first pop value 30, got %f", first.value)
	}

	second := heap.Pop(h).(topNEntry)
	if second.value != 20 {
		t.Fatalf("expected second pop value 20, got %f", second.value)
	}

	third := heap.Pop(h).(topNEntry)
	if third.value != 10 {
		t.Fatalf("expected third pop value 10, got %f", third.value)
	}
}
