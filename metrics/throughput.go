package metrics

import (
	"container/heap"
	"sync"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

type throughputMetrics struct {
	cm *sync.Map

	// per-cycle accumulators
	heapIn  *throughputHeap
	heapOut *throughputHeap
}

func (tm *throughputMetrics) init(cm *sync.Map, registry *prometheus.Registry, nodeLabels prometheus.Labels) {
	tm.cm = cm

	registerInternalGaugeVec(cm, registry,
		"ergo_process_messages_in_top",
		"Top-N processes by total messages received",
		nodeLabels, []string{"pid", "name", "application", "behavior"})

	registerInternalGaugeVec(cm, registry,
		"ergo_process_messages_out_top",
		"Top-N processes by total messages sent",
		nodeLabels, []string{"pid", "name", "application", "behavior"})
}

func (tm *throughputMetrics) begin() {
	tm.heapIn = &throughputHeap{}
	tm.heapOut = &throughputHeap{}
}

func (tm *throughputMetrics) observe(info gen.ProcessShortInfo, topN int) {
	pid := info.PID.String()
	name := string(info.Name)
	application := string(info.Application)
	behavior := info.Behavior

	if info.MessagesIn > 0 {
		entry := throughputEntry{
			value:       info.MessagesIn,
			pid:         pid,
			name:        name,
			application: application,
			behavior:    behavior,
		}
		if tm.heapIn.Len() < topN {
			heap.Push(tm.heapIn, entry)
		} else if info.MessagesIn > (*tm.heapIn)[0].value {
			(*tm.heapIn)[0] = entry
			heap.Fix(tm.heapIn, 0)
		}
	}

	if info.MessagesOut > 0 {
		entry := throughputEntry{
			value:       info.MessagesOut,
			pid:         pid,
			name:        name,
			application: application,
			behavior:    behavior,
		}
		if tm.heapOut.Len() < topN {
			heap.Push(tm.heapOut, entry)
		} else if info.MessagesOut > (*tm.heapOut)[0].value {
			(*tm.heapOut)[0] = entry
			heap.Fix(tm.heapOut, 0)
		}
	}
}

func (tm *throughputMetrics) flush() {
	topIn := gaugeVecFromMap(tm.cm, "ergo_process_messages_in_top")
	topIn.Reset()
	for _, e := range *tm.heapIn {
		topIn.WithLabelValues(
			e.pid,
			e.name,
			e.application,
			e.behavior,
		).Set(float64(e.value))
	}

	topOut := gaugeVecFromMap(tm.cm, "ergo_process_messages_out_top")
	topOut.Reset()
	for _, e := range *tm.heapOut {
		topOut.WithLabelValues(
			e.pid,
			e.name,
			e.application,
			e.behavior,
		).Set(float64(e.value))
	}
}

//
// min-heap for top-N by throughput
//

type throughputEntry struct {
	value       uint64
	pid         string
	name        string
	application string
	behavior    string
}

type throughputHeap []throughputEntry

func (h throughputHeap) Len() int            { return len(h) }
func (h throughputHeap) Less(i, j int) bool  { return h[i].value < h[j].value }
func (h throughputHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *throughputHeap) Push(x any)         { *h = append(*h, x.(throughputEntry)) }
func (h *throughputHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// compile-time check
var _ heap.Interface = (*throughputHeap)(nil)
