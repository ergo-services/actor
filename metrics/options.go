package metrics

import (
	"net/http"
	"time"
)

type Options struct {
	Port            uint16
	Host            string
	Path            string // HTTP path for metrics endpoint. Default: "/metrics".
	CollectInterval time.Duration
	TopN            int

	// Mux allows using an external HTTP mux instead of starting a dedicated server.
	// When set, the metrics actor registers its handler on this mux
	// and skips creating its own HTTP server.
	Mux *http.ServeMux

	// Shared enables sharing the prometheus registry and custom metrics storage
	// across multiple metrics actor instances.
	// Primary actor: set Shared + Port (or Mux) -- handles base metrics + HTTP + custom metrics.
	// Pool workers: set Shared only -- handle custom metrics only.
	// See NewShared().
	Shared *Shared
}
