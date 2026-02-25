package metrics

import (
	"container/heap"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

type initTimeMetrics struct {
	maxInitTime prometheus.Gauge
	topInitTime *prometheus.GaugeVec

	// per-cycle accumulators
	max  uint64
	heap *initTimeHeap
}

func (im *initTimeMetrics) init(registry *prometheus.Registry, nodeLabels prometheus.Labels) {
	im.maxInitTime = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "ergo_process_init_time_max_seconds",
		Help:        "Maximum ProcessInit duration across all processes on this node",
		ConstLabels: nodeLabels,
	})

	im.topInitTime = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "ergo_process_init_time_top_seconds",
			Help:        "Top-N processes by ProcessInit duration",
			ConstLabels: nodeLabels,
		},
		[]string{"pid", "name", "application", "behavior"},
	)

	registry.MustRegister(
		im.maxInitTime,
		im.topInitTime,
	)
}

func (im *initTimeMetrics) begin() {
	im.max = 0
	im.heap = &initTimeHeap{}
}

func (im *initTimeMetrics) observe(info gen.ProcessShortInfo, topN int) {
	if info.InitTime == 0 {
		return
	}

	if info.InitTime > im.max {
		im.max = info.InitTime
	}

	entry := initTimeEntry{
		value:       info.InitTime,
		pid:         info.PID.String(),
		name:        string(info.Name),
		application: string(info.Application),
		behavior:    info.Behavior,
	}

	if im.heap.Len() < topN {
		heap.Push(im.heap, entry)
	} else if info.InitTime > (*im.heap)[0].value {
		(*im.heap)[0] = entry
		heap.Fix(im.heap, 0)
	}
}

func (im *initTimeMetrics) flush() {
	im.maxInitTime.Set(float64(im.max) / 1e9)

	im.topInitTime.Reset()
	for _, e := range *im.heap {
		im.topInitTime.WithLabelValues(
			e.pid,
			e.name,
			e.application,
			e.behavior,
		).Set(float64(e.value) / 1e9)
	}
}

//
// min-heap for top-N by init time
//

type initTimeEntry struct {
	value       uint64
	pid         string
	name        string
	application string
	behavior    string
}

type initTimeHeap []initTimeEntry

func (h initTimeHeap) Len() int            { return len(h) }
func (h initTimeHeap) Less(i, j int) bool  { return h[i].value < h[j].value }
func (h initTimeHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *initTimeHeap) Push(x any)         { *h = append(*h, x.(initTimeEntry)) }
func (h *initTimeHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// compile-time check
var _ heap.Interface = (*initTimeHeap)(nil)
