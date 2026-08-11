package metrics

import (
	"ergo.services/ergo/gen"
	"github.com/prometheus/client_golang/prometheus"
)

// MetricType represents the type of Prometheus metric
type MetricType int

const (
	MetricGauge     MetricType = 1
	MetricCounter   MetricType = 2
	MetricHistogram MetricType = 3
	MetricTopN      MetricType = 4
)

// RegisterRequest is a sync request (via Call) to register a custom metric.
// Returns RegisterResponse.
type RegisterRequest struct {
	Name    string
	Help    string
	Type    MetricType
	Labels  []string  // label names (for Vec metrics); empty for plain metrics
	Buckets []float64 // histogram only; nil = prometheus.DefBuckets
}

// RegisterResponse is the sync response for RegisterRequest.
// Empty Error means success.
type RegisterResponse struct {
	Error string
}

// MessageUnregister removes a previously registered custom metric.
type MessageUnregister struct {
	Name string
}

// MessageGaugeSet sets the value of a registered gauge metric.
type MessageGaugeSet struct {
	Name   string
	Value  float64
	Labels []string // label values (for Vec metrics); empty for plain metrics
}

// MessageGaugeAdd adds the value to a registered gauge metric.
type MessageGaugeAdd struct {
	Name   string
	Value  float64
	Labels []string
}

// MessageCounterAdd adds the value to a registered counter metric.
type MessageCounterAdd struct {
	Name   string
	Value  float64
	Labels []string
}

// MessageHistogramObserve observes a value on a registered histogram metric.
type MessageHistogramObserve struct {
	Name   string
	Value  float64
	Labels []string
}

// RegisterTopNRequest is a sync request (via Call) to register a custom top-N metric.
// The metric is managed by a dedicated actor spawned under a SOFO supervisor.
// Returns RegisterResponse.
type RegisterTopNRequest struct {
	Name   string
	Help   string
	TopN   int
	Order  TopNOrder
	Labels []string
}

// MessageTopNObserve sends a value observation to a top-N metric actor.
type MessageTopNObserve struct {
	Value  float64
	Labels []string
}

// registeredMetric holds the state of a single registered custom metric.
type registeredMetric struct {
	name       string
	metricType MetricType
	labelNames []string
	collector  prometheus.Collector

	// typed references for updates (only one pair is set per metric)
	gauge        prometheus.Gauge
	gaugeVec     *prometheus.GaugeVec
	counter      prometheus.Counter
	counterVec   *prometheus.CounterVec
	histogram    prometheus.Observer
	histogramVec *prometheus.HistogramVec

	registeredBy gen.PID
	internal     bool // base ergo metrics, not removable by external actors
}

// NetworkTypes returns the wire types this actor receives from the processes it collects
// for.
//
// Registering them is the caller's job, not the library's, and it must happen before the
// node carries any traffic. Type registration is node-scoped, so it cannot be done from a
// package init(); doing it in the actor's own Init would be too late for a node whose
// metrics process starts after a connection is already established.
//
// Declare them on the application that hosts the actor, which is processed during
// ApplicationLoad before any of its processes are spawned:
//
//	gen.ApplicationSpec{
//	    Network: gen.ApplicationNetwork{
//	        RegisterTypes:  metrics.NetworkTypes(),
//	        RegisterErrors: metrics.ErrorTypes(),
//	    },
//	}
//
// or register them on the node directly before it starts serving:
//
//	node.Network().RegisterTypes(metrics.NetworkTypes())
//
// Init refuses to start if the node does not know them: an observation that cannot be
// decoded is a metric that silently stays flat, which is worse than not starting.
func NetworkTypes() []any {
	return []any{
		MetricType(0),
		TopNOrder(0),
		RegisterRequest{},
		RegisterResponse{},
		RegisterTopNRequest{},
		MessageUnregister{},
		MessageGaugeSet{},
		MessageGaugeAdd{},
		MessageCounterAdd{},
		MessageHistogramObserve{},
		MessageTopNObserve{},
	}
}

// ErrorTypes returns the sentinel errors this actor sends over the wire. There are none
// today - failures ride as strings on the responses - so this exists to keep consumer
// setup uniform and keep working if that changes.
func ErrorTypes() []error {
	return nil
}
