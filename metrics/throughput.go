package metrics

import (
	"sync"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

type throughputMetrics struct {
	cm *sync.Map

	// per-cycle accumulators
	heapIn  *topNHeap
	heapOut *topNHeap
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
	tm.heapIn = newTopNHeap(TopNMax)
	tm.heapOut = newTopNHeap(TopNMax)
}

func (tm *throughputMetrics) observe(info gen.ProcessShortInfo, topN int) {
	labels := []string{
		info.PID.String(),
		string(info.Name),
		string(info.Application),
		info.Behavior,
	}

	if info.MessagesIn > 0 {
		tm.heapIn.Observe(float64(info.MessagesIn), labels, topN)
	}

	if info.MessagesOut > 0 {
		tm.heapOut.Observe(float64(info.MessagesOut), labels, topN)
	}
}

func (tm *throughputMetrics) flush() {
	tm.heapIn.Flush(gaugeVecFromMap(tm.cm, "ergo_process_messages_in_top"))
	tm.heapOut.Flush(gaugeVecFromMap(tm.cm, "ergo_process_messages_out_top"))
}
