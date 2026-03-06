//go:build latency

package metrics

import (
	"fmt"
	"sync"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

// distribution boundaries in seconds
var latencyBoundaries = []float64{
	0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60,
}

// human-readable labels for each range
var latencyRangeLabels []string

func init() {
	latencyRangeLabels = make([]string, len(latencyBoundaries)+1)
	for i, b := range latencyBoundaries {
		if b < 1 {
			latencyRangeLabels[i] = fmt.Sprintf("%.0fms", b*1000)
		} else {
			latencyRangeLabels[i] = fmt.Sprintf("%.0fs", b)
		}
	}
	latencyRangeLabels[len(latencyBoundaries)] = "60s+"
}

type latencyMetrics struct {
	cm *sync.Map

	// per-cycle accumulators
	maxLat   float64
	stressed float64
	buckets  []float64
	heap *topNHeap
}

func (lm *latencyMetrics) init(cm *sync.Map, registry *prometheus.Registry, nodeLabels prometheus.Labels) {
	lm.cm = cm

	registerInternalGaugeVec(cm, registry,
		"ergo_mailbox_latency_distribution",
		"Number of processes by mailbox latency range (snapshot per collect cycle)",
		nodeLabels, []string{"range"})

	registerInternalGauge(cm, registry,
		"ergo_mailbox_latency_max_seconds",
		"Maximum mailbox latency across all processes on this node",
		nodeLabels)

	registerInternalGauge(cm, registry,
		"ergo_mailbox_latency_processes",
		"Number of processes with non-empty mailbox (latency > 0)",
		nodeLabels)

	registerInternalGaugeVec(cm, registry,
		"ergo_mailbox_latency_top_seconds",
		"Top-N processes by mailbox latency",
		nodeLabels, []string{"pid", "name", "application", "behavior"})
}

func (lm *latencyMetrics) begin() {
	lm.maxLat = 0
	lm.stressed = 0
	lm.buckets = make([]float64, len(latencyBoundaries)+1)
	lm.heap = newTopNHeap(TopNMax)
}

func (lm *latencyMetrics) observe(info gen.ProcessShortInfo, topN int) {
	if info.MailboxLatency <= 0 {
		return
	}

	seconds := float64(info.MailboxLatency) / 1e9
	lm.stressed++

	if seconds > lm.maxLat {
		lm.maxLat = seconds
	}

	// find bucket for this latency
	placed := false
	for i, boundary := range latencyBoundaries {
		if seconds <= boundary {
			lm.buckets[i]++
			placed = true
			break
		}
	}
	if placed == false {
		lm.buckets[len(latencyBoundaries)]++ // >60s
	}

	lm.heap.Observe(seconds, []string{
		info.PID.String(),
		string(info.Name),
		string(info.Application),
		info.Behavior,
	}, topN)
}

func (lm *latencyMetrics) flush() {
	gaugeFromMap(lm.cm, "ergo_mailbox_latency_max_seconds").Set(lm.maxLat)
	gaugeFromMap(lm.cm, "ergo_mailbox_latency_processes").Set(lm.stressed)

	distribution := gaugeVecFromMap(lm.cm, "ergo_mailbox_latency_distribution")
	for i, label := range latencyRangeLabels {
		distribution.WithLabelValues(label).Set(lm.buckets[i])
	}

	lm.heap.Flush(gaugeVecFromMap(lm.cm, "ergo_mailbox_latency_top_seconds"))
}
