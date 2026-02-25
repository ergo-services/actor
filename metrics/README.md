# Metrics Actor

A Prometheus metrics exporter actor for Ergo Framework that automatically collects and exposes node and network metrics via HTTP endpoint.

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
    CollectInterval: 5 * time.Second,     // Collection frequency
    TopN:            50,                   // Top-N entries for each metric group (processes and events)
}

n.Spawn(metrics.Factory, gen.ProcessOptions{}, options)
```

### Default Values

- **Port**: 3000
- **Host**: localhost
- **CollectInterval**: 10 seconds
- **TopN**: 50

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
| `ergo_events_published_total` | Gauge | Cumulative number of events published. Includes both local producer publishes and events arriving from remote nodes |
| `ergo_events_local_sent_total` | Gauge | Cumulative number of event messages delivered to local subscribers. Reflects actual fanout -- one publish with 100 subscribers produces 100 local deliveries |
| `ergo_events_remote_sent_total` | Gauge | Cumulative number of event messages sent to remote nodes. Counts one per node, not per subscriber, due to shared subscription optimization |

### Network Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
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
- **Running** -- number of processes currently executing callbacks or waiting for a Call response. The gap between Total and Running is normal -- most processes spend their time in Sleep state (idle, waiting for messages)
- **Zombie** -- number of zombie processes (green when 0, red when 1 or more). Non-zero value signals that some processes have terminated abnormally and were not properly cleaned up -- requires investigation
- **Memory Used** -- total OS memory used across selected nodes
- **Memory Alloc** -- total runtime memory allocated across selected nodes. A significant difference between Used and Alloc may indicate memory fragmentation or that the runtime is holding memory that could be released
- **Total Nodes** -- number of nodes matching the filter. Useful for quickly detecting if a node has left the cluster unexpectedly

#### Mailbox Latency (expanded row, requires `-tags=latency`)

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

1. **Identify the node.** Check Max Latency per Node and Stressed Processes per Node. If one node stands out while others are calm, the problem is localized -- look at that node's Resources and Network panels
2. **Identify the process.** Open the Top Stressed Processes table. The columns Application, Behavior, and Name tell you exactly what kind of actor is struggling. Multiple processes from the same application suggest the application itself is under pressure. A single process with extreme latency is likely stuck or blocked
3. **Understand the distribution.** The Latency Distribution panel shows whether the problem is isolated (one red sliver at the top of an otherwise green chart) or systemic (the entire chart shifting from green toward orange/red over time). A systemic shift means the node is overloaded and needs either scaling or load shedding
4. **Correlate with other panels.** High latency combined with high CPU suggests compute-bound processes. High latency with low CPU suggests processes are blocked on external I/O or waiting for responses from other actors. High latency with growing memory may indicate unbounded mailbox accumulation

#### Mailbox Depth (expanded row)

Three panels showing how many messages are currently queued in process mailboxes. Complementary to latency -- depth tells you "how many", latency tells you "how long".

- **Max Depth per Node** -- maximum mailbox queue depth per node over time. A growing value means at least one process is accumulating messages faster than it can process them. Compare with the latency Max Latency panel -- high depth with low latency means the process handles messages quickly but receives many; high depth with high latency means it is falling behind
- **Depth Distribution** -- stacked area chart showing how many processes fall into each depth range over time. Uses a flame color gradient: green tones for low depth (1-10 messages), yellow for moderate (50-100), orange for high (500-1K), red for critical (5K-10K+). In a healthy system most processes should have low depth. A shift toward red indicates growing backpressure
- **Top Processes by Depth** -- table showing processes with the deepest mailbox queues across the cluster. Columns: Application, Behavior, Name, PID, Node, Depth (plus Kubernetes labels when available). Sorted by depth descending. Use this to identify which actors are accumulating the most messages

#### Events (collapsed row)

A collapsed row containing ten panels. Click to expand. Organized from general to specific: cluster-wide rates and utilization, per-node breakdown, per-node rates, and top-N tables.

Two timeseries panels (cluster-wide overview):

- **Event Publish/Delivery Rate** -- three lines showing `rate()` of published, local delivered, and remote sent event messages. Published (blue) shows how often producers publish. Local Delivered (green) shows the actual fanout to local subscribers -- this is typically much higher than Published when events have many subscribers. Remote Sent (orange) shows messages sent to other nodes (one per node due to shared subscription optimization). A growing gap between Local Delivered and Published indicates increasing fanout load
- **Event Utilization** -- stacked timeseries showing the utilization state of all registered events. Active (green): publishing with subscribers -- event is working. On Demand (blue): uses Notify mechanism, waiting for subscribers or data -- correct behavior. Idle (grey): registered without Notify, no publishes, no subscribers -- potentially forgotten. No Subscribers (orange): publishing but nobody listening. No Publishing (yellow): subscribers waiting but producer never published. The total height equals the total number of registered events. A healthy system is mostly green and blue

Two timeseries panels (per-node rates):

- **Event Publish Rate per Node** -- event publish rate per node. Shows which nodes have the most active producers. A spike on one node while others are stable points to a localized producer issue
- **Event Delivery Rate per Node** -- local event delivery rate per node. Shows where fanout load concentrates. High delivery rate relative to publish rate indicates events with many subscribers on that node

Two timeseries panels (per-node counts):

- **Registered Events per Node** -- total number of registered events per node. A growing count without plateaus may indicate event registration leak (events being registered but never unregistered)
- **Max Subscribers per Node** -- maximum subscriber count for any single event on each node. A high value means at least one event has many consumers, which amplifies the cost of each publish

Four table panels (specific events):

- **Top Events by Subscribers** -- top 50 events by subscriber count. Columns: Event, Producer, Node, Subscribers. Sorted by subscribers descending. Identifies events with the widest audience
- **Top Events by Published** -- top 50 events by messages published. Columns: Event, Producer, Node, Published. Sorted by published descending. Identifies the most active producers
- **Top Events by Local Deliveries** -- top 50 events by messages delivered to local subscribers. Columns: Event, Producer, Node, Local Deliveries. Sorted descending. Shows which events create the most local fanout load. An event that publishes rarely but has many subscribers will rank high here
- **Top Events by Remote Sent** -- top 50 events by messages sent to remote nodes. Columns: Event, Producer, Node, Remote Sent. Sorted descending. Shows which events generate the most inter-node traffic

#### Process Activity (collapsed row)

A collapsed row containing eight panels organized by topic: message throughput first, then process utilization. Click to expand.

Two timeseries panels (message throughput overview):

- **Message Throughput (Cluster Total)** -- cluster-wide message rate showing total inbound (received by processes) and outbound (sent by processes). A sudden drop may indicate stalled processes or upstream failures
- **Message Throughput per Node** -- message rate per node showing inbound and outbound. Identifies nodes with the highest message flow

Two table panels (message throughput top-N):

- **Top Processes by Messages In** -- top 50 processes by total messages received (cumulative). Identifies which actors handle the most inbound traffic
- **Top Processes by Messages Out** -- top 50 processes by total messages sent (cumulative). Identifies which actors generate the most outbound traffic

Two timeseries panels (utilization overview):

- **Utilization Distribution** -- stacked area chart showing how many processes fall into each utilization range (1%-90%+). Utilization is `RunningTime / Uptime`. A shift toward higher ranges indicates increasing compute load
- **Max Utilization per Node** -- maximum process utilization per node. Persistently high values indicate a compute-bound actor that may need load distribution

Two panels (utilization detail):

- **Actor Running Time per Node** -- `rate(ergo_process_running_time_seconds)` per node, showing seconds of callback execution per second. When this approaches CPU core count, the node is compute-saturated
- **Top Processes by Utilization** -- table showing the busiest processes by lifetime utilization. A process at 90%+ is almost always busy

#### Processes (collapsed row)

A collapsed row containing six panels. Click to expand.

- **Processes (total)** -- total process count per node. Steady growth without a plateau may indicate a process leak (processes being spawned but never terminated)
- **Processes (running)** -- running process count per node. Helps identify load distribution across the cluster -- uneven running counts may point to hotspot nodes
- **Process Spawn Rate** -- rate of successfully spawned processes per node. Also shows failed spawn attempts (in red). Spawn failures indicate resource exhaustion or configuration errors. A sudden spike in spawn rate may signal a restart loop
- **Process Termination Rate** -- rate of terminated processes per node. When termination rate consistently exceeds spawn rate, the node is draining. When spawn rate exceeds termination rate, process count is growing -- correlate with the Processes panel to verify
- **Init Time per Node** -- bar gauge showing maximum ProcessInit duration per node. Color indicates severity: green < 100ms, yellow < 1s, red > 1s. Shows at a glance which nodes have slow initialization
- **Top Processes by Init Time** -- table showing processes with the longest ProcessInit duration. Identifies which actor types take the longest to initialize

#### Resources (collapsed row)

A collapsed row containing four timeseries graphs. Click to expand.

- **CPU User Time per Node** -- user CPU time percentage per node, normalized by core count. High user CPU indicates the application logic is compute-bound. Useful for identifying nodes that need horizontal scaling
- **CPU System Time per Node** -- system CPU time percentage per node, normalized by core count. High system CPU relative to user CPU suggests excessive syscalls, context switching, or I/O pressure rather than application workload
- **Memory (OS:used)** -- OS-reported memory used per node. Monotonic growth over time is a strong indicator of a memory leak. Compare across nodes to spot outliers
- **Memory (Runtime:alloc)** -- Go runtime allocated memory per node. Sawtooth pattern is normal (allocation followed by GC). Flat or steadily rising baseline between GC cycles points to objects that are not being collected

#### Network (collapsed row)

A collapsed row containing six timeseries graphs. Click to expand. Shows cluster-wide totals, per-node breakdowns, and node-pair detail for both message rates and byte rates.

- **Network Messages (Cluster Total)** -- total inbound and outbound message rate across all nodes. Provides a high-level view of cluster communication intensity. Sudden drops may indicate network partitions or node failures
- **Network Traffic (Cluster Total)** -- total inbound and outbound byte rate across all nodes. Helps estimate bandwidth requirements. A growing gap between message rate and byte rate means average message size is changing
- **Network Messages per Node** -- inbound and outbound message rate per node. Helps identify which nodes are communication hotspots and whether traffic is evenly distributed
- **Network Traffic per Node** -- inbound and outbound byte rate per node. Nodes with disproportionately high byte rate relative to message rate are sending larger payloads -- useful for identifying nodes that transfer bulk data
- **Network Messages Detail** -- message rate between each pair of connected nodes. Helps trace specific inter-node communication paths and detect unexpected or missing connections
- **Network Traffic Detail** -- byte rate between each pair of connected nodes. Useful for identifying which specific node-to-node link is saturated or carrying the most data

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
