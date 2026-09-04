# leader

Distributed leader election for coordinating work across a cluster - task schedulers, resource managers, singleton workers. Term-based election with a quorum taken over the peers the actor currently knows.

Discovery is not part of this actor. Resolve peers however suits your deployment - a registrar lookup, static configuration, a message from elsewhere - and hand the names to `Join`. The actor negotiates with them, and membership propagates through the protocol once one side knows the other.

Full documentation: [docs.ergo.services/extra-library/actors/leader](https://docs.ergo.services/extra-library/actors/leader)

## Three things to get right

All three fail quietly if skipped - the node starts, nothing errors, and the problem appears later as a cluster that never converges. The first one refuses to start instead.

**Register the wire types** on the node before it carries any traffic. `Init` checks the node's registry and returns an error naming the fix, because without them every vote fails to encode and no leader is ever elected.

The actor deliberately does not register them itself. Registration is node-scoped, so a type added once a connection is already established never reaches that wire - a node that forgot, then started a leader that registered late, would pass every check and still never join an election. A loud error at startup is the only honest outcome.

```go
gen.ApplicationSpec{
    Network: gen.ApplicationNetwork{
        RegisterTypes:  leader.NetworkTypes(),
        RegisterErrors: leader.ErrorTypes(),
    },
    // ...
}
```

`ApplicationSpec.Network` is processed during `ApplicationLoad`, before any process in the application is spawned. `node.Network().RegisterTypes(leader.NetworkTypes())` before the node starts serving works too.

**Withdraw peers your discovery no longer lists**, with `Leave`. Not strictly mandatory - `GhostTTL` drops an unreachable peer after five seconds as a safety net - but it is the only precise mechanism, and it matters most where node names are dynamic. Every replaced pod leaves an unreachable member behind, and the framework reports everything on a lost node as a connection loss whatever actually happened to it. Without a bound, two rolling deploys of a five-node cluster produce a quorum of seven that the five living nodes can never assemble.

**Set `MinClusterSize` deliberately.** It is the smallest view - this node plus the peers it knows - that may have a leader at all. The default is 3, the smallest majority that survives losing one node. A single-node deployment must set 1 explicitly; 2 is permitted and warned about, because a quorum of 2 of 2 tolerates no failure.

## Usage

```go
type coordinator struct {
    leader.Actor
}

func factory() gen.ProcessBehavior { return &coordinator{} }

type discoverPeers struct{}

func (c *coordinator) Init(args ...any) (leader.Options, error) {
    // Join from a handler frame, not from here: the options are not applied yet, so a
    // vote sent from Init would carry an empty ClusterID and be dropped by the receiver.
    if err := c.Send(c.PID(), discoverPeers{}); err != nil {
        return leader.Options{}, err
    }
    return leader.Options{
        ClusterID:      "scheduler",
        MinClusterSize: 3,
    }, nil
}

func (c *coordinator) HandleMessage(from gen.PID, message any) error {
    switch message.(type) {
    case discoverPeers:
        nodes := myDiscovery() // whatever resolves peers for you
        for _, node := range nodes {
            c.Join(gen.ProcessID{Name: "coordinator", Node: node})
        }
        // And withdraw what discovery no longer lists: you know a pod is gone, the
        // actor can only guess from a timer.
        for _, peer := range c.Peers() {
            if contains(nodes, peer.Node) == false {
                c.Leave(peer.Node)
            }
        }
    }
    return nil
}

func (c *coordinator) HandleBecomeLeader() error {
    // start the work that exactly one node should be doing
    return nil
}

func (c *coordinator) HandleBecomeFollower(leader gen.PID) error {
    // stop it
    return nil
}
```

Embedding `leader.Actor` is enough to satisfy `leader.ActorBehavior` - only `Init` has no default. `HandleBecomeLeader` and `HandleBecomeFollower` do have defaults, but they log a warning: winning leadership and doing nothing with it is almost always a missing implementation.

## One leader per cluster, and what a partition does to that

There is exactly one leader per cluster. The number of clusters, however, is not fixed - so from outside, looking at the deployment as one thing, you can see two leaders at once, for as long as the split lasts.

A dropped connection does not cause that. Losing a connection keeps the peer in the view and only forgets how to reach it, so the quorum denominator survives the partition, and two disjoint groups cannot both be a majority of the same set - at most one side elects. What does cause it is diverged views: if each side only ever learned about its own members, each holds a majority of its own smaller view and each elects. That is the cold-start case, and it is the price of dynamic membership with no seed list - the actor is never told how large the deployment is meant to be, so it cannot tell a group of three from half of six.

Two ways to change the trade:

- set `MinClusterSize` above half the expected node count, so two disjoint groups can never both qualify, at the cost of no leader in any partition smaller than that;
- implement `HandleConfirmLeader` and make leadership contingent on an authority outside the cluster, such as a Kubernetes `Lease`.

A leader that can no longer reach a quorum steps down on its own, without waiting to be told, which bounds how long a superseded leader keeps acting - but does not make the window zero.

The part worth thinking about is the external resource. Leadership is scoped to a cluster; a database row, a queue or a lock is not. When a partition turns one cluster into two, that resource is now shared between two clusters, each with a legitimate leader, and nothing in the election can tell it which one to obey. If leadership authorises something irreversible, the resource has to arbitrate: gate every such action on a monotonic fencing token that the resource itself validates and rejects when stale.

## Observability

`HandleInspect` reports the full election state: the role as a word, the current term, the peers by name, the quorum in force, whether the timers are armed, counters for every silently dropped message, and the timestamps of the last transitions. Pass `help` for the list of keys.

Those keys use the reserved `ergo:` prefix - `ergo:state`, `ergo:term`, `ergo:quorum` and so on - the same convention the core behaviors follow. If you override `HandleInspect`, your keys are merged on top of the actor's own, so your fields sit beside the election state and one of its keys is replaced only if you name it with the prefix.
