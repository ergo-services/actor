package metrics

import (
	"container/heap"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

type wakeupsMetrics struct {
	topWakeups *prometheus.GaugeVec
	topDrains  *prometheus.GaugeVec

	// per-cycle accumulators
	heapWakeups *wakeupsHeap
	heapDrains  *drainsHeap
}

func (wm *wakeupsMetrics) init(registry *prometheus.Registry, nodeLabels prometheus.Labels) {
	wm.topWakeups = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "ergo_process_wakeups_top",
			Help:        "Top-N processes by cumulative wakeup count (Sleep to Running transitions)",
			ConstLabels: nodeLabels,
		},
		[]string{"pid", "name", "application", "behavior"},
	)

	wm.topDrains = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "ergo_process_drains_top",
			Help:        "Top-N processes by drain ratio (MessagesIn / Wakeups)",
			ConstLabels: nodeLabels,
		},
		[]string{"pid", "name", "application", "behavior"},
	)

	registry.MustRegister(
		wm.topWakeups,
		wm.topDrains,
	)
}

func (wm *wakeupsMetrics) begin() {
	wm.heapWakeups = &wakeupsHeap{}
	wm.heapDrains = &drainsHeap{}
}

func (wm *wakeupsMetrics) observe(info gen.ProcessShortInfo, topN int) {
	if info.Wakeups == 0 {
		return
	}

	pid := info.PID.String()
	name := string(info.Name)
	application := string(info.Application)
	behavior := info.Behavior

	// top-N by wakeups
	wEntry := wakeupsEntry{
		value:       info.Wakeups,
		pid:         pid,
		name:        name,
		application: application,
		behavior:    behavior,
	}
	if wm.heapWakeups.Len() < topN {
		heap.Push(wm.heapWakeups, wEntry)
	} else if info.Wakeups > (*wm.heapWakeups)[0].value {
		(*wm.heapWakeups)[0] = wEntry
		heap.Fix(wm.heapWakeups, 0)
	}

	// top-N by drain ratio (skip processes with no messages)
	if info.MessagesIn == 0 {
		return
	}
	drain := float64(info.MessagesIn) / float64(info.Wakeups)
	dEntry := drainsEntry{
		value:       drain,
		pid:         pid,
		name:        name,
		application: application,
		behavior:    behavior,
	}
	if wm.heapDrains.Len() < topN {
		heap.Push(wm.heapDrains, dEntry)
	} else if drain > (*wm.heapDrains)[0].value {
		(*wm.heapDrains)[0] = dEntry
		heap.Fix(wm.heapDrains, 0)
	}
}

func (wm *wakeupsMetrics) flush() {
	wm.topWakeups.Reset()
	for _, e := range *wm.heapWakeups {
		wm.topWakeups.WithLabelValues(
			e.pid,
			e.name,
			e.application,
			e.behavior,
		).Set(float64(e.value))
	}

	wm.topDrains.Reset()
	for _, e := range *wm.heapDrains {
		wm.topDrains.WithLabelValues(
			e.pid,
			e.name,
			e.application,
			e.behavior,
		).Set(e.value)
	}
}

//
// min-heap for top-N by wakeups
//

type wakeupsEntry struct {
	value       uint64
	pid         string
	name        string
	application string
	behavior    string
}

type wakeupsHeap []wakeupsEntry

func (h wakeupsHeap) Len() int            { return len(h) }
func (h wakeupsHeap) Less(i, j int) bool  { return h[i].value < h[j].value }
func (h wakeupsHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *wakeupsHeap) Push(x any)         { *h = append(*h, x.(wakeupsEntry)) }
func (h *wakeupsHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

var _ heap.Interface = (*wakeupsHeap)(nil)

//
// min-heap for top-N by drain ratio
//

type drainsEntry struct {
	value       float64
	pid         string
	name        string
	application string
	behavior    string
}

type drainsHeap []drainsEntry

func (h drainsHeap) Len() int            { return len(h) }
func (h drainsHeap) Less(i, j int) bool  { return h[i].value < h[j].value }
func (h drainsHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *drainsHeap) Push(x any)         { *h = append(*h, x.(drainsEntry)) }
func (h *drainsHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

var _ heap.Interface = (*drainsHeap)(nil)
