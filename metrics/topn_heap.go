package metrics

import (
	"container/heap"

	"github.com/prometheus/client_golang/prometheus"
)

// TopNOrder controls sorting direction for top-N selection.
type TopNOrder int

const (
	// TopNMax selects the N largest values (uses a min-heap internally).
	TopNMax TopNOrder = 0
	// TopNMin selects the N smallest values (uses a max-heap internally).
	TopNMin TopNOrder = 1
)

type topNEntry struct {
	value  float64
	labels []string
}

type topNHeap struct {
	entries []topNEntry
	order   TopNOrder
}

func newTopNHeap(order TopNOrder) *topNHeap {
	return &topNHeap{order: order}
}

// Observe adds or replaces an entry in the heap.
// For TopNMax: keeps the N largest values (min-heap root is smallest, replaced when new > root).
// For TopNMin: keeps the N smallest values (max-heap root is largest, replaced when new < root).
func (h *topNHeap) Observe(value float64, labels []string, topN int) {
	if h.Len() < topN {
		heap.Push(h, topNEntry{value: value, labels: labels})
		return
	}
	if h.Len() == 0 {
		return
	}

	switch h.order {
	case TopNMax:
		if value > h.entries[0].value {
			h.entries[0] = topNEntry{value: value, labels: labels}
			heap.Fix(h, 0)
		}
	case TopNMin:
		if value < h.entries[0].value {
			h.entries[0] = topNEntry{value: value, labels: labels}
			heap.Fix(h, 0)
		}
	}
}

// Reset clears entries while keeping allocated capacity.
func (h *topNHeap) Reset() {
	h.entries = h.entries[:0]
}

// Flush writes all heap entries to the given GaugeVec, then resets the heap.
// The GaugeVec is Reset() first to remove stale label combinations.
func (h *topNHeap) Flush(g *prometheus.GaugeVec) {
	g.Reset()
	for _, e := range h.entries {
		g.WithLabelValues(e.labels...).Set(e.value)
	}
	h.Reset()
}

// heap.Interface implementation

func (h *topNHeap) Len() int { return len(h.entries) }

func (h *topNHeap) Less(i, j int) bool {
	switch h.order {
	case TopNMin:
		// max-heap: largest at root, so we can evict it when a smaller value arrives
		return h.entries[i].value > h.entries[j].value
	default:
		// min-heap: smallest at root, so we can evict it when a larger value arrives
		return h.entries[i].value < h.entries[j].value
	}
}

func (h *topNHeap) Swap(i, j int) { h.entries[i], h.entries[j] = h.entries[j], h.entries[i] }

func (h *topNHeap) Push(x any) {
	h.entries = append(h.entries, x.(topNEntry))
}

func (h *topNHeap) Pop() any {
	old := h.entries
	n := len(old)
	item := old[n-1]
	h.entries = old[:n-1]
	return item
}

// compile-time check
var _ heap.Interface = (*topNHeap)(nil)
