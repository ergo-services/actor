package metrics

import (
	"container/heap"
	"fmt"

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
	distribution *prometheus.GaugeVec
	maxDepth     prometheus.Gauge
	topDepth     *prometheus.GaugeVec

	// per-cycle accumulators
	max     uint64
	buckets []float64
	heap    *depthHeap
}

func (dm *depthMetrics) init(registry *prometheus.Registry, nodeLabels prometheus.Labels) {
	dm.distribution = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "ergo_mailbox_depth_distribution",
			Help:        "Number of processes by mailbox queue depth range (snapshot per collect cycle)",
			ConstLabels: nodeLabels,
		},
		[]string{"range"},
	)

	dm.maxDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "ergo_mailbox_depth_max",
		Help:        "Maximum mailbox queue depth across all processes on this node",
		ConstLabels: nodeLabels,
	})

	dm.topDepth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "ergo_mailbox_depth_top",
			Help:        "Top-N processes by mailbox queue depth",
			ConstLabels: nodeLabels,
		},
		[]string{"pid", "name", "application", "behavior"},
	)

	registry.MustRegister(
		dm.distribution,
		dm.maxDepth,
		dm.topDepth,
	)
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
	dm.maxDepth.Set(float64(dm.max))

	for i, label := range depthRangeLabels {
		dm.distribution.WithLabelValues(label).Set(dm.buckets[i])
	}

	dm.topDepth.Reset()
	for _, e := range *dm.heap {
		dm.topDepth.WithLabelValues(
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
