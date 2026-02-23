package metrics

import (
	"container/heap"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

var eventSubscriberBoundaries = []int64{0, 1, 5, 10, 50, 100, 500, 1000}

var eventSubscriberRangeLabels = []string{
	"0", "1", "2-5", "6-10", "11-50", "51-100", "101-500", "501-1K", "1K+",
}

type eventMetrics struct {
	distribution   *prometheus.GaugeVec
	maxSubs        prometheus.Gauge
	waste          *prometheus.GaugeVec
	topSubs        *prometheus.GaugeVec
	topPublished   *prometheus.GaugeVec
	topLocalSent   *prometheus.GaugeVec
	topRemoteSent  *prometheus.GaugeVec

	// per-cycle accumulators
	max            int64
	buckets        []float64
	wasteCounts    [3]float64 // idle, no_subscribers, no_publishing
	heapSubs       *eventHeap
	heapPublished  *eventHeap
	heapLocalSent  *eventHeap
	heapRemoteSent *eventHeap
}

func (em *eventMetrics) init(registry *prometheus.Registry, nodeLabels prometheus.Labels) {
	em.distribution = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "ergo_event_subscribers_distribution",
			Help:        "Number of events by subscriber count range (snapshot per collect cycle)",
			ConstLabels: nodeLabels,
		},
		[]string{"range"},
	)

	em.maxSubs = prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "ergo_event_subscribers_max",
		Help:        "Maximum subscriber count across all events on this node",
		ConstLabels: nodeLabels,
	})

	em.waste = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "ergo_event_waste",
			Help:        "Number of events with wasteful usage patterns (snapshot per collect cycle)",
			ConstLabels: nodeLabels,
		},
		[]string{"reason"},
	)

	em.topSubs = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "ergo_event_subscribers_top",
			Help:        "Top-N events by subscriber count",
			ConstLabels: nodeLabels,
		},
		[]string{"event", "producer"},
	)

	em.topPublished = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "ergo_event_published_top",
			Help:        "Top-N events by messages published",
			ConstLabels: nodeLabels,
		},
		[]string{"event", "producer"},
	)

	em.topLocalSent = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "ergo_event_local_sent_top",
			Help:        "Top-N events by messages delivered to local subscribers",
			ConstLabels: nodeLabels,
		},
		[]string{"event", "producer"},
	)

	em.topRemoteSent = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "ergo_event_remote_sent_top",
			Help:        "Top-N events by messages sent to remote nodes",
			ConstLabels: nodeLabels,
		},
		[]string{"event", "producer"},
	)

	registry.MustRegister(
		em.distribution,
		em.maxSubs,
		em.waste,
		em.topSubs,
		em.topPublished,
		em.topLocalSent,
		em.topRemoteSent,
	)
}

func (em *eventMetrics) begin() {
	em.max = 0
	em.buckets = make([]float64, len(eventSubscriberBoundaries)+1)
	em.wasteCounts = [3]float64{}
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

	// find bucket for subscriber count
	placed := false
	for i, boundary := range eventSubscriberBoundaries {
		if subs <= boundary {
			em.buckets[i]++
			placed = true
			break
		}
	}
	if placed == false {
		em.buckets[len(eventSubscriberBoundaries)]++
	}

	// classify waste patterns
	if info.MessagesPublished == 0 && subs == 0 {
		em.wasteCounts[0]++ // idle
	} else if info.MessagesPublished > 0 && subs == 0 {
		em.wasteCounts[1]++ // no_subscribers
	} else if subs > 0 && info.MessagesPublished == 0 {
		em.wasteCounts[2]++ // no_publishing
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
	em.maxSubs.Set(float64(em.max))

	for i, label := range eventSubscriberRangeLabels {
		em.distribution.WithLabelValues(label).Set(em.buckets[i])
	}

	em.waste.WithLabelValues("idle").Set(em.wasteCounts[0])
	em.waste.WithLabelValues("no_subscribers").Set(em.wasteCounts[1])
	em.waste.WithLabelValues("no_publishing").Set(em.wasteCounts[2])

	eventGaugeFlush(em.topSubs, em.heapSubs)
	eventGaugeFlush(em.topPublished, em.heapPublished)
	eventGaugeFlush(em.topLocalSent, em.heapLocalSent)
	eventGaugeFlush(em.topRemoteSent, em.heapRemoteSent)
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
