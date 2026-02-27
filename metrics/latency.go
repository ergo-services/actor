//go:build latency

package metrics

import (
	"container/heap"
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
	heap     *topNHeap
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
	lm.heap = &topNHeap{}
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

	entry := topNEntry{
		seconds:     seconds,
		pid:         info.PID.String(),
		name:        string(info.Name),
		application: string(info.Application),
		behavior:    info.Behavior,
	}

	if lm.heap.Len() < topN {
		heap.Push(lm.heap, entry)
	} else if seconds > (*lm.heap)[0].seconds {
		(*lm.heap)[0] = entry
		heap.Fix(lm.heap, 0)
	}
}

func (lm *latencyMetrics) flush() {
	gaugeFromMap(lm.cm, "ergo_mailbox_latency_max_seconds").Set(lm.maxLat)
	gaugeFromMap(lm.cm, "ergo_mailbox_latency_processes").Set(lm.stressed)

	distribution := gaugeVecFromMap(lm.cm, "ergo_mailbox_latency_distribution")
	for i, label := range latencyRangeLabels {
		distribution.WithLabelValues(label).Set(lm.buckets[i])
	}

	topLatency := gaugeVecFromMap(lm.cm, "ergo_mailbox_latency_top_seconds")
	topLatency.Reset()
	for _, e := range *lm.heap {
		topLatency.WithLabelValues(
			e.pid,
			e.name,
			e.application,
			e.behavior,
		).Set(e.seconds)
	}
}

//
// min-heap for top-N selection
//

type topNEntry struct {
	seconds     float64
	pid         string
	name        string
	application string
	behavior    string
}

type topNHeap []topNEntry

func (h topNHeap) Len() int            { return len(h) }
func (h topNHeap) Less(i, j int) bool  { return h[i].seconds < h[j].seconds }
func (h topNHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *topNHeap) Push(x any)         { *h = append(*h, x.(topNEntry)) }
func (h *topNHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// compile-time check
var _ heap.Interface = (*topNHeap)(nil)
