package metrics

import (
	"container/heap"
	"fmt"
	"sync"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

var depthBoundaries = []uint64{
	1, 5, 10, 50, 100, 500, 1000, 5000, 10000,
}

var depthRangeLabels []string

func init() {
	depthRangeLabels = make([]string, len(depthBoundaries)+1)
	for i, b := range depthBoundaries {
		if b >= 1000 {
			depthRangeLabels[i] = fmt.Sprintf("%dK", b/1000)
		} else {
			depthRangeLabels[i] = fmt.Sprintf("%d", b)
		}
	}
	depthRangeLabels[len(depthBoundaries)] = "10K+"
}

type depthMetrics struct {
	cm *sync.Map

	// per-cycle accumulators
	max     uint64
	buckets []float64
	heap    *depthHeap
}

func (dm *depthMetrics) init(cm *sync.Map, registry *prometheus.Registry, nodeLabels prometheus.Labels) {
	dm.cm = cm

	registerInternalGaugeVec(cm, registry,
		"ergo_mailbox_depth_distribution",
		"Number of processes by mailbox queue depth range (snapshot per collect cycle)",
		nodeLabels, []string{"range"})

	registerInternalGauge(cm, registry,
		"ergo_mailbox_depth_max",
		"Maximum mailbox queue depth across all processes on this node",
		nodeLabels)

	registerInternalGaugeVec(cm, registry,
		"ergo_mailbox_depth_top",
		"Top-N processes by mailbox queue depth",
		nodeLabels, []string{"pid", "name", "application", "behavior"})
}

func (dm *depthMetrics) begin() {
	dm.max = 0
	dm.buckets = make([]float64, len(depthBoundaries)+1)
	dm.heap = &depthHeap{}
}

func (dm *depthMetrics) observe(info gen.ProcessShortInfo, topN int) {
	if info.MessagesMailbox == 0 {
		return
	}

	depth := info.MessagesMailbox
	if depth > dm.max {
		dm.max = depth
	}

	// find bucket
	placed := false
	for i, boundary := range depthBoundaries {
		if depth <= boundary {
			dm.buckets[i]++
			placed = true
			break
		}
	}
	if placed == false {
		dm.buckets[len(depthBoundaries)]++
	}

	entry := depthEntry{
		depth:       depth,
		pid:         info.PID.String(),
		name:        string(info.Name),
		application: string(info.Application),
		behavior:    info.Behavior,
	}

	if dm.heap.Len() < topN {
		heap.Push(dm.heap, entry)
	} else if depth > (*dm.heap)[0].depth {
		(*dm.heap)[0] = entry
		heap.Fix(dm.heap, 0)
	}
}

func (dm *depthMetrics) flush() {
	gaugeFromMap(dm.cm, "ergo_mailbox_depth_max").Set(float64(dm.max))

	distribution := gaugeVecFromMap(dm.cm, "ergo_mailbox_depth_distribution")
	for i, label := range depthRangeLabels {
		distribution.WithLabelValues(label).Set(dm.buckets[i])
	}

	topDepth := gaugeVecFromMap(dm.cm, "ergo_mailbox_depth_top")
	topDepth.Reset()
	for _, e := range *dm.heap {
		topDepth.WithLabelValues(
			e.pid,
			e.name,
			e.application,
			e.behavior,
		).Set(float64(e.depth))
	}
}

//
// min-heap for top-N by depth
//

type depthEntry struct {
	depth       uint64
	pid         string
	name        string
	application string
	behavior    string
}

type depthHeap []depthEntry

func (h depthHeap) Len() int            { return len(h) }
func (h depthHeap) Less(i, j int) bool  { return h[i].depth < h[j].depth }
func (h depthHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *depthHeap) Push(x any)         { *h = append(*h, x.(depthEntry)) }
func (h *depthHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// compile-time check
var _ heap.Interface = (*depthHeap)(nil)
