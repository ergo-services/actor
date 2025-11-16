package metrics

import "time"

type Options struct {
	Port            uint16
	Host            string
	CollectInterval time.Duration
}
