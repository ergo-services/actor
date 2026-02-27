package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Shared holds state shared across multiple metrics actor instances.
// Use with a single primary actor (Port/Mux set) for base metrics + HTTP,
// and a pool of workers (no Port/Mux) for high-throughput custom metrics.
type Shared struct {
	registry *prometheus.Registry
	custom   sync.Map // string -> *registeredMetric
}

// NewShared creates a new Shared instance with a fresh Prometheus registry.
func NewShared() *Shared {
	return &Shared{
		registry: prometheus.NewRegistry(),
	}
}

// Registry returns the prometheus registry
func (s *Shared) Registry() *prometheus.Registry {
	return s.registry
}
