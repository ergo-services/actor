package metrics

import (
	"container/heap"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

var utilizationBoundaries = []float64{
	0.01, 0.05, 0.10, 0.25, 0.50, 0.75, 0.90,
}

var utilizationRangeLabels = []string{
	"1%", "5%", "10%", "25%", "50%", "75%", "90%", "90%+",
}

type utilizationMetrics struct {
	distribution *prometheus.GaugeVec
	maxUtil      prometheus.Gauge
	topUtil      *prometheus.GaugeVec

	// per-cycle accumulators
	max     float64
	buckets []float64
	heap    *utilHeap
}

func (um *utilizationMetrics) init(registry *prometheus.Registry, nodeLabels prometheus.Labels) {
	um.distribution = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "ergo_process_utilization_distribution",
			Help:        "Number of processes by utilization range (snapshot per collect cycle)",
			ConstLabels: nodeLabels,
		},
		[]string{"range"},
	)

	um.maxUtil = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "ergo_process_utilization_max",
		Help:        "Maximum process utilization (RunningTime/Uptime) on this node",
		ConstLabels: nodeLabels,
	})

	um.topUtil = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "ergo_process_utilization_top",
			Help:        "Top-N processes by utilization (RunningTime/Uptime)",
			ConstLabels: nodeLabels,
		},
		[]string{"pid", "name", "application", "behavior"},
	)

	registry.MustRegister(
		um.distribution,
		um.maxUtil,
		um.topUtil,
	)
}

func (um *utilizationMetrics) begin() {
	um.max = 0
	um.buckets = make([]float64, len(utilizationBoundaries)+1)
	um.heap = &utilHeap{}
}

func (um *utilizationMetrics) observe(info gen.ProcessShortInfo, topN int) {
	if info.Uptime <= 0 {
		return
	}

	utilization := float64(info.RunningTime) / (float64(info.Uptime) * 1e9)
	if utilization <= 0 {
		return
	}
	if utilization > 1.0 {
		utilization = 1.0
	}

	if utilization > um.max {
		um.max = utilization
	}

	// find bucket
	placed := false
	for i, boundary := range utilizationBoundaries {
		if utilization <= boundary {
			um.buckets[i]++
			placed = true
			break
		}
	}
	if placed == false {
		um.buckets[len(utilizationBoundaries)]++
	}

	entry := utilEntry{
		utilization: utilization,
		pid:         info.PID.String(),
		name:        string(info.Name),
		application: string(info.Application),
		behavior:    info.Behavior,
	}

	if um.heap.Len() < topN {
		heap.Push(um.heap, entry)
	} else if utilization > (*um.heap)[0].utilization {
		(*um.heap)[0] = entry
		heap.Fix(um.heap, 0)
	}
}

func (um *utilizationMetrics) flush() {
	um.maxUtil.Set(um.max)

	for i, label := range utilizationRangeLabels {
		um.distribution.WithLabelValues(label).Set(um.buckets[i])
	}

	um.topUtil.Reset()
	for _, e := range *um.heap {
		um.topUtil.WithLabelValues(
			e.pid,
			e.name,
			e.application,
			e.behavior,
		).Set(e.utilization)
	}
}

//
// min-heap for top-N by utilization
//

type utilEntry struct {
	utilization float64
	pid         string
	name        string
	application string
	behavior    string
}

type utilHeap []utilEntry

func (h utilHeap) Len() int            { return len(h) }
func (h utilHeap) Less(i, j int) bool  { return h[i].utilization < h[j].utilization }
func (h utilHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *utilHeap) Push(x any)         { *h = append(*h, x.(utilEntry)) }
func (h *utilHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// compile-time check
var _ heap.Interface = (*utilHeap)(nil)
