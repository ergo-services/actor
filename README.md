[![Gitbook Documentation](https://img.shields.io/badge/GitBook-Documentation-f37f40?style=plastic&logo=gitbook&logoColor=white&style=flat)](https://docs.ergo.services/extra-library/applications)
[![MIT license](https://img.shields.io/badge/license-MIT-brightgreen.svg)](https://opensource.org/licenses/MIT)
[![Telegram Community](https://img.shields.io/badge/Telegram-ergo__services-229ed9?style=flat&logo=telegram&logoColor=white)](https://t.me/ergo_services)
[![Twitter](https://img.shields.io/badge/Twitter-ergo__services-00acee?style=flat&logo=twitter&logoColor=white)](https://twitter.com/ergo_services)
[![Reddit](https://img.shields.io/badge/Reddit-r/ergo__services-ff4500?style=plastic&logo=reddit&logoColor=white&style=flat)](https://reddit.com/r/ergo_services)

Extra library of actors for the Ergo Framework 3.0 (and above)

## leader

Distributed leader election for coordinating work across a cluster - task schedulers, resource managers, singleton workers. Term-based election with a quorum taken over the peers the actor currently knows.

Discovery is your job: resolve peers however suits your deployment and hand the names to `Join`. The actor negotiates with them, and membership propagates through the protocol once one side knows the other.

**Features:**
- Term-based election with automatic failover
- A leader that steps down on its own once it can no longer reach a quorum
- `MinClusterSize` floor, so a fragment too small to be a cluster does not operate as one
- Consumer-driven membership: `Join` / `Leave`, never withdrawn by the library on silence alone
- Optional confirmation of leadership against an authority outside the cluster
- No external dependencies

There is one leader per cluster, but the number of clusters is not fixed, so from outside you can see two leaders at once. Not from a dropped connection - that keeps the peer in the view, so quorum does not shrink and only one side can elect - but from diverged views, which is what a cold start without seed nodes produces. Set `MinClusterSize` above half the expected node count and two disjoint groups can never both qualify.

**Two things you must do**, both of which fail quietly if skipped: register the wire types on the node before it carries traffic (`leader.NetworkTypes()`, `leader.ErrorTypes()`), and set `MinClusterSize` deliberately rather than inheriting the default. See [Leader](https://docs.ergo.services/extra-library/actors/leader) for details.

See [documentation](https://docs.ergo.services/extra-library/actors/leader) for details.


## health

Kubernetes health probe actor that serves `/health/live`, `/health/ready`, and `/health/startup` endpoints. Actors register named signals with probe type and optional heartbeat timeout. When a signal goes down (missed heartbeat, process termination, or explicit notification), the corresponding probe endpoint returns 503.

**Features:**
- Kubernetes liveness, readiness, and startup probe endpoints
- Signal registration with bitmask probe selection and heartbeat timeout
- Automatic failure detection on heartbeat timeout or process termination
- Thread-safe HTTP responses via atomic operations
- External mux support for sharing a port with other HTTP handlers
- Package-level helper functions for convenient signal management

See [documentation](https://docs.ergo.services/extra-library/actors/health) for details.

## metrics

Prometheus metrics exporter actor that automatically collects and exposes Ergo node and network telemetry via HTTP endpoint. Provides observability for monitoring cluster health, resource usage, and inter-node communication patterns.

**Features:**
- Automatic collection of node metrics (uptime, processes, memory, CPU)
- Network metrics per remote node (messages, bytes transferred, connection uptime)
- HTTP endpoint exposing metrics in Prometheus format
- Extensible - embed `metrics.Actor` to add custom application metrics
- Configurable collection interval
- External mux support for sharing a port with other HTTP handlers
- Observer UI integration for real-time inspection

See [documentation](https://docs.ergo.services/extra-library/actors/metrics) for details.
