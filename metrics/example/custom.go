package main

import (
	"math/rand"
	"time"

	"ergo.services/actor/metrics"
	"ergo.services/ergo/gen"
	"github.com/prometheus/client_golang/prometheus"
)

func CustomFactory() gen.ProcessBehavior {
	return &MyMetrics{}
}

type MyMetrics struct {
	metrics.Actor

	// Custom metrics
	requestsTotal   prometheus.Counter
	requestDuration prometheus.Histogram
	activeUsers     prometheus.Gauge
}

func (m *MyMetrics) Init(args ...any) (metrics.Options, error) {
	// Initialize custom metrics
	m.requestsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "my_app_requests_total",
		Help: "Total number of requests processed",
	})

	m.requestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "my_app_request_duration_seconds",
		Help:    "Request duration in seconds",
		Buckets: prometheus.DefBuckets,
	})

	m.activeUsers = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "my_app_active_users",
		Help: "Number of currently active users",
	})

	// Register custom metrics with the registry
	m.Registry().MustRegister(
		m.requestsTotal,
		m.requestDuration,
		m.activeUsers,
	)

	m.Log().Info("Using custom metrics with user-defined metrics")

	// Return options
	return metrics.Options{
		Port:            9090,
		Host:            "localhost",
		CollectInterval: 5 * time.Second,
	}, nil
}

func (m *MyMetrics) CollectMetrics() error {
	// Simulate collecting custom metrics
	m.requestsTotal.Add(float64(rand.Intn(10)))
	m.requestDuration.Observe(rand.Float64() * 2)
	m.activeUsers.Set(float64(rand.Intn(100)))

	return nil
}
