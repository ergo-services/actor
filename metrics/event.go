package metrics

import (
	"sync"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

type eventMetrics struct {
	cm *sync.Map

	// per-cycle accumulators
	max            int64
	utilCounts     [5]float64 // active, on_demand, idle, no_subscribers, no_publishing
	heapSubs       *topNHeap
	heapPublished  *topNHeap
	heapLocalSent  *topNHeap
	heapRemoteSent *topNHeap
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
	em.heapSubs = newTopNHeap(TopNMax)
	em.heapPublished = newTopNHeap(TopNMax)
	em.heapLocalSent = newTopNHeap(TopNMax)
	em.heapRemoteSent = newTopNHeap(TopNMax)
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

	labels := []string{string(info.Event.Name), info.Producer.String()}

	// top-N by subscribers
	em.heapSubs.Observe(float64(subs), labels, topN)

	// top-N by messages published
	if info.MessagesPublished > 0 {
		em.heapPublished.Observe(float64(info.MessagesPublished), labels, topN)
	}

	// top-N by local deliveries
	if info.MessagesLocalSent > 0 {
		em.heapLocalSent.Observe(float64(info.MessagesLocalSent), labels, topN)
	}

	// top-N by remote sent
	if info.MessagesRemoteSent > 0 {
		em.heapRemoteSent.Observe(float64(info.MessagesRemoteSent), labels, topN)
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

	em.heapSubs.Flush(gaugeVecFromMap(em.cm, "ergo_event_subscribers_top"))
	em.heapPublished.Flush(gaugeVecFromMap(em.cm, "ergo_event_published_top"))
	em.heapLocalSent.Flush(gaugeVecFromMap(em.cm, "ergo_event_local_sent_top"))
	em.heapRemoteSent.Flush(gaugeVecFromMap(em.cm, "ergo_event_remote_sent_top"))
}
