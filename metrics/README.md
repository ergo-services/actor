# Metrics Actor

A Prometheus metrics exporter actor for Ergo Framework that automatically collects and exposes node and network metrics via HTTP endpoint.

Doc: https://docs.ergo.services/extra-library/actors/metrics

## Features

- **Automatic Base Metrics Collection**: Collects node metrics (uptime, processes, memory, CPU), network metrics (connected nodes, messages, bytes transferred), and event metrics (pub/sub subscribers, publish rates, delivery fanout)
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
    Path:            "/metrics",          // HTTP path (default: "/metrics")
    CollectInterval: 5 * time.Second,     // Collection frequency
    TopN:            50,                   // Top-N entries for each metric group (processes and events)
}

n.Spawn(metrics.Factory, gen.ProcessOptions{}, options)
```

### External Mux

When you need to serve metrics alongside other HTTP handlers on the same port, provide an external `*http.ServeMux`. The metrics actor registers its `/metrics` handler on the given mux and skips starting its own HTTP server:

```go
mux := http.NewServeMux()
mux.HandleFunc("/custom", myHandler)

options := metrics.Options{
    Mux:             mux,
    CollectInterval: 5 * time.Second,
}

n.Spawn(metrics.Factory, gen.ProcessOptions{}, options)
```

When `Mux` is set, the `Port` and `Host` fields are ignored since the caller is responsible for serving the mux.

### Default Values

- **Port**: 3000
- **Host**: localhost
- **Path**: /metrics
- **CollectInterval**: 10 seconds
- **TopN**: 50

## Custom Metrics

There are two ways to work with custom metrics: helper functions from any actor, or embedding `metrics.Actor` for direct registry access.

All custom metrics automatically receive a `node` const label set to the node name. Do not include `"node"` in your variable label names -- it will cause a "duplicate label names" registration error.

### Helper Functions

Any actor on the same node can register and update custom metrics using the helper functions. Registration is a synchronous `Call` (returns error on failure). Updates are asynchronous `Send` (fire-and-forget).

```go
// Register metrics (sync Call)
metrics.RegisterGauge(w, "metrics_actor_name", "request_count", "Total requests", []string{"method"})
metrics.RegisterCounter(w, "metrics_actor_name", "cache_hits", "Cache hit count", []string{"region"})
metrics.RegisterHistogram(w, "metrics_actor_name", "latency_seconds", "Request latency", []string{"path"}, nil)

// Update metrics (async Send)
metrics.GaugeSet(w, "metrics_actor_name", "request_count", 42, []string{"GET"})
metrics.GaugeAdd(w, "metrics_actor_name", "request_count", 1, []string{"POST"})
metrics.CounterAdd(w, "metrics_actor_name", "cache_hits", 1, []string{"us-east"})
metrics.HistogramObserve(w, "metrics_actor_name", "latency_seconds", 0.023, []string{"/api/users"})

// Unregister (async Send)
metrics.Unregister(w, "metrics_actor_name", "request_count")
```

The `to` parameter accepts anything `gen.Process.Send` accepts: `gen.Atom` name, `gen.PID`, `gen.ProcessID`, or `gen.Alias`.

When a process that registered a metric terminates, the metrics actor automatically unregisters all metrics owned by that process. If the `gen.MessageDownPID` does not match any registered metric owner, it is forwarded to `HandleMessage` so that actors embedding `metrics.Actor` can use their own monitors.

### Embedding metrics.Actor

For direct access to the Prometheus registry (e.g. periodic collection via `CollectMetrics`), embed `metrics.Actor`:

```go
type MyMetrics struct {
    metrics.Actor

    requestsTotal prometheus.Counter
    activeUsers   prometheus.Gauge
}

func CustomFactory() gen.ProcessBehavior {
    return &MyMetrics{}
}

func (m *MyMetrics) Init(args ...any) (metrics.Options, error) {
    m.requestsTotal = prometheus.NewCounter(prometheus.CounterOpts{
        Name: "my_app_requests_total",
        Help: "Total number of requests",
    })
    m.activeUsers = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "my_app_active_users",
        Help: "Number of active users",
    })

    m.Registry().MustRegister(m.requestsTotal, m.activeUsers)

    return metrics.Options{
        Port:            9090,
        CollectInterval: 5 * time.Second,
    }, nil
}

