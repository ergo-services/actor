package metrics

import (
	"sync"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

type livenessMetrics struct {
	cm *sync.Map

	heap *topNHeap
}

func (lm *livenessMetrics) init(cm *sync.Map, registry *prometheus.Registry, nodeLabels prometheus.Labels) {
	lm.cm = cm

	registerInternalGaugeVec(cm, registry,
		"ergo_process_liveness_bottom",
		"Bottom-N processes by liveness score (lowest = most likely stuck in blocking call)",
		nodeLabels, []string{"pid", "name", "application", "behavior"})
}

func (lm *livenessMetrics) begin() {
	lm.heap = newTopNHeap(TopNMin)
}

func (lm *livenessMetrics) observe(info gen.ProcessShortInfo, topN int) {
	if info.Uptime < 60 {
		return
	}
	if info.MessagesIn == 0 {
		return
	}
	if info.MailboxLatency <= 0 {
		return
	}
	if info.State == gen.ProcessStateZombee {
		return
	}

	// liveness = RunningTime / (Uptime * MailboxLatency)
	// high = healthy (actively processing), near zero = handler blocked
	liveness := float64(info.RunningTime) / (float64(info.Uptime) * float64(info.MailboxLatency))

	labels := []string{
		info.PID.String(),
		string(info.Name),
		string(info.Application),
		info.Behavior,
	}

	lm.heap.Observe(liveness, labels, topN)
}

func (lm *livenessMetrics) flush() {
	lm.heap.Flush(gaugeVecFromMap(lm.cm, "ergo_process_liveness_bottom"))
}
