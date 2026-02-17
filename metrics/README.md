# Metrics Actor

A Prometheus metrics exporter actor for Ergo Framework that automatically collects and exposes node and network metrics via HTTP endpoint.

## Features

- **Automatic Base Metrics Collection**: Collects node metrics (uptime, processes, memory, CPU) and network metrics (connected nodes, messages, bytes transferred)
- **HTTP Endpoint**: Exposes metrics in Prometheus format at `/metrics`
- **Periodic Collection**: Configurable interval for automatic metrics updates
- **Extensible**: Easy to extend with custom application-specific metrics

## Installation

```bash
go get ergo.services/actor/metrics
```

## Quick Start

### Basic Usage

```go
package main

import (
    "ergo.services/actor/metrics"
    "ergo.services/ergo"
    "ergo.services/ergo/gen"
)

func main() {
    n, _ := ergo.StartNode("mynode@localhost", gen.NodeOptions{})
    defer n.Stop()

    // Start metrics actor with default metrics
    n.Spawn(metrics.Factory, gen.ProcessOptions{}, metrics.Options{})

    // Metrics available at http://localhost:3000/metrics
    n.Wait()
}
```

### Configuration Options

```go
options := metrics.Options{
    Host:            "0.0.0.0",           // Listen address
    Port:            9090,                 // HTTP port
    CollectInterval: 5 * time.Second,     // Collection frequency
}

n.Spawn(metrics.Factory, gen.ProcessOptions{}, options)
```

### Default Values

- **Port**: 3000
- **Host**: localhost
- **CollectInterval**: 10 seconds

## Extending with Custom Metrics

Create a custom metrics actor with `metrics.Actor`:

```go
package main

import (
    "time"
    "ergo.services/actor/metrics"
    "ergo.services/ergo/gen"
    "github.com/prometheus/client_golang/prometheus"
)

// Custom metrics actor
type MyMetrics struct {
    metrics.Actor

    requestsTotal prometheus.Counter
    activeUsers   prometheus.Gauge
}

// Factory function
func CustomFactory() gen.ProcessBehavior {
    return &MyMetrics{}
}

// Initialize custom metrics
func (m *MyMetrics) Init(args ...any) (metrics.Options, error) {
    // Create custom metrics
    m.requestsTotal = prometheus.NewCounter(prometheus.CounterOpts{
        Name: "my_app_requests_total",
        Help: "Total number of requests",
    })

    m.activeUsers = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "my_app_active_users",
        Help: "Number of active users",
    })

    // Register with the metrics registry
    m.Registry().MustRegister(m.requestsTotal, m.activeUsers)

    // Return options
    return metrics.Options{
        Port:            9090,
        CollectInterval: 5 * time.Second,
    }, nil
}

// Collect custom metrics periodically
func (m *MyMetrics) CollectMetrics() error {
    // Update your custom metrics here
    m.requestsTotal.Add(10)
    m.activeUsers.Set(42)

    return nil
}
```

## Available Base Metrics

### Node Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `ergo_node_uptime_seconds` | Gauge | Node uptime in seconds |
| `ergo_processes_total` | Gauge | Total number of processes |
| `ergo_processes_running` | Gauge | Number of running processes |
| `ergo_processes_zombie` | Gauge | Number of zombie processes |
| `ergo_processes_spawned_total` | Gauge | Cumulative number of successfully spawned processes |
| `ergo_processes_spawn_failed_total` | Gauge | Cumulative number of failed spawn attempts |
| `ergo_processes_terminated_total` | Gauge | Cumulative number of terminated processes |
| `ergo_memory_used_bytes` | Gauge | Memory used in bytes |
| `ergo_memory_alloc_bytes` | Gauge | Memory allocated in bytes |
| `ergo_cpu_user_seconds` | Gauge | User CPU time in seconds |
| `ergo_cpu_system_seconds` | Gauge | System CPU time in seconds |
| `ergo_applications_total` | Gauge | Total number of applications |
| `ergo_applications_running` | Gauge | Number of running applications |
| `ergo_registered_names_total` | Gauge | Total registered process names |
| `ergo_registered_aliases_total` | Gauge | Total registered aliases |
| `ergo_registered_events_total` | Gauge | Total registered events |

### Network Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `ergo_connected_nodes_total` | Gauge | - | Total connected nodes |
| `ergo_remote_node_uptime_seconds` | Gauge | node | Remote node uptime |
| `ergo_remote_messages_in_total` | Gauge | node | Messages received from node |
| `ergo_remote_messages_out_total` | Gauge | node | Messages sent to node |
| `ergo_remote_bytes_in_total` | Gauge | node | Bytes received from node |
| `ergo_remote_bytes_out_total` | Gauge | node | Bytes sent to node |

## Observer Integration

The metrics actor implements `HandleInspect` to provide metric information in the Observer UI:

```go
// Access via Observer at http://localhost:9911
// Shows:
// - total_metrics: count of registered metrics
// - http_endpoint: metrics HTTP endpoint URL
// - collect_interval: collection interval
// - Each metric name with current values
```

## Integration with Prometheus

Configure Prometheus to scrape metrics:

```yaml
scrape_configs:
  - job_name: 'ergo-metrics'
    static_configs:
      - targets: ['localhost:3000']
```

## Example Application

A complete example with both basic and custom metrics is provided in the `example/` directory.

### Running the Example

```bash
cd example
go build

# Run with default metrics
./example

# Run with custom metrics
./example -custom
```

The example includes:
- **Observer UI**: http://localhost:9911 - Web interface for inspecting processes and metrics
- **Prometheus Metrics**: http://localhost:3000/metrics
  
## Best Practices

1. **Custom Metrics**: Always register custom metrics in `Init()` before the HTTP server starts
2. **Collection Logic**: Implement `CollectMetrics()` for periodic updates to custom metrics
3. **Error Handling**: Return errors from `CollectMetrics()` only for fatal issues; log non-fatal errors
4. **Metric Types**: Choose appropriate Prometheus metric types:
   - Counter: Monotonically increasing values
   - Gauge: Values that can go up and down
   - Histogram: Observations in configurable buckets
   - Summary: Similar to histogram with quantiles

## License

See LICENSE file in the repository root.