func (m *MyMetrics) CollectMetrics() error {
    m.requestsTotal.Add(10)
    m.activeUsers.Set(42)
    return nil
}
```

### Shared Mode

A single metrics actor processes messages sequentially -- one at a time. When many actors on the node send metric updates concurrently, the metrics actor's mailbox becomes the bottleneck. Registration (`RegisterRequest`) is a synchronous `Call`, so callers block until the metrics actor processes their request. High-throughput updates (`GaugeSet`, `CounterAdd`, etc.) queue up in the mailbox, increasing latency for all senders.

`metrics.Shared` solves this by allowing a pool of metrics actor instances to share the same Prometheus registry and custom metric storage. The pool distributes incoming messages across workers, providing parallel processing while all workers read and write to the same underlying registry.

```go
shared := metrics.NewShared()

// Primary actor: collects base Ergo metrics, registers /metrics handler on the mux.
// Identified by having both Shared and Mux (or Port) set.
primaryOpts := metrics.Options{
    Mux:             mux,
    Shared:          shared,
    Path:            "/metrics",
    CollectInterval: 10 * time.Second,
}

// Worker actor: handles custom metric registration and updates only.
// No HTTP server, no base metrics collection.
workerOpts := metrics.Options{
    Shared: shared,
}
```

The primary actor initializes base Ergo metrics, starts the HTTP handler, and runs the collection timer. Workers only process custom metric messages (`RegisterRequest`, `MessageGaugeSet`, etc.), distributing the load across the pool.

### Custom Top-N Metrics

Top-N metrics track the N highest (or lowest) values observed during each collection cycle. Unlike gauges or counters, a top-N metric accumulates observations and periodically flushes only the top entries to Prometheus. This is useful when you want to identify the most active, slowest, or largest items out of many -- without creating a separate time series for each one.

Each top-N metric is managed by a dedicated actor. Registration spawns this actor; observations are sent to it asynchronously. On each flush interval the actor writes the current top-N entries to a Prometheus GaugeVec and resets for the next cycle.

```go
// Register a top-N metric (sync Call, returns error)
// TopNMax keeps the N largest values; TopNMin keeps the N smallest
metrics.RegisterTopN(w, "topn_supervisor_name", "slowest_queries", "Slowest DB queries",
    10, metrics.TopNMax, []string{"query", "table"})

