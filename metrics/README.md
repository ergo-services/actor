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
    LatencyTopN:     50,                   // Top-N processes by mailbox latency
}

n.Spawn(metrics.Factory, gen.ProcessOptions{}, options)
```

### Default Values

- **Port**: 3000
- **Host**: localhost
- **CollectInterval**: 10 seconds
- **LatencyTopN**: 50

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

### Mailbox Latency Metrics

When built with `-tags=latency`, the metrics actor automatically collects per-process mailbox latency data. This enables detection of stressed processes whose mailboxes are growing.

```bash
go build -tags=latency ./...
```

Without the tag, latency measurement is disabled and no additional metrics are registered.

#### Latency Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `ergo_mailbox_latency_distribution` | Gauge | range | Number of processes in each latency range (snapshot per collect cycle) |
| `ergo_mailbox_latency_max_seconds` | Gauge | - | Maximum mailbox latency on this node |
| `ergo_mailbox_latency_processes` | Gauge | - | Number of processes with non-empty mailbox |
| `ergo_mailbox_latency_top_seconds` | Gauge | pid, name, application, behavior | Top-N processes by mailbox latency |

Distribution ranges: 1ms, 5ms, 10ms, 50ms, 100ms, 500ms, 1s, 5s, 10s, 30s, 60s, 60s+.

Each range represents an upper boundary. For example, "5ms" counts processes with latency between 1ms and 5ms. The "60s+" range counts processes with latency above 60 seconds. Values are snapshots -- each collect cycle counts from scratch, so values reflect the current state, not cumulative history.

#### Cardinality

For a cluster of 500 nodes with `LatencyTopN=50`:
- Distribution: 500 x 12 series = 6,000
- Max + Count gauges: 500 x 2 = 1,000
- Top-N gauges: 500 x 50 = 25,000
- Total: ~32,000 series

#### Grafana Queries

Latency distribution across the cluster (stacked timeseries):
```promql
sum(ergo_mailbox_latency_distribution{node=~"$node", range="1ms"})
sum(ergo_mailbox_latency_distribution{node=~"$node", range="5ms"})
...one query per range for controlled legend order...
```

Top 50 stressed processes across all nodes (table panel):
```promql
topk(50, ergo_mailbox_latency_top_seconds)
```

Per-node maximum latency:
```promql
ergo_mailbox_latency_max_seconds
```

Alert when any process has latency above 1 second:
```promql
ergo_mailbox_latency_max_seconds > 1
```

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
  
## Grafana Dashboard

A pre-built Grafana dashboard is included in `ergo-cluster.json`. It provides a comprehensive overview of an Ergo cluster with automatic refresh every 10 seconds.

### Importing

1. Open Grafana and go to Dashboards
2. Click "Import"
3. Upload the `ergo-cluster.json` file or paste its contents
4. Select your Prometheus data source

### Node Filter

The dashboard includes a `$node` variable dropdown at the top. It allows selecting one or more nodes to filter all panels. By default, all nodes are selected.

### Panels

#### Summary Row (top)

Six stat panels showing aggregated values for selected nodes:

- **Total Processes** -- total number of processes across selected nodes
- **Running** -- number of running processes (green). A large gap between Total and Running indicates many processes are idle or waiting
- **Zombie** -- number of zombie processes (green when 0, red when 1 or more). Non-zero value signals that some processes have terminated abnormally and were not properly cleaned up -- requires investigation
- **Memory Used** -- total OS memory used across selected nodes
- **Memory Alloc** -- total runtime memory allocated across selected nodes. A significant difference between Used and Alloc may indicate memory fragmentation or that the runtime is holding memory that could be released
- **Total Nodes** -- number of nodes matching the filter. Useful for quickly detecting if a node has left the cluster unexpectedly

#### Processes (collapsed row)

A collapsed row containing four timeseries graphs. Click to expand.

- **Processes (total)** -- total process count per node. Steady growth without a plateau may indicate a process leak (processes being spawned but never terminated)
- **Processes (running)** -- running process count per node. Helps identify load distribution across the cluster -- uneven running counts may point to hotspot nodes
- **Process Spawn Rate** -- rate of successfully spawned processes per node. Also shows failed spawn attempts (in red). Spawn failures indicate resource exhaustion or configuration errors. A sudden spike in spawn rate may signal a restart loop
- **Process Termination Rate** -- rate of terminated processes per node. When termination rate consistently exceeds spawn rate, the node is draining. When spawn rate exceeds termination rate, process count is growing -- correlate with the Processes panel to verify

#### CPU

Two timeseries graphs showing CPU utilization normalized by core count:

- **CPU User Time per Node** -- user CPU time percentage per node. High user CPU indicates the application logic is compute-bound. Useful for identifying nodes that need horizontal scaling
- **CPU System Time per Node** -- system CPU time percentage per node. High system CPU relative to user CPU suggests excessive syscalls, context switching, or I/O pressure rather than application workload

#### Memory

Two timeseries graphs showing memory usage over time:

- **Memory (OS:used)** -- OS-reported memory used per node. Monotonic growth over time is a strong indicator of a memory leak. Compare across nodes to spot outliers
- **Memory (Runtime:alloc)** -- Go runtime allocated memory per node. Sawtooth pattern is normal (allocation followed by GC). Flat or steadily rising baseline between GC cycles points to objects that are not being collected

#### Network (Cluster Total)

Two timeseries graphs showing cluster-wide network activity:

- **Network Messages (Cluster Total)** -- total inbound and outbound message rate across all nodes. Provides a high-level view of cluster communication intensity. Sudden drops may indicate network partitions or node failures
- **Network Traffic (Cluster Total)** -- total inbound and outbound byte rate across all nodes. Helps estimate bandwidth requirements. A growing gap between message rate and byte rate means average message size is changing

#### Network (Per Node)

Two timeseries graphs showing network activity broken down by node:

- **Network Messages per Node** -- inbound and outbound message rate per node. Helps identify which nodes are communication hotspots and whether traffic is evenly distributed
- **Network Traffic per Node** -- inbound and outbound byte rate per node. Nodes with disproportionately high byte rate relative to message rate are sending larger payloads -- useful for identifying nodes that transfer bulk data

#### Network (Detail)

Two timeseries graphs showing network activity between specific node pairs:

- **Network Messages Detail** -- message rate between each pair of connected nodes. Helps trace specific inter-node communication paths and detect unexpected or missing connections
- **Network Traffic Detail** -- byte rate between each pair of connected nodes. Useful for identifying which specific node-to-node link is saturated or carrying the most data

#### Mailbox Latency (requires `-tags=latency`)

An expanded row that appears right after the Summary. Only useful when the application is built with `-tags=latency`. When the tag is not used, these panels will show "No data".

Two timeseries panels at the top showing cluster-wide overview:

- **Max Latency** -- maximum mailbox latency across all selected nodes over time. Drawn in red. Hover on any point to see the exact value at that moment. A value above 1 second means at least one process has a message sitting in its mailbox for that long -- the process is either overloaded or stuck
- **Stressed Processes** -- stacked timeseries showing two categories: processes with latency under 1ms (light-blue, typically normal) and processes with latency 1ms or above (orange, worth attention). The total height is the number of processes with non-empty mailboxes. A growing orange area indicates increasing backpressure

Two timeseries graphs showing per-node breakdown:

- **Max Latency per Node** -- maximum mailbox latency per node over time. Helps identify which specific nodes are experiencing backpressure. Persistent high latency on a single node while others are low points to a hotspot or a stuck process on that node
- **Stressed Processes per Node** -- number of processes with non-empty mailboxes per node over time. Correlate with the Max Latency panel -- a node with high max latency but low stressed count has one problematic process, while high count with moderate latency suggests general overload

One stacked timeseries panel:

- **Latency Distribution** -- stacked area chart showing how many processes fall into each latency range over time. Uses a flame color gradient: green tones for low latency (1ms-10ms), yellow for moderate (50ms-100ms), orange for high (500ms-1s), red/dark-red for critical (5s-60s+). The legend is sorted from highest to lowest range. In a healthy system most of the area should be green/yellow. Growing red/orange areas indicate degradation

One table panel:

- **Top Stressed Processes** -- top 50 processes by mailbox latency across the cluster. Columns: Application, Behavior, Name, PID, Node, Latency (plus Kubernetes labels when available: Pod, Container, Cluster, Service). Sorted by latency descending. Directly answers "which process is the bottleneck?"

#### Reading the Latency Dashboard

**Start here: Max Latency and Stressed Processes (top row).** These two panels give an immediate answer to "is there a problem right now?" If Max Latency is under 100ms and the Stressed Processes panel is mostly light-blue or empty -- the system is healthy, no further investigation needed.

**React to these signals:**

- Max Latency above 1 second -- at least one process is severely behind. Move to the Top Stressed Processes table to identify it by name, application, and behavior
- Orange area growing in Stressed Processes -- multiple processes are accumulating latency above 1ms. Check the Latency Distribution panel to understand the severity spread
- A sudden spike in Max Latency followed by a return to normal -- a temporary burst of load. Compare with spawn/termination rates in the Processes row to see if it correlates with process lifecycle events

**Dig deeper:**

1. **Identify the node.** Check Max Latency per Node and Stressed Processes per Node. If one node stands out while others are calm, the problem is localized -- look at that node's CPU, memory, and network panels
2. **Identify the process.** Open the Top Stressed Processes table. The columns Application, Behavior, and Name tell you exactly what kind of actor is struggling. Multiple processes from the same application suggest the application itself is under pressure. A single process with extreme latency is likely stuck or blocked
3. **Understand the distribution.** The Latency Distribution panel shows whether the problem is isolated (one red sliver at the top of an otherwise green chart) or systemic (the entire chart shifting from green toward orange/red over time). A systemic shift means the node is overloaded and needs either scaling or load shedding
4. **Correlate with other panels.** High latency combined with high CPU suggests compute-bound processes. High latency with low CPU suggests processes are blocked on external I/O or waiting for responses from other actors. High latency with growing memory may indicate unbounded mailbox accumulation

#### Nodes Overview

A table at the bottom listing all selected nodes with columns: Node name, Uptime, Processes, Running, Zombie, Memory. Sorted by process count (descending). Provides a quick snapshot for comparing nodes side by side -- helps spot nodes that have recently restarted (low uptime), are overloaded (high process count), or have issues (non-zero zombie count).

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
