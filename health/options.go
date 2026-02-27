package health

import (
	"net/http"
	"time"
)

// Options configures the health actor.
type Options struct {
	// Port for the HTTP server. Default: 3000.
	Port uint16

	// Host for the HTTP server. Default: "localhost".
	Host string

	// Path prefix for health endpoints. Default: "/health".
	// Endpoints are registered as Path+"/live", Path+"/ready", Path+"/startup".
	Path string

	// Mux allows using an external HTTP mux instead of starting a dedicated server.
	// When set, the health actor registers its handlers on this mux and skips
	// creating its own HTTP server.
	Mux *http.ServeMux

	// CheckInterval is the interval for checking heartbeat timeouts.
	// Default: 1s.
	CheckInterval time.Duration
}
