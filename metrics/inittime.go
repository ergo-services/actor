package metrics

import (
	"sync"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

type initTimeMetrics struct {
	cm *sync.Map

	// per-cycle accumulators
	max  uint64
	heap *topNHeap
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
	im.heap = newTopNHeap(TopNMax)
}

func (im *initTimeMetrics) observe(info gen.ProcessShortInfo, topN int) {
	if info.InitTime == 0 {
		return
	}

	if info.InitTime > im.max {
		im.max = info.InitTime
	}

	im.heap.Observe(float64(info.InitTime)/1e9, []string{
		info.PID.String(),
		string(info.Name),
		string(info.Application),
		info.Behavior,
	}, topN)
}

func (im *initTimeMetrics) flush() {
	gaugeFromMap(im.cm, "ergo_process_init_time_max_seconds").Set(float64(im.max) / 1e9)
	im.heap.Flush(gaugeVecFromMap(im.cm, "ergo_process_init_time_top_seconds"))
}
