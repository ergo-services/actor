# Health Actor

A Kubernetes health probe actor for Ergo Framework. Actors register signals, send heartbeats, and the health actor serves `/health/*` endpoints with per-probe status aggregation.

Doc: https://docs.ergo.services/extra-library/actors/health

## Features

- **Kubernetes Integration**: Serves `/health/live`, `/health/ready`, and `/health/startup` endpoints
- **Signal Registration**: Actors register named signals with probe type and optional heartbeat timeout
- **Automatic Failure Detection**: Heartbeat timeout expiry and process termination both mark signals as down
- **Process Monitoring**: When a process that registered a signal terminates, its signals are automatically marked as down
- **Bitmask Probes**: A single signal can participate in multiple probes (liveness, readiness, startup)
- **Thread-Safe HTTP**: HTTP handlers read atomic values only -- no blocking in the HTTP goroutine
- **External Mux**: Optionally register handlers on a shared `*http.ServeMux` instead of starting a dedicated server
- **Helper Functions**: Package-level `Register`, `Unregister`, `Heartbeat`, `SignalUp`, `SignalDown` for convenient use from any actor

## Installation

```bash
go get ergo.services/actor/health
```

## Quick Start

### Basic Usage

```go
package main

import (
    "ergo.services/actor/health"
    "ergo.services/ergo"
    "ergo.services/ergo/gen"
)

func main() {
    n, _ := ergo.StartNode("mynode@localhost", gen.NodeOptions{})
    defer n.Stop()

    // Start health actor with default settings (port 3000)
    n.SpawnRegister(gen.Atom("health"), health.Factory, gen.ProcessOptions{},
        health.Options{Port: 8080})

    // Endpoints available:
    //   http://localhost:8080/health/live
    //   http://localhost:8080/health/ready
    //   http://localhost:8080/health/startup
    n.Wait()
}
```

### Configuration Options

```go
options := health.Options{
    Host:          "0.0.0.0",           // Listen address
    Port:          8080,                // HTTP port
    Path:          "/health",           // Path prefix (default: "/health")
    CheckInterval: 2 * time.Second,    // Heartbeat timeout check interval
}
```

Endpoints are registered as `Path+"/live"`, `Path+"/ready"`, `Path+"/startup"`. For example, with `Path: "/k8s"` the endpoints become `/k8s/live`, `/k8s/ready`, `/k8s/startup`.

### Default Values

- **Port**: 3000
- **Host**: localhost
- **Path**: /health
- **CheckInterval**: 1 second

## Signal Registration

Actors register named signals with the health actor. Each signal has a probe bitmask and an optional heartbeat timeout. The health actor monitors the registering process -- if it terminates, all its signals are marked as down.

### From Any Actor

Use the package-level helper functions:

```go
import "ergo.services/actor/health"

func (w *MyActor) Init(args ...any) error {
    // Register a signal for liveness and readiness probes with 5s heartbeat timeout
    health.Register(w, gen.Atom("health"), "db",
        health.ProbeLiveness|health.ProbeReadiness, 5*time.Second)

    // Register a signal for startup probe only, no heartbeat needed
    health.Register(w, gen.Atom("health"), "migrations",
        health.ProbeStartup, 0)

    return nil
}
```

### Probe Types

| Constant | Value | Endpoint |
|----------|-------|----------|
| `ProbeLiveness` | 1 | `/health/live` |
| `ProbeReadiness` | 2 | `/health/ready` |
| `ProbeStartup` | 4 | `/health/startup` |

Probes can be combined with bitwise OR: `ProbeLiveness | ProbeReadiness` registers a signal for both liveness and readiness endpoints.

When `Probe` is 0 in `RegisterRequest`, it defaults to `ProbeLiveness`.

### Heartbeat Timeout

When `Timeout` is set in `RegisterRequest`, the health actor expects periodic `MessageHeartbeat` messages for that signal. If no heartbeat arrives within the timeout, the signal is marked as down.

When `Timeout` is 0, no heartbeat is required. The signal stays up until explicitly marked down via `MessageSignalDown` or the registering process terminates.

```go
// Send heartbeat periodically
health.Heartbeat(w, gen.Atom("health"), "db")
```

### Manual Signal Control

```go
// Mark signal as down (e.g., lost database connection)
health.SignalDown(w, gen.Atom("health"), "db")

// Mark signal as up (e.g., database reconnected)
health.SignalUp(w, gen.Atom("health"), "db")

// Remove signal entirely
health.Unregister(w, gen.Atom("health"), "db")
```

## HTTP Endpoints

