package metrics

import (
	"sync"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

type wakeupsMetrics struct {
	cm *sync.Map

	// per-cycle accumulators
	heapWakeups *topNHeap
	heapDrains  *topNHeap
}

func (wm *wakeupsMetrics) init(cm *sync.Map, registry *prometheus.Registry, nodeLabels prometheus.Labels) {
	wm.cm = cm

	registerInternalGaugeVec(cm, registry,
		"ergo_process_wakeups_top",
		"Top-N processes by cumulative wakeup count (Sleep to Running transitions)",
		nodeLabels, []string{"pid", "name", "application", "behavior"})

	registerInternalGaugeVec(cm, registry,
		"ergo_process_drains_top",
		"Top-N processes by drain ratio (MessagesIn / Wakeups)",
		nodeLabels, []string{"pid", "name", "application", "behavior"})
}

func (wm *wakeupsMetrics) begin() {
	wm.heapWakeups = newTopNHeap(TopNMax)
	wm.heapDrains = newTopNHeap(TopNMax)
}

func (wm *wakeupsMetrics) observe(info gen.ProcessShortInfo, topN int) {
	if info.Wakeups == 0 {
		return
	}

	labels := []string{
		info.PID.String(),
		string(info.Name),
		string(info.Application),
		info.Behavior,
	}

	// top-N by wakeups
	wm.heapWakeups.Observe(float64(info.Wakeups), labels, topN)

	// top-N by drain ratio (skip processes with no messages)
	if info.MessagesIn == 0 {
		return
	}
	drain := float64(info.MessagesIn) / float64(info.Wakeups)
	wm.heapDrains.Observe(drain, labels, topN)
}

func (wm *wakeupsMetrics) flush() {
	wm.heapWakeups.Flush(gaugeVecFromMap(wm.cm, "ergo_process_wakeups_top"))
	wm.heapDrains.Flush(gaugeVecFromMap(wm.cm, "ergo_process_drains_top"))
}
