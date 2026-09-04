module ergo.services/actor/metrics/example

go 1.21

require (
	ergo.services/actor/metrics v0.0.0
	ergo.services/application/observer v0.1.1-0.20260901123004-83a1d62b96d9
	ergo.services/ergo v1.999.321-0.20260902074819-ba6b95f9188d
	github.com/prometheus/client_golang v1.20.5
)

require (
	ergo.services/meta/sse v0.2.1-0.20260901122738-1cc6bbe402cb // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.55.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	golang.org/x/sys v0.22.0 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
)

replace (
	ergo.services/actor/metrics => ../
	ergo.services/ergo => ../../../ergo
)
