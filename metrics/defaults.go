package metrics

import "time"

const (
	DefaultPort            = 3000
	DefaultHost            = "localhost"
	DefaultPath            = "/metrics"
	DefaultCollectInterval = 10 * time.Second
	DefaultTopN            = 50
)
