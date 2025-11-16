# Z - Health Probe Actor

Actor implementation providing HTTP endpoints for health probes.

## Endpoints

- `/livez` - Liveness probe
- `/readyz` - Readiness probe
- `/startupz` - Startup probe
- `/healthz` - General health check

## Usage

```go
import "ergo.services/actor/z"
```

## License

MIT
