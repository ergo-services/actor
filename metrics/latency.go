//go:build latency

package metrics

import (
	"container/heap"
	"fmt"

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
	distribution  *prometheus.GaugeVec
	maxLatency    prometheus.Gauge
	stressedCount prometheus.Gauge
	topLatency    *prometheus.GaugeVec
}

func (lm *latencyMetrics) init(registry *prometheus.Registry, nodeLabels prometheus.Labels) {
	lm.distribution = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "ergo_mailbox_latency_distribution",
			Help:        "Number of processes by mailbox latency range (snapshot per collect cycle)",
			ConstLabels: nodeLabels,
		},
		[]string{"range"},
	)

	lm.maxLatency = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "ergo_mailbox_latency_max_seconds",
		Help:        "Maximum mailbox latency across all processes on this node",
		ConstLabels: nodeLabels,
	})

	lm.stressedCount = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "ergo_mailbox_latency_processes",
		Help:        "Number of processes with non-empty mailbox (latency > 0)",
		ConstLabels: nodeLabels,
	})

	lm.topLatency = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "ergo_mailbox_latency_top_seconds",
			Help:        "Top-N processes by mailbox latency",
			ConstLabels: nodeLabels,
		},
		[]string{"pid", "name", "application", "behavior"},
	)

	registry.MustRegister(
		lm.distribution,
		lm.maxLatency,
		lm.stressedCount,
		lm.topLatency,
	)
}

func (lm *latencyMetrics) collect(node gen.Node, topN int) {
	var maxLat float64
	var stressed float64
	h := &topNHeap{}

	// bucket counters: one per boundary + one for >max boundary
	buckets := make([]float64, len(latencyBoundaries)+1)

	node.ProcessRangeShortInfo(func(info gen.ProcessShortInfo) bool {
		if info.MailboxLatency <= 0 {
			return true
		}

		seconds := float64(info.MailboxLatency) / 1e9
		stressed++

		if seconds > maxLat {
			maxLat = seconds
		}

		// find bucket for this latency
		placed := false
		for i, boundary := range latencyBoundaries {
			if seconds <= boundary {
				buckets[i]++
				placed = true
				break
			}
		}
		if placed == false {
			buckets[len(latencyBoundaries)]++ // >60s
		}

		entry := topNEntry{
			seconds:     seconds,
			pid:         info.PID.String(),
			name:        string(info.Name),
			application: string(info.Application),
			behavior:    info.Behavior,
		}

		if h.Len() < topN {
			heap.Push(h, entry)
		} else if seconds > (*h)[0].seconds {
			(*h)[0] = entry
			heap.Fix(h, 0)
		}

		return true
	})

	lm.maxLatency.Set(maxLat)
	lm.stressedCount.Set(stressed)

	// update distribution gauges
	for i, label := range latencyRangeLabels {
		lm.distribution.WithLabelValues(label).Set(buckets[i])
	}

	lm.topLatency.Reset()
	for _, e := range *h {
		lm.topLatency.WithLabelValues(
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
