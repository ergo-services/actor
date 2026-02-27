package metrics

import (
	"container/heap"
	"sync"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

type initTimeMetrics struct {
	cm *sync.Map

	// per-cycle accumulators
	max  uint64
	heap *initTimeHeap
}

func (im *initTimeMetrics) init(cm *sync.Map, registry *prometheus.Registry, nodeLabels prometheus.Labels) {
	im.cm = cm

	registerInternalGauge(cm, registry,
		"ergo_process_init_time_max_seconds",
		"Maximum ProcessInit duration across all processes on this node",
		nodeLabels)

	registerInternalGaugeVec(cm, registry,
		"ergo_process_init_time_top_seconds",
		"Top-N processes by ProcessInit duration",
		nodeLabels, []string{"pid", "name", "application", "behavior"})
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
	gaugeFromMap(im.cm, "ergo_process_init_time_max_seconds").Set(float64(im.max) / 1e9)

	topInitTime := gaugeVecFromMap(im.cm, "ergo_process_init_time_top_seconds")
	topInitTime.Reset()
	for _, e := range *im.heap {
		topInitTime.WithLabelValues(
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
