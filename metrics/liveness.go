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
	// only consider processes that have been alive long enough
	if info.Uptime < 60 {
		return
	}

	// only consider processes that received messages (not idle/internal)
	if info.MessagesIn == 0 {
		return
	}

	// only consider processes with latency data available
	if info.MailboxLatency < 0 {
		return
	}

	// liveness = Wakeups / (Uptime * max(MailboxLatency_sec, 1))
	latencySec := float64(info.MailboxLatency) / 1e9
	if latencySec < 1 {
		latencySec = 1
	}

	liveness := float64(info.Wakeups) / (float64(info.Uptime) * latencySec)

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