| Path | Probe | Description |
|------|-------|-------------|
| `/health/live` | ProbeLiveness | Kubernetes liveness probe |
| `/health/ready` | ProbeReadiness | Kubernetes readiness probe |
| `/health/startup` | ProbeStartup | Kubernetes startup probe |

Each endpoint returns:
- **200 OK** when all signals for that probe are up (or no signals are registered for that probe)
- **503 Service Unavailable** when any signal for that probe is down

### Response Format

```json
{"status":"healthy","signals":[{"signal":"db","status":"up","timeout":"5s"}]}
```

When unhealthy:

```json
{"status":"unhealthy","signals":[{"signal":"db","status":"down","timeout":"5s"}]}
```

When no signals are registered for a probe, the response contains no `signals` field:

```json
{"status":"healthy"}
```

## Message Types

| Message | Type | Description |
|---------|------|-------------|
| `RegisterRequest` / `RegisterResponse` | sync (Call) | Register a signal with probe bitmask and optional timeout |
| `UnregisterRequest` / `UnregisterResponse` | sync (Call) | Remove a signal |
| `MessageHeartbeat` | async (Send) | Update heartbeat timestamp for a signal |
| `MessageSignalUp` | async (Send) | Manually mark a signal as up |
| `MessageSignalDown` | async (Send) | Manually mark a signal as down |

Registration and unregistration use synchronous Call to confirm the operation completed before the caller proceeds. This prevents race conditions where a heartbeat arrives before the signal is registered. All types are registered with EDF for network transparency.

## External Mux

When you need to serve health endpoints alongside other HTTP handlers on the same port, provide an external `*http.ServeMux`:

```go
mux := http.NewServeMux()
mux.HandleFunc("/custom", myHandler)

options := health.Options{
    Mux: mux,
}

n.SpawnRegister(gen.Atom("health"), health.Factory, gen.ProcessOptions{}, options)
```

When `Mux` is set, the `Port` and `Host` fields are ignored since the caller is responsible for serving the mux.

## Extending with Custom Behavior

Create a custom health actor by embedding `health.Actor`:

```go
type MyHealth struct {
    health.Actor
}

func MyHealthFactory() gen.ProcessBehavior {
    return &MyHealth{}
}

func (h *MyHealth) Init(args ...any) (health.Options, error) {
    return health.Options{Port: 8080}, nil
}

func (h *MyHealth) HandleSignalDown(signal gen.Atom) error {
    h.Log().Error("signal went down: %s", signal)
    // Custom alerting, metric updates, etc.
    return nil
}

func (h *MyHealth) HandleSignalUp(signal gen.Atom) error {
    h.Log().Info("signal recovered: %s", signal)
    return nil
}
```

### ActorBehavior Interface

| Callback | Description |
|----------|-------------|
| `Init(args ...any) (Options, error)` | Initialize and return options |
| `HandleMessage(from gen.PID, message any) error` | Handle unrecognized messages |
| `HandleCall(from gen.PID, ref gen.Ref, message any) (any, error)` | Handle synchronous requests |
| `HandleInspect(from gen.PID, item ...string) map[string]string` | Extend Observer inspection. Base data (path, signals, check_interval) is always included; returned keys are merged on top |
| `HandleSignalDown(signal gen.Atom) error` | Called when a signal goes down |
| `HandleSignalUp(signal gen.Atom) error` | Called when a signal recovers |
| `Terminate(reason error)` | Called on process termination |

All callbacks have default (no-op) implementations and are optional.

## Kubernetes Configuration

```yaml
apiVersion: v1
kind: Pod
spec:
  containers:
    - name: myapp
      livenessProbe:
        httpGet:
          path: /health/live
          port: 3000
        initialDelaySeconds: 5
        periodSeconds: 10
      readinessProbe:
        httpGet:
          path: /health/ready
          port: 3000
        initialDelaySeconds: 5
        periodSeconds: 10
      startupProbe:
        httpGet:
          path: /health/startup
          port: 3000
        failureThreshold: 30
        periodSeconds: 2
```

## Example Application

A complete example is provided in the `example/` directory. It spawns a health actor and a demo worker that registers a signal with heartbeat.

### Running the Example

```bash
cd example
go run .
```

Test the endpoints:

```bash
curl http://localhost:3001/health/live
# {"status":"healthy","signals":[{"signal":"db","status":"up","timeout":"5s"}]}

curl http://localhost:3001/health/ready
# {"status":"healthy","signals":[{"signal":"db","status":"up","timeout":"5s"}]}

curl http://localhost:3001/health/startup
# {"status":"healthy"}
```

## License

See LICENSE file in the repository root.
