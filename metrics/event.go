package metrics

import (
	"container/heap"
	"sync"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

type eventMetrics struct {
	cm *sync.Map

	// per-cycle accumulators
	max            int64
	utilCounts     [5]float64 // active, on_demand, idle, no_subscribers, no_publishing
	heapSubs       *eventHeap
	heapPublished  *eventHeap
	heapLocalSent  *eventHeap
	heapRemoteSent *eventHeap
}

func (em *eventMetrics) init(cm *sync.Map, registry *prometheus.Registry, nodeLabels prometheus.Labels) {
	em.cm = cm

	registerInternalGauge(cm, registry,
		"ergo_event_subscribers_max",
		"Maximum subscriber count across all events on this node",
		nodeLabels)

	registerInternalGaugeVec(cm, registry,
		"ergo_event_utilization",
		"Number of events in each utilization state (snapshot per collect cycle)",
		nodeLabels, []string{"state"})

	registerInternalGaugeVec(cm, registry,
		"ergo_event_subscribers_top",
		"Top-N events by subscriber count",
		nodeLabels, []string{"event", "producer"})

	registerInternalGaugeVec(cm, registry,
		"ergo_event_published_top",
		"Top-N events by messages published",
		nodeLabels, []string{"event", "producer"})

	registerInternalGaugeVec(cm, registry,
		"ergo_event_local_sent_top",
		"Top-N events by messages delivered to local subscribers",
		nodeLabels, []string{"event", "producer"})

	registerInternalGaugeVec(cm, registry,
		"ergo_event_remote_sent_top",
		"Top-N events by messages sent to remote nodes",
		nodeLabels, []string{"event", "producer"})
}

func (em *eventMetrics) begin() {
	em.max = 0
	em.utilCounts = [5]float64{}
	em.heapSubs = &eventHeap{}
	em.heapPublished = &eventHeap{}
	em.heapLocalSent = &eventHeap{}
	em.heapRemoteSent = &eventHeap{}
}

func (em *eventMetrics) observe(info gen.EventInfo, topN int) {
	subs := info.Subscribers

	if subs > em.max {
		em.max = subs
	}

	// classify event utilization state
	if info.MessagesPublished > 0 && subs > 0 {
		em.utilCounts[0]++ // active
	} else if info.Notify {
		em.utilCounts[1]++ // on_demand
	} else if info.MessagesPublished == 0 && subs == 0 {
		em.utilCounts[2]++ // idle
	} else if info.MessagesPublished > 0 && subs == 0 {
		em.utilCounts[3]++ // no_subscribers
	} else if subs > 0 && info.MessagesPublished == 0 {
		em.utilCounts[4]++ // no_publishing
	}

	eventName := string(info.Event.Name)
	producer := info.Producer.String()

	// top-N by subscribers
	eventHeapPush(em.heapSubs, eventEntry{
		value:    subs,
		event:    eventName,
		producer: producer,
	}, topN)

	// top-N by messages published
	if info.MessagesPublished > 0 {
		eventHeapPush(em.heapPublished, eventEntry{
			value:    info.MessagesPublished,
			event:    eventName,
			producer: producer,
		}, topN)
	}

	// top-N by local deliveries
	if info.MessagesLocalSent > 0 {
		eventHeapPush(em.heapLocalSent, eventEntry{
			value:    info.MessagesLocalSent,
			event:    eventName,
			producer: producer,
		}, topN)
	}

	// top-N by remote sent
	if info.MessagesRemoteSent > 0 {
		eventHeapPush(em.heapRemoteSent, eventEntry{
			value:    info.MessagesRemoteSent,
			event:    eventName,
			producer: producer,
		}, topN)
	}
}

func (em *eventMetrics) flush() {
	gaugeFromMap(em.cm, "ergo_event_subscribers_max").Set(float64(em.max))

	utilization := gaugeVecFromMap(em.cm, "ergo_event_utilization")
	utilization.WithLabelValues("active").Set(em.utilCounts[0])
	utilization.WithLabelValues("on_demand").Set(em.utilCounts[1])
	utilization.WithLabelValues("idle").Set(em.utilCounts[2])
	utilization.WithLabelValues("no_subscribers").Set(em.utilCounts[3])
	utilization.WithLabelValues("no_publishing").Set(em.utilCounts[4])

	eventGaugeFlush(gaugeVecFromMap(em.cm, "ergo_event_subscribers_top"), em.heapSubs)
	eventGaugeFlush(gaugeVecFromMap(em.cm, "ergo_event_published_top"), em.heapPublished)
	eventGaugeFlush(gaugeVecFromMap(em.cm, "ergo_event_local_sent_top"), em.heapLocalSent)
	eventGaugeFlush(gaugeVecFromMap(em.cm, "ergo_event_remote_sent_top"), em.heapRemoteSent)
}

//
// helpers
//

func eventHeapPush(h *eventHeap, e eventEntry, topN int) {
	if h.Len() < topN {
		heap.Push(h, e)
	} else if e.value > (*h)[0].value {
		(*h)[0] = e
		heap.Fix(h, 0)
	}
}

func eventGaugeFlush(g *prometheus.GaugeVec, h *eventHeap) {
	g.Reset()
	for _, e := range *h {
		g.WithLabelValues(
			e.event,
			e.producer,
		).Set(float64(e.value))
	}
}

//
// min-heap for top-N event entries
//

type eventEntry struct {
	value    int64
	event    string
	producer string
}

type eventHeap []eventEntry

func (h eventHeap) Len() int            { return len(h) }
func (h eventHeap) Less(i, j int) bool  { return h[i].value < h[j].value }
func (h eventHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(x any)         { *h = append(*h, x.(eventEntry)) }
func (h *eventHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// compile-time check
var _ heap.Interface = (*eventHeap)(nil)
