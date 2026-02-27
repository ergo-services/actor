package health

import "time"

const (
	DefaultPort          uint16        = 3000
	DefaultHost          string        = "localhost"
	DefaultPath          string        = "/health"
	DefaultCheckInterval time.Duration = time.Second
)
