package health

import "time"

const (
	DefaultPort          uint16        = 3000
	DefaultHost          string        = "localhost"
	DefaultPath          string        = "/health"
	DefaultCheckInterval time.Duration = time.Second
)

const docsURL = "https://docs.ergo.services/extra-library/actors/health"
