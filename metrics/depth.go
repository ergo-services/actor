package metrics

import (
	"fmt"
	"sync"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

var depthBoundaries = []uint64{
	1, 5, 10, 50, 100, 500, 1000, 5000, 10000,
}

var depthRangeLabels []string

func init() {
	depthRangeLabels = make([]string, len(depthBoundaries)+1)
	for i, b := range depthBoundaries {
		if b >= 1000 {
			depthRangeLabels[i] = fmt.Sprintf("%dK", b/1000)
		} else {
			depthRangeLabels[i] = fmt.Sprintf("%d", b)
		}
	}
	depthRangeLabels[len(depthBoundaries)] = "10K+"
}

type depthMetrics struct {
	cm *sync.Map

	// per-cycle accumulators
	max     uint64
	buckets []float64
	heap    *topNHeap
}

func (dm *depthMetrics) init(cm *sync.Map, registry *prometheus.Registry, nodeLabels prometheus.Labels) {
	dm.cm = cm

	registerInternalGaugeVec(cm, registry,
		"ergo_mailbox_depth_distribution",
		"Number of processes by mailbox queue depth range (snapshot per collect cycle)",
		nodeLabels, []string{"range"})

	registerInternalGauge(cm, registry,
		"ergo_mailbox_depth_max",
		"Maximum mailbox queue depth across all processes on this node",
		nodeLabels)

	registerInternalGaugeVec(cm, registry,
		"ergo_mailbox_depth_top",
		"Top-N processes by mailbox queue depth",
		nodeLabels, []string{"pid", "name", "application", "behavior"})
}

func (dm *depthMetrics) begin() {
	dm.max = 0
	dm.buckets = make([]float64, len(depthBoundaries)+1)
	dm.heap = newTopNHeap(TopNMax)
}

func (dm *depthMetrics) observe(info gen.ProcessShortInfo, topN int) {
	if info.MessagesMailbox == 0 {
		return
	}

	depth := info.MessagesMailbox
	if depth > dm.max {
		dm.max = depth
	}

	// find bucket
	placed := false
	for i, boundary := range depthBoundaries {
		if depth <= boundary {
			dm.buckets[i]++
			placed = true
			break
		}
	}
	if placed == false {
		dm.buckets[len(depthBoundaries)]++
	}

	dm.heap.Observe(float64(depth), []string{
		info.PID.String(),
		string(info.Name),
		string(info.Application),
		info.Behavior,
	}, topN)
}

func (dm *depthMetrics) flush() {
	gaugeFromMap(dm.cm, "ergo_mailbox_depth_max").Set(float64(dm.max))

	distribution := gaugeVecFromMap(dm.cm, "ergo_mailbox_depth_distribution")
	for i, label := range depthRangeLabels {
		distribution.WithLabelValues(label).Set(dm.buckets[i])
	}

	dm.heap.Flush(gaugeVecFromMap(dm.cm, "ergo_mailbox_depth_top"))
}
