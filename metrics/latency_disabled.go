//go:build !latency

package metrics

import (
	"sync"

	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

type latencyMetrics struct{}

func (lm *latencyMetrics) init(cm *sync.Map, registry *prometheus.Registry, nodeLabels prometheus.Labels) {
}
func (lm *latencyMetrics) begin()                                                          {}
func (lm *latencyMetrics) observe(info gen.ProcessShortInfo, topN int)                     {}
func (lm *latencyMetrics) flush()                                                          {}
