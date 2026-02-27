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