// Observe values (async Send)
metrics.TopNObserve(w, gen.Atom("radar_topn_slowest_queries"), 0.250, []string{"SELECT * FROM users", "users"})
metrics.TopNObserve(w, gen.Atom("radar_topn_slowest_queries"), 1.100, []string{"JOIN orders", "orders"})
```

The `to` parameter in `RegisterTopN` is the name of the SOFO supervisor that manages top-N actors. The `to` parameter in `TopNObserve` is the actor name -- by convention `"radar_topn_" + metricName`.

Ordering modes:
- `metrics.TopNMax` -- keeps the N largest values (e.g., slowest queries, busiest actors)
- `metrics.TopNMin` -- keeps the N smallest values (e.g., lowest latency, least active)

When the process that registered a top-N metric terminates, the actor automatically cleans up and unregisters its GaugeVec from Prometheus.

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
| `ergo_events_published_total` | Gauge | Cumulative number of events published by local producers |
| `ergo_events_received_total` | Gauge | Cumulative number of events received from remote nodes |
| `ergo_events_local_sent_total` | Gauge | Cumulative number of event messages delivered to local subscribers. Reflects actual fanout -- one publish with 100 subscribers produces 100 local deliveries |
| `ergo_events_remote_sent_total` | Gauge | Cumulative number of event messages sent to remote nodes. Counts one per node, not per subscriber, due to shared subscription optimization |
| `ergo_send_errors_local_total` | Gauge | Cumulative local send delivery errors (process unknown, terminated, mailbox full) |
| `ergo_send_errors_remote_total` | Gauge | Cumulative remote send delivery errors (connection failure) |
| `ergo_call_errors_local_total` | Gauge | Cumulative local call delivery errors (process unknown, terminated, mailbox full) |
| `ergo_call_errors_remote_total` | Gauge | Cumulative remote call delivery errors (connection failure) |

### Log Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `ergo_log_messages_total` | Gauge | level | Cumulative log message count by level (trace, debug, info, warning, error, panic). Counted once before fan-out to loggers |

### Network Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `ergo_connections_established_total` | Gauge | - | Cumulative connections established. Use `rate()` to detect connection churn |
| `ergo_connections_lost_total` | Gauge | - | Cumulative connections lost. Use `rate()` to detect connection churn |
| `ergo_acceptor_handshake_errors_total` | Gauge | acceptor | Cumulative handshake errors per acceptor interface |
| `ergo_connected_nodes_total` | Gauge | - | Total connected nodes |
| `ergo_remote_node_uptime_seconds` | Gauge | remote_node | Remote node uptime |
| `ergo_remote_messages_in_total` | Gauge | remote_node | Messages received from node |
| `ergo_remote_messages_out_total` | Gauge | remote_node | Messages sent to node |
| `ergo_remote_bytes_in_total` | Gauge | remote_node | Bytes received from node |
| `ergo_remote_bytes_out_total` | Gauge | remote_node | Bytes sent to node |

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

#### Grafana Queries

Latency distribution across the cluster (stacked timeseries):
```promql
sum(ergo_mailbox_latency_distribution{node=~"$node", range="1ms"})
sum(ergo_mailbox_latency_distribution{node=~"$node", range="5ms"})
...one query per range for controlled legend order...
```

Top-N stressed processes across all nodes (table panel):
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

### Mailbox Depth Metrics

The metrics actor automatically collects per-process mailbox queue depth -- the number of messages waiting in the mailbox at the moment of collection. This is complementary to latency: latency measures how long the oldest message has been waiting, while depth measures how many messages are queued.

No build tags required. Depth metrics are always active.

#### Depth Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `ergo_mailbox_depth_distribution` | Gauge | range | Number of processes in each depth range (snapshot per collect cycle) |
| `ergo_mailbox_depth_max` | Gauge | - | Maximum mailbox depth across all processes on this node |
| `ergo_mailbox_depth_top` | Gauge | pid, name, application, behavior | Top-N processes by mailbox depth |

Distribution ranges: 1, 5, 10, 50, 100, 500, 1K, 5K, 10K, 10K+.

Each range represents an upper boundary. For example, "5" counts processes with 2 to 5 messages in the mailbox. Processes with empty mailboxes are not counted.

### Event Metrics

The metrics actor collects per-event pub/sub metrics using `Node.EventRangeInfo()`. This provides visibility into which events have the most subscribers and which generate the most delivery load. The subscriber count for each event includes both `LinkEvent` and `MonitorEvent` subscribers.

No build tags required. Event metrics are always active.

#### Per-Event Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `ergo_event_subscribers_max` | Gauge | - | Maximum subscriber count across all events on this node |
| `ergo_event_utilization` | Gauge | state | Number of events in each utilization state (snapshot per collect cycle) |
| `ergo_event_subscribers_top` | Gauge | event, producer | Top-N events by subscriber count |
| `ergo_event_published_top` | Gauge | event, producer | Top-N events by messages published |
| `ergo_event_local_sent_top` | Gauge | event, producer | Top-N events by messages delivered to local subscribers |
| `ergo_event_remote_sent_top` | Gauge | event, producer | Top-N events by messages sent to remote nodes |

Utilization states: `active` (published and has subscribers -- event is doing its job), `on_demand` (event uses `Notify` mechanism, currently waiting for subscribers or data -- correct on-demand behavior), `idle` (registered without `Notify`, no publishes, no subscribers -- potentially forgotten), `no_subscribers` (published but nobody listening -- publishing into the void), `no_publishing` (subscribers waiting but producer never published). Every event falls into exactly one state. All values are snapshots per collect cycle.

The distinction between `published`, `local_sent`, and `remote_sent` reflects the pub/sub delivery model. A single publish may fan out to many local subscribers (`local_sent >> published`) and to multiple remote nodes (`remote_sent` counts one per node, not per subscriber, due to the shared subscription optimization). Comparing these values reveals the actual delivery load each event creates.

### Process Utilization Metrics

The metrics actor collects per-process utilization -- the ratio of callback running time to process uptime. This is a lifetime average that shows which actors have spent the most time executing callbacks relative to their total existence.

No build tags required. Utilization metrics are always active.

#### Utilization Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `ergo_process_utilization_distribution` | Gauge | range | Number of processes in each utilization range (snapshot per collect cycle) |
| `ergo_process_utilization_max` | Gauge | - | Maximum process utilization on this node |
| `ergo_process_utilization_top` | Gauge | pid, name, application, behavior | Top-N processes by utilization |

Distribution ranges: 1%, 5%, 10%, 25%, 50%, 75%, 90%, 90%+.

Utilization is computed as `RunningTime / Uptime`. A value of 0.50 means the process has spent 50% of its lifetime executing callbacks. The remaining time was spent waiting for messages. Processes with zero running time or zero uptime are excluded.

### Process Init Time Metrics

The metrics actor tracks how long each process spent in its `ProcessInit` callback. This identifies actors with slow initialization -- heavy setup, blocking I/O, or synchronous calls during init.

No build tags required. Init time metrics are always active.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `ergo_process_init_time_max_seconds` | Gauge | - | Maximum ProcessInit duration across all processes on this node |
| `ergo_process_init_time_top_seconds` | Gauge | pid, name, application, behavior | Top-N processes by ProcessInit duration |

### Process Throughput Metrics

Per-process message throughput top-N and node-level aggregates.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `ergo_process_messages_in_top` | Gauge | pid, name, application, behavior | Top-N processes by total messages received |
| `ergo_process_messages_out_top` | Gauge | pid, name, application, behavior | Top-N processes by total messages sent |
| `ergo_process_messages_in` | Gauge | - | Sum of messages received by all processes on this node |
| `ergo_process_messages_out` | Gauge | - | Sum of messages sent by all processes on this node |
| `ergo_process_running_time_seconds` | Gauge | - | Sum of callback running time across all processes on this node (seconds) |

Aggregate values are sums of per-process cumulative counters. When a process terminates, its contribution is removed, which may cause the aggregate to decrease. This is expected -- `rate()` handles it correctly in most cases, though short-lived process churn may produce minor artifacts.

### Process Wakeups and Drains Metrics

The metrics actor tracks process wakeups (transitions from Sleep to Running state) and drains (messages processed per wakeup). Drain ratio (`MessagesIn / Wakeups`) reveals the nature of a process's load that utilization alone cannot distinguish. Two processes with 80% utilization may have completely different workloads: one with drain ~1 processes individual messages slowly (heavy per-message computation), while one with drain ~100 processes messages quickly but receives so many that it never sleeps (high throughput load). The optimization strategy is different: the first needs faster callbacks, the second needs load distribution.

No build tags required. Wakeups and drains metrics are always active.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `ergo_process_wakeups_top` | Gauge | pid, name, application, behavior | Top-N processes by cumulative wakeup count |
| `ergo_process_drains_top` | Gauge | pid, name, application, behavior | Top-N processes by drain ratio (MessagesIn / Wakeups) |
| `ergo_process_wakeups` | Gauge | - | Sum of wakeups across all processes on this node |

On the dashboard, the Throughput panels show wakeup rate as a third line alongside message in/out rates -- the visual gap between message rate and wakeup rate represents the drain effect. Drains per Node timeseries shows per-node drain ratio over time. Top Processes by Drains table identifies specific actors with the highest drain.

## Observer Integration

The metrics actor provides inspection data in the Observer UI: total metric count, HTTP endpoint, collection interval, custom metric count, and current values for all registered metrics.

When embedding `metrics.Actor` and overriding `HandleInspect`, the base inspection data is always included. The actor merges both results: base data first, then user data on top. User keys override base keys with the same name, but base keys that the user does not set are preserved.

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

### Dashboard Rows

**Summary** (top, always visible) -- cluster-wide snapshot: process counts (total, running, zombie), memory usage, node count. Zombie > 0 or unexpected node count drop means something is wrong.

**Mailbox Latency** (expanded, requires `-tags=latency`) -- mailbox wait times: max latency, stressed process count, latency distribution, and top-N stressed processes. Answers "are actors keeping up with their workload?" Requires building with `-tags=latency`, otherwise shows "No data".

**Mailbox Depth** (expanded) -- mailbox queue sizes: max depth per node, depth distribution, top-N by depth. Complementary to latency -- depth is "how many messages are waiting", latency is "how long the oldest has been waiting".

**Events** (collapsed) -- pub/sub health: publish/delivery rates, event utilization states (active, idle, no subscribers, no publishing), per-node breakdown, and top-N tables by subscribers, published, local deliveries, remote sent.

**Process Activity** (collapsed) -- actor workload analysis: message throughput and wakeup rates, drain ratio (messages processed per wakeup), process utilization distribution, actor running time, and delivery error rates (Send/Call failures split by local/remote).

**Processes** (collapsed) -- process lifecycle: counts per node, spawn/termination rates, init time per node, top-N by init time. Growing count without plateau suggests leaks, spawn failures indicate resource exhaustion.

**Resources** (collapsed) -- CPU and memory per node. User/system CPU normalized by core count, OS memory used, runtime memory allocated. Monotonic memory growth signals leaks.

**Logging** (collapsed) -- log message rate by level. Spikes in warning/error indicate issues.

**Network** (collapsed) -- inter-node communication: message and byte rates (cluster total, per node, per node pair), connectivity strength (full mesh percentage as stat and per-node bar gauge), and connection events (established/lost/handshake errors).

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
