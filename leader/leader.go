package leader

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/lib"
)

type ActorBehavior interface {
	gen.ProcessBehavior

	Init(args ...any) (Options, error)
	HandleMessage(from gen.PID, message any) error
	HandleCall(from gen.PID, ref gen.Ref, request any) (any, error)
	Terminate(reason error)
	HandleInspect(from gen.PID, item ...string) map[string]string

	// Leadership callbacks
	HandleConfirmLeader() (bool, error)
	HandleBecomeLeader() error
	HandleBecomeFollower(leader gen.PID) error

	// Peer management callbacks
	HandlePeerJoined(peer gen.PID) error
	HandlePeerLeft(peer gen.PID) error

	HandleEvent(event gen.MessageEvent) error
	HandleSpan(span gen.TracingSpan) error
	HandleLog(message gen.MessageLog) error
}

// processKindCandidate makes a campaigning replica distinguishable from a
// healthy follower. gen/process.go:63-73 allows a behavior to report any name.
const processKindCandidate gen.ProcessKind = "candidate"

const docsURL = "https://docs.ergo.services/extra-library/actors/leader"

type Actor struct {
	gen.Process

	behavior ActorBehavior
	mailbox  gen.ProcessMailbox

	// leader election state
	clusterID          string
	bootstrap          []gen.ProcessID
	electionTimeoutMin int
	electionTimeoutMax int
	heartbeatInterval  int
	minClusterSize     int
	ghostTTL           time.Duration
	term               uint64
	votedFor           gen.PID
	isLeader           bool
	leader             gen.PID
	peers              map[gen.PID]bool
	votesReceived      map[gen.PID]bool

	electionTimer  gen.CancelFunc
	heartbeatTimer gen.CancelFunc

	// A non-nil CancelFunc only means one was handed out, not that a timer will
	// still fire: these are one-shot and a spent handle is never cleared. Track
	// armed state explicitly so the reported value is not a lie.
	electionArmed  bool
	heartbeatArmed bool
	electionGen    uint64
	heartbeatGen   uint64

	// reported is what the behavior last believes leadership to be: self after a
	// reported win, the leader's PID after a reported step-down, zero for none. It
	// exists so each transition is reported exactly once.
	reported gen.PID

	// trap, when set, turns an exit request from a non-parent into a regular message
	// instead of terminating. Mirrors act.Actor.
	trap      bool
	spanStart int64

	// unreachableSince stamps a member whose connection dropped. It stays in the view
	// until GhostTTL passes, then it is dropped - see Options.GhostTTL.
	unreachableSince map[gen.Atom]time.Time

	// withdrawn records peers the consumer took out through Leave, so traffic already
	// in flight from them cannot re-admit what the consumer just removed.
	withdrawn map[gen.Atom]time.Time

	// lastContact is the most recent evidence that a member is reachable, by node.
	// A successful send or any inbound protocol message counts.
	lastContact map[gen.Atom]time.Time

	// Observability only, no protocol role: answers "what does this replica
	// believe, and who is it talking to" from one Inspect call.
	termChangedAt    time.Time
	lastHeartbeatIn  time.Time // last heartbeat accepted from a leader
	lastHeartbeatOut time.Time // last heartbeat fan-out by this leader
	// declared holds the peers the consumer handed over through Join, keyed by node
	// name because Join names a process and the PID is unknown until the peer answers.
	// A declared peer counts toward the view immediately: the consumer is the
	// authority on who belongs, and waiting for an answer would mean the floor can
	// never be lifted without traffic that only a campaigning node emits.
	declared        map[gen.Atom]declaredPeer
	confirmDenied   uint64 // leadership withheld by HandleConfirmLeader
	lastDeniedAt    time.Time
	unclusteredAt   time.Time            // last time the floor was reported, for rate limiting
	unclusteredView int                  // view size last reported, so a change is always logged
	sendFailures    map[gen.PID]uint64   // per-peer cumulative send failures
	sendFailing     map[gen.PID]struct{} // peers whose last send failed
	dropped         map[string]uint64    // silently-dropped counts by reason
}

// Options for leader election
// declaredPeer is one membership entry. pid is zero until the peer answers; target is
// how to reach it before that.
type declaredPeer struct {
	target gen.ProcessID
	pid    gen.PID
}

type Options struct {
	ClusterID string
	Bootstrap []gen.ProcessID

	ElectionTimeoutMin int // Minimum election timeout (ms, default: 150)
	ElectionTimeoutMax int // Maximum election timeout (ms, default: 300)
	HeartbeatInterval  int // Heartbeat interval (ms, default: 50)

	// GhostTTL is how long an unreachable peer stays in the view before it is
	// dropped (ms, default: 5000).
	//
	// A lost connection is kept for a while because it is usually a blip, and
	// shrinking the view on every blip would make quorum flap. But with dynamic node
	// names an unreachable peer may be a pod that is gone for good and whose name will
	// never exist again, and keeping those forever grows the quorum past the number of
	// nodes that exist - after two rolling deploys of a five-node cluster, a quorum of
	// seven that can never be assembled.
	//
	// This is not a safety control. MinClusterSize is: a group below the floor does not
	// operate regardless of how the view shrank. GhostTTL only decides how long to give
	// a peer the benefit of the doubt, so a few seconds is the useful range - long
	// enough to outlast a blip at a 150-300 ms election cycle, short enough that dead
	// names do not survive into the next stage of a rollout.
	GhostTTL int

	// MinClusterSize is the smallest view - this node plus the peers it knows - that
	// may have a leader. A smaller view neither campaigns nor keeps leadership and
	// reports state "unclustered". It is a floor on what this node observes, not a
	// declaration of deployment size, and it is not a quorum: quorum stays a majority
	// of the current view.
	//
	// Default 3, the smallest majority that survives one loss. Lower values are warned
	// about, not rejected - 1 lets a lone node appoint itself, 2 tolerates no failure.
	// It forbids singletons; it does not prevent fragmentation.
	MinClusterSize int
}

type msgVote struct {
	ClusterID string
	Term      uint64
	Candidate gen.PID
}

type msgVoteReply struct {
	ClusterID string
	Term      uint64
	Granted   bool
}

type msgHeartbeat struct {
	ClusterID string
	Term      uint64
	Leader    gen.PID
}

// Generation-stamped so a cancelled-but-already-queued timeout cannot drive a
// spurious election. Local messages; neither is registered for the wire.
type msgElectionTimeout struct{ Gen uint64 }
type msgHeartbeatTimeout struct{ Gen uint64 }

func (l *Actor) ProcessInit(process gen.Process, args ...any) (rr error) {
	var ok bool

	if l.behavior, ok = process.Behavior().(ActorBehavior); ok == false {
		unknown := strings.TrimPrefix(reflect.TypeOf(process.Behavior()).String(), "*")
		return fmt.Errorf("ProcessInit: not an ActorBehavior %s", unknown)
	}

	if lib.Recover() {
		defer func() {
			if r := recover(); r != nil {
				pc, fn, line, _ := runtime.Caller(2)
				l.Log().Panic("Leader init failed: %#v at %s[%s:%d]",
					r, runtime.FuncForPC(pc).Name(), fn, line)
				rr = gen.TerminateReasonPanic
			}
		}()
	}

	l.Process = process
	l.mailbox = process.Mailbox()

	// Before behavior.Init, which may call Join: none of these depend on the options,
	// and creating them afterwards left a Join from Init writing to a nil map.
	l.peers = make(map[gen.PID]bool)
	l.declared = make(map[gen.Atom]declaredPeer)
	l.lastContact = make(map[gen.Atom]time.Time)
	l.withdrawn = make(map[gen.Atom]time.Time)
	l.unreachableSince = make(map[gen.Atom]time.Time)
	l.sendFailures = make(map[gen.PID]uint64)
	l.sendFailing = make(map[gen.PID]struct{})
	l.dropped = make(map[string]uint64)

	opts, err := l.behavior.Init(args...)
	if err != nil {
		return err
	}

	// Validate options
	if opts.ClusterID == "" {
		return fmt.Errorf("ClusterID cannot be empty")
	}

	l.clusterID = opts.ClusterID
	l.bootstrap = opts.Bootstrap

	// Derive the companion rather than defaulting each independently, so a partial
	// configuration does not fail startup over a value the caller never set.
	l.electionTimeoutMin = opts.ElectionTimeoutMin
	l.electionTimeoutMax = opts.ElectionTimeoutMax
	switch {
	case l.electionTimeoutMin < 1 && l.electionTimeoutMax < 1:
		l.electionTimeoutMin, l.electionTimeoutMax = 150, 300
	case l.electionTimeoutMax < 1:
		l.electionTimeoutMax = l.electionTimeoutMin * 2
	case l.electionTimeoutMin < 1:
		l.electionTimeoutMin = l.electionTimeoutMax / 2
	}

	l.heartbeatInterval = opts.HeartbeatInterval
	if l.heartbeatInterval < 1 {
		l.heartbeatInterval = 50
	}

	l.minClusterSize = opts.MinClusterSize
	if l.minClusterSize < 1 {
		l.minClusterSize = 3
	}

	l.ghostTTL = time.Duration(opts.GhostTTL) * time.Millisecond
	if l.ghostTTL < 1 {
		l.ghostTTL = 5 * time.Second
	}

	// Not rejected: the weak model tolerates several leaders, so a low floor delivers
	// less rather than breaking the contract, and single-node deployments are real.
	switch l.minClusterSize {
	case 1:
		l.Log().Warning("MinClusterSize 1: a lone node will appoint itself leader, so " +
			"nothing prevents a fragment from operating alone")
	case 2:
		l.Log().Warning("MinClusterSize 2: quorum is 2 of 2, so losing either node ends " +
			"leadership - no fault tolerance. Sensible only when an external authority " +
			"gates leadership through HandleConfirmLeader")
	}

	if l.electionTimeoutMax <= l.electionTimeoutMin {
		return fmt.Errorf("ElectionTimeoutMax (%d) must be greater than ElectionTimeoutMin (%d)",
			l.electionTimeoutMax, l.electionTimeoutMin)
	}

	if l.heartbeatInterval >= l.electionTimeoutMin {
		l.Log().Warning("HeartbeatInterval (%dms) >= ElectionTimeoutMin (%dms) - elections may be unstable",
			l.heartbeatInterval, l.electionTimeoutMin)
	}

	// Bootstrap is Join declared up front, so it seeds the same membership instead of
	// being a second, parallel set of send targets.
	for _, b := range l.bootstrap {
		if b.Node == l.Node().Name() && b.Name == l.Name() {
			continue
		}
		l.declared[b.Node] = declaredPeer{target: b}
	}
	l.termChangedAt = time.Now()

	// The protocol types have to be on the node by now: without them every vote fails to
	// encode, silently, leaving a cluster that never converges.
	if err := l.checkNetworkTypes(); err != nil {
		return err
	}

	l.resetElectionTimer()

	return nil
}

// checkNetworkTypes reports whether the node can encode this actor's own protocol.
func (l *Actor) checkNetworkTypes() error {
	for _, v := range NetworkTypes() {
		t := reflect.TypeOf(v)
		got, ok := l.Node().Network().LookupType(fmt.Sprintf("#%s/%s", t.PkgPath(), t.Name()))
		if ok == true && got == t {
			continue
		}
		return fmt.Errorf("%s is not registered on this node: pass leader.NetworkTypes() to "+
			"ApplicationSpec.Network.RegisterTypes (or Network().RegisterTypes) before the node "+
			"serves traffic - see %s", t, docsURL)
	}
	return nil
}

// viewSize is the number of nodes this replica believes are in the cluster: the
// peers it knows plus itself. l.peers never contains self - discoverPeer returns
// early for own PID - so every threshold taken over the view must add one.
func (l *Actor) viewSize() int {
	return len(l.declared) + 1
}

// sendDeclared delivers a protocol message once to every member, addressing each by
// PID where known and by name otherwise. One set means one message per peer; the
// previous pair of loops over peers and bootstrap sent twice to anyone in both.
// sendDeclared delivers a message once per member. Peers whose PID is known are
// addressed by PID; the rest are addressed by name only when reaching out is the point.
//
// includeUnresolved is false on the heartbeat path. A peer that has never answered is
// not a follower: there is no election timer of its own to suppress, and if it is up it
// will campaign and be corrected by the reply. Addressing it by name would make every
// heartbeat tick attempt a connection to a node that may not exist, and establishing a
// connection is synchronous inside this callback.
func (l *Actor) sendDeclared(message any, includeUnresolved bool) {
	for _, d := range l.declared {
		if d.pid != (gen.PID{}) {
			l.sendPeer(d.pid, message)
			continue
		}
		if includeUnresolved == false {
			continue
		}
		if err := l.Send(d.target, message); err != nil {
			l.Log().Debug("send to unresolved peer %s: %s", d.target, err)
		}
	}
}

// quorum is a majority of the current view. Taking it over len(l.peers) instead is
// short by one for every even cluster size and degenerates to 1 at a single peer,
// which lets a node self-elect with no grants at all.
func (l *Actor) quorum() int {
	return l.viewSize()/2 + 1
}

// reportUnclustered makes an unmet floor visible - it is otherwise indistinguishable
// from a hang. Rate limited, because the election timer fires several times a second;
// a change in view size is always reported.
func (l *Actor) reportUnclustered() {
	const repeat = 30 * time.Second

	if l.viewSize() == l.unclusteredView && time.Since(l.unclusteredAt) < repeat {
		return
	}
	l.unclusteredView = l.viewSize()
	l.unclusteredAt = time.Now()
	l.Log().Warning("unclustered: view %d < MinClusterSize %d - not campaigning",
		l.viewSize(), l.minClusterSize)
}

// dropGhosts removes members that have been unreachable for longer than GhostTTL. With
// dynamic node names an unreachable peer is often a name that will never come back, and
// keeping it would inflate quorum beyond the nodes that exist.
func (l *Actor) dropGhosts() {
	for node, since := range l.unreachableSince {
		if time.Since(since) < l.ghostTTL {
			continue
		}
		delete(l.unreachableSince, node)
		if _, declared := l.declared[node]; declared == false {
			continue
		}
		delete(l.declared, node)
		l.Log().Info("peer on %s dropped after %s unreachable (view %d, quorum %d)",
			node, l.ghostTTL, l.viewSize(), l.quorum())
	}
}

// clustered reports whether the view is large enough to have a leader.
func (l *Actor) clustered() bool {
	return l.viewSize() >= l.minClusterSize
}

// drop turns a discarded message into a readable counter and a log line.
func (l *Actor) drop(reason string, format string, args ...any) {
	l.dropped[reason]++
	l.Log().Warning(format, args...)
}

func (l *Actor) ProcessRun() (rr error) {
	var message *gen.MailboxMessage

	if lib.Recover() {
		defer func() {
			if r := recover(); r != nil {
				pc, fn, line, _ := runtime.Caller(2)
				l.Log().Panic("Leader panic: %#v at %s[%s:%d]",
					r, runtime.FuncForPC(pc).Name(), fn, line)
				rr = gen.TerminateReasonPanic
			}
		}()
	}

	var savedTracing gen.Tracing

	for {
		if l.State() != gen.ProcessStateRunning {
			return gen.TerminateReasonKill
		}

		if message != nil {
			gen.ReleaseMailboxMessage(message)
			message = nil
		}

		for {
			msg, ok := l.mailbox.Urgent.Pop()
			if ok {
				message = msg.(*gen.MailboxMessage)
				break
			}

			msg, ok = l.mailbox.System.Pop()
			if ok {
				message = msg.(*gen.MailboxMessage)
				break
			}

			msg, ok = l.mailbox.Main.Pop()
			if ok {
				message = msg.(*gen.MailboxMessage)
				break
			}

			// The log queue has to be drained or a leader.Actor registered as a node
			// logger accumulates messages that are never consumed.
			msg, ok = l.mailbox.Log.Pop()
			if ok {
				if reason := l.behavior.HandleLog(msg.(gen.MessageLog)); reason != nil {
					return reason
				}
				continue
			}

			return nil
		}

	retry:
		switch message.Type {
		case gen.MailboxMessageTypeRegular:
			// Carry the incoming trace through this actor. Ignoring it terminated every
			// trace that passed through a leader process.
			hasTracing := message.Tracing.ID != [2]uint64{}
			if hasTracing {
				savedTracing = l.PropagatingTrace()
				l.SetPropagatingTrace(message.Tracing)
				l.spanStart = time.Now().UnixNano()
			}

			reason := l.handleMessage(message.From, message.Message)

			if hasTracing {
				errStr := ""
				if reason != nil {
					errStr = reason.Error()
				}
				l.sendSpanProcessed(message, gen.TracingKindSend, errStr)
				if reason == nil && l.PropagatingTrace().ID == message.Tracing.ID {
					l.SetPropagatingTrace(savedTracing)
				}
			}

			if reason != nil {
				return reason
			}

		case gen.MailboxMessageTypeRequest:
			result, reason := l.behavior.HandleCall(message.From, message.Ref, message.Message)
			if reason != nil {
				if reason == gen.TerminateReasonNormal && result != nil {
					l.SendResponse(message.From, message.Ref, result)
				}
				return reason
			}
			if result != nil {
				l.SendResponse(message.From, message.Ref, result)
			}

		case gen.MailboxMessageTypeEvent:
			if reason := l.behavior.HandleEvent(message.Message.(gen.MessageEvent)); reason != nil {
				return reason
			}

		case gen.MailboxMessageTypeSpan:
			if reason := l.behavior.HandleSpan(message.Message.(gen.TracingSpan)); reason != nil {
				return reason
			}

		case gen.MailboxMessageTypeExit:
			// With the trap set, an exit from anyone but the parent becomes a regular
			// message. Without it any exit is fatal, and an application group member is not
			// restarted, so one linked child would remove this node for good.
			switch exit := message.Message.(type) {
			case gen.MessageExitPID:
				if l.trap && message.From != l.Parent() {
					message.Type = gen.MailboxMessageTypeRegular
					goto retry
				}
				return fmt.Errorf("%s: %w", exit.PID, exit.Reason)
			case gen.MessageExitProcessID:
				if l.trap {
					message.Type = gen.MailboxMessageTypeRegular
					goto retry
				}
				return fmt.Errorf("%s: %w", exit.ProcessID, exit.Reason)
			case gen.MessageExitAlias:
				if l.trap {
					message.Type = gen.MailboxMessageTypeRegular
					goto retry
				}
				return fmt.Errorf("%s: %w", exit.Alias, exit.Reason)
			case gen.MessageExitEvent:
				if l.trap {
					message.Type = gen.MailboxMessageTypeRegular
					goto retry
				}
				return fmt.Errorf("%s: %w", exit.Event, exit.Reason)
			case gen.MessageExitNode:
				if l.trap {
					message.Type = gen.MailboxMessageTypeRegular
					goto retry
				}
				return fmt.Errorf("%s: %w", exit.Name, gen.ErrNoConnection)
			default:
				panic(fmt.Sprintf("unknown exit: %#v", exit))
			}

		case gen.MailboxMessageTypeInspect:
			items := message.Message.([]string)
			// election state first, the behavior may override any of the fields
			result := l.inspect(items...)
			for k, v := range l.behavior.HandleInspect(message.From, items...) {
				result[k] = v
			}
			l.SendResponse(message.From, message.Ref, result)
		}
	}
}

func (l *Actor) ProcessTerminate(reason error) {
	l.cancelElectionTimer()
	l.cancelHeartbeatTimer()

	// behavior is nil when ProcessInit failed its type assertion, and this is called
	// anyway; dereferencing it turned a clear configuration error into a panic.
	if l.behavior == nil {
		return
	}
	l.behavior.Terminate(reason)
}

func (l *Actor) ProcessKind() gen.ProcessKind {
	return gen.ProcessKindFollower
}

func (l *Actor) handleMessage(from gen.PID, message any) error {
	switch msg := message.(type) {
	case gen.MessageDownPID:
		if l.peers[msg.PID] == true {
			delete(l.peers, msg.PID)
			delete(l.sendFailing, msg.PID)
			delete(l.sendFailures, msg.PID)
			delete(l.lastContact, msg.PID.Node)
			delete(l.votesReceived, msg.PID)

			// A lost connection is not a death: the framework raises this with
			// ErrNoConnection for a live peer whose connection dropped. Shrinking the view
			// would lower quorum, so a blip would let a fragment elect. Keep the member,
			// forget only how to reach it.
			unreachable := errors.Is(msg.Reason, gen.ErrNoConnection)
			if unreachable {
				if d, ok := l.declared[msg.PID.Node]; ok {
					d.pid = gen.PID{}
					l.declared[msg.PID.Node] = d
				}
				l.unreachableSince[msg.PID.Node] = time.Now()
				l.Log().Info("peer %s unreachable (connection lost) - kept in the view for %s",
					msg.PID, l.ghostTTL)
			} else {
				delete(l.declared, msg.PID.Node)
				delete(l.unreachableSince, msg.PID.Node)
			}

			if err := l.behavior.HandlePeerLeft(msg.PID); err != nil {
				return err
			}

			// The floor applies continuously, not only at formation. Without this a
			// single surviving node of a larger cluster is a majority of its own view
			// and keeps leadership indefinitely.
			if l.isLeader == true && l.clustered() == false {
				l.Log().Warning("stepping down: view %d < MinClusterSize %d",
					l.viewSize(), l.minClusterSize)
				return l.becomeFollower(gen.PID{})
			}
		}
		if l.leader == msg.PID {
			l.Log().Info("leader down")
			return l.becomeFollower(gen.PID{})
		}

	// The ClusterID guard gates peer discovery as well as handling, so a mismatch
	// costs both the term update and the peer. Never drop it silently.
	case msgVote:
		if msg.ClusterID != l.clusterID {
			l.drop("cluster_id_mismatch_vote",
				"dropping msgVote from %s: cluster %q does not match %q (term %d)",
				from, msg.ClusterID, l.clusterID, msg.Term)
			return nil
		}
		if err := l.discoverPeer(from); err != nil {
			return err
		}
		return l.handleVote(from, msg)

	case msgVoteReply:
		if msg.ClusterID != l.clusterID {
			l.drop("cluster_id_mismatch_vote_reply",
				"dropping msgVoteReply from %s: cluster %q does not match %q (term %d, granted %v)",
				from, msg.ClusterID, l.clusterID, msg.Term, msg.Granted)
			return nil
		}
		if err := l.discoverPeer(from); err != nil {
			return err
		}
		return l.handleVoteReply(from, msg)

	case msgHeartbeat:
		if msg.ClusterID != l.clusterID {
			l.drop("cluster_id_mismatch_heartbeat",
				"dropping msgHeartbeat from %s: cluster %q does not match %q (term %d)",
				from, msg.ClusterID, l.clusterID, msg.Term)
			return nil
		}
		if err := l.discoverPeer(from); err != nil {
			return err
		}
		return l.handleHeartbeat(from, msg)

	case msgElectionTimeout:
		return l.handleElectionTimeout(msg)

	case msgHeartbeatTimeout:
		return l.handleHeartbeatTimeout(msg)

	default:
		return l.behavior.HandleMessage(from, message)
	}

	return nil
}

func (l *Actor) discoverPeer(pid gen.PID) error {
	if pid == l.PID() {
		return nil
	}

	delete(l.unreachableSince, pid.Node)

	// A peer the consumer withdrew stays out for one election timeout, long enough for
	// messages already in flight to drain. Re-declaring it is the consumer's call.
	if at, ok := l.withdrawn[pid.Node]; ok {
		if time.Since(at) < time.Duration(l.electionTimeoutMax)*time.Millisecond {
			l.drop("withdrawn_peer", "ignoring %s: withdrawn by the consumer", pid)
			return nil
		}
		delete(l.withdrawn, pid.Node)
	}

	// Anything received from a peer is the strongest evidence of reachability there
	// is, and it is what keeps a leader's quorum contact fresh.
	l.lastContact[pid.Node] = time.Now()

	if _, exists := l.peers[pid]; exists {
		return nil
	}

	l.peers[pid] = true
	// Either resolve an existing declaration or record a peer we were never told
	// about: a node speaking this cluster's protocol is a member, which is how
	// membership propagates once one side knows the other.
	d := l.declared[pid.Node]
	d.pid = pid
	if d.target == (gen.ProcessID{}) {
		d.target = gen.ProcessID{Name: l.Name(), Node: pid.Node}
	}
	l.declared[pid.Node] = d
	l.Monitor(pid)
	return l.behavior.HandlePeerJoined(pid)
}

// IsLeader returns true if this node is the leader
func (l *Actor) IsLeader() bool {
	return l.isLeader
}

// Leader returns the current leader PID (empty if no leader)
func (l *Actor) Leader() gen.PID {
	return l.leader
}

// Term returns the current election term
func (l *Actor) Term() uint64 {
	return l.term
}

// Peers returns a snapshot of current peer list
func (l *Actor) Peers() []gen.PID {
	peers := make([]gen.PID, 0, len(l.peers))
	for pid := range l.peers {
		peers = append(peers, pid)
	}
	return peers
}

// PeerCount returns the number of known peers
func (l *Actor) PeerCount() int {
	return len(l.peers)
}

// ClusterID returns the cluster identifier
func (l *Actor) ClusterID() string {
	return l.clusterID
}

// Bootstrap returns the bootstrap peer list
func (l *Actor) Bootstrap() []gen.ProcessID {
	return l.bootstrap
}

// HasPeer checks if a PID is a known peer
func (l *Actor) HasPeer(pid gen.PID) bool {
	return l.peers[pid]
}

// Broadcast sends a message to every member of the current view, by PID where known
// and by name otherwise, and reports the number of failed targets with the first
// error rather than discarding them.
func (l *Actor) Broadcast(message any) (failed int, err error) {
	for _, d := range l.declared {
		target := any(d.target)
		if d.pid != (gen.PID{}) {
			target = d.pid
		}
		if e := l.Send(target, message); e != nil {
			failed++
			if err == nil {
				err = e
			}
		}
	}
	return failed, err
}

// Join declares a peer as part of this cluster and opens negotiation with it.
//
// Discovery is the consumer's job; hand the names here. The peer counts toward the
// view immediately, before it answers, so the floor can be reached without traffic
// that only a campaigning node emits. Idempotent per node. See Leave.
func (l *Actor) Join(peer gen.ProcessID) {
	// Keyed by node, so a peer on this node is this node whatever it is registered as.
	// Comparing the name too would let a consumer vote for itself.
	if peer.Node == l.Node().Name() {
		return
	}

	// Before Init returns the ClusterID is unset, so the vote would be dropped by the
	// receiver's guard. Refuse loudly rather than send an unusable one.
	if l.clusterID == "" {
		l.Log().Error("Join(%s) before Init returned: ClusterID is not configured yet - "+
			"Join from a handler frame after Init, not from Init itself", peer)
		return
	}

	delete(l.withdrawn, peer.Node)

	if _, exists := l.declared[peer.Node]; exists == false {
		l.declared[peer.Node] = declaredPeer{target: peer}
		l.Log().Info("peer %s declared (view %d)", peer, l.viewSize())
	}

	if err := l.Send(peer, msgVote{
		ClusterID: l.clusterID,
		Term:      l.term,
		Candidate: l.PID(),
	}); err != nil {
		// Not fatal: the peer is part of the view regardless, and either side will make
		// contact later.
		l.Log().Warning("Join(%s): send failed: %s", peer, err)
	}
}

// Leave withdraws a peer from the view and from every quorum computed afterwards.
//
// The library never withdraws one by itself: a peer that stops answering is down,
// not gone, and shrinking the view on silence lets a fragment lower its own bar.
func (l *Actor) Leave(node gen.Atom) {
	d, declared := l.declared[node]
	if declared == false {
		return
	}
	delete(l.declared, node)

	if d.pid != (gen.PID{}) {
		delete(l.peers, d.pid)
		delete(l.sendFailing, d.pid)
		delete(l.sendFailures, d.pid)
	}
	l.withdrawn[node] = time.Now()
	delete(l.lastContact, node)
	l.Log().Info("peer on %s withdrawn (view %d)", node, l.viewSize())
}

// election logic

// becomeFollower steps down and reports newLeader, zero meaning "none known".
//
// votedFor is left alone - setTerm owns it, because a vote belongs to a term.
// Clearing votesReceived retires a candidacy, so a late grant cannot elect us for a
// term that already has a leader. The callback fires once per change, including a
// follower losing its leader and a candidate standing down.
func (l *Actor) becomeFollower(newLeader gen.PID) error {
	l.isLeader = false
	l.leader = newLeader
	l.votesReceived = nil

	if err := l.SetProcessKind(gen.ProcessKindFollower); err != nil {
		l.Log().Debug("could not report follower process kind: %s", err)
	}

	l.cancelHeartbeatTimer()
	l.resetElectionTimer()

	if l.reported == newLeader {
		return nil
	}
	l.reported = newLeader
	return l.behavior.HandleBecomeFollower(newLeader)
}

func (l *Actor) becomeCandidate() error {
	l.setTerm(l.term + 1)
	l.votedFor = l.PID()
	l.isLeader = false
	l.votesReceived = make(map[gen.PID]bool)

	if err := l.SetProcessKind(processKindCandidate); err != nil {
		l.Log().Debug("could not report candidate process kind: %s", err)
	}

	votes := 1 // vote for self
	quorum := l.quorum()

	l.Log().Debug("election: term=%d view=%d quorum=%d", l.term, l.viewSize(), quorum)

	vote := msgVote{
		ClusterID: l.clusterID,
		Term:      l.term,
		Candidate: l.PID(),
	}

	l.sendDeclared(vote, true)

	// Win without waiting only when there is genuinely nobody to ask. The test is the
	// view, not l.peers: a declared peer is someone to ask before its PID is known.
	if l.viewSize() == 1 && votes >= quorum {
		return l.becomeLeader()
	}

	// An undecided election has to be retried, and only this node can retry it.
	// Without re-arming here a candidate that misses quorum stops electing forever.
	l.resetElectionTimer()
	return nil
}

func (l *Actor) becomeLeader() error {
	// Won the election. Whether that entitles this node to act is a separate
	// question, and one the behavior may answer no to.
	confirmed, err := l.behavior.HandleConfirmLeader()
	if err != nil {
		// Fail closed. An error means "could not determine", which is not consent.
		l.denyLeadership("HandleConfirmLeader failed: %s", err)
		return nil
	}
	if confirmed == false {
		l.denyLeadership("HandleConfirmLeader withheld leadership")
		return nil
	}

	l.isLeader = true
	l.leader = l.PID()

	l.cancelElectionTimer()
	l.resetHeartbeatTimer()

	l.SetProcessKind(gen.ProcessKindLeader)
	l.Log().Debug("became leader: term=%d", l.term)
	if err := l.behavior.HandleBecomeLeader(); err != nil {
		// Roll back, and do not let the rollback's own failure hide the original.
		if rollback := l.becomeFollower(gen.PID{}); rollback != nil {
			l.Log().Error("rollback after HandleBecomeLeader failed: %s", rollback)
		}
		return err
	}
	l.reported = l.PID()

	l.sendHeartbeat()
	return nil
}

// denyLeadership abandons a won election. The timer is armed with a backoff rather
// than the usual timeout: the denial is about the world, not this candidate, so
// re-campaigning at once would hot-loop against the authority that said no.
func (l *Actor) denyLeadership(format string, args ...any) {
	l.confirmDenied++
	l.lastDeniedAt = time.Now()
	l.Log().Warning("leadership withheld (denials %d): "+format,
		append([]any{l.confirmDenied}, args...)...)

	l.isLeader = false
	l.votedFor = gen.PID{}
	l.leader = gen.PID{}
	l.votesReceived = nil

	if err := l.SetProcessKind(gen.ProcessKindFollower); err != nil {
		l.Log().Debug("could not report follower process kind: %s", err)
	}

	l.cancelHeartbeatTimer()
	l.armElectionTimerAfter(l.deniedBackoff())
}

// deniedBackoff grows with consecutive denials, capped at 20 election timeouts, so a
// persistently denied group settles into slow polling.
func (l *Actor) deniedBackoff() time.Duration {
	base := time.Duration(l.electionTimeoutMax) * time.Millisecond
	factor := l.confirmDenied
	if factor > 20 {
		factor = 20
	}
	// #nosec G115 -- factor is clamped to 20
	return base * time.Duration(factor)
}

func (l *Actor) handleVote(from gen.PID, msg msgVote) error {
	if msg.Term < l.term {
		// ClusterID is required: without it the receiver's own guard drops this reply
		// and the stale candidate never learns the real term.
		l.Send(from, msgVoteReply{ClusterID: l.clusterID, Term: l.term, Granted: false})
		return nil
	}

	if msg.Term > l.term {
		l.setTerm(msg.Term)
		if err := l.becomeFollower(gen.PID{}); err != nil {
			return err
		}
	}

	granted := false
	if l.votedFor == (gen.PID{}) || l.votedFor == msg.Candidate {
		granted = true
		l.votedFor = msg.Candidate
		l.resetElectionTimer()
	}

	l.Send(from, msgVoteReply{ClusterID: l.clusterID, Term: l.term, Granted: granted})
	return nil
}

func (l *Actor) handleVoteReply(from gen.PID, msg msgVoteReply) error {
	if msg.Term > l.term {
		l.setTerm(msg.Term)
		if err := l.becomeFollower(gen.PID{}); err != nil {
			return err
		}
		return nil
	}

	// only process vote replies if we're still a candidate for this term
	if msg.Term != l.term {
		return nil
	}

	if msg.Granted == false {
		return nil
	}

	if l.isLeader == true {
		return nil
	}

	if l.votedFor != l.PID() {
		return nil
	}

	if l.votesReceived == nil {
		return nil
	}

	// A grant only counts if the sender is a member. Counting a stranger, or a peer
	// that has since gone, inflates the numerator against a denominator it is not part
	// of.
	if _, member := l.declared[from.Node]; member == false {
		l.drop("vote_from_non_member",
			"ignoring grant from %s: not a member of this view", from)
		return nil
	}

	l.votesReceived[from] = true

	// count actual votes received
	votes := 1 // vote for self
	for _, granted := range l.votesReceived {
		if granted == true {
			votes++
		}
	}

	if votes >= l.quorum() {
		return l.becomeLeader()
	}

	return nil
}

func (l *Actor) handleHeartbeat(from gen.PID, msg msgHeartbeat) error {
	// A stale heartbeat means the sender still believes it leads a term we have
	// left. Dropping it silently is why a deposed leader never learns to step
	// down; the reply that corrects it arrives with N2 in step 6.
	if msg.Term < l.term {
		l.drop("stale_heartbeat",
			"dropping msgHeartbeat from %s: term %d is behind our term %d, sender still claims leadership",
			from, msg.Term, l.term)
		// Correct the sender rather than only dropping it. msgVoteReply carries our
		// term, and handleVoteReply tests for a higher term before anything else, so
		// the stale leader adopts it and steps down. No new wire type is needed.
		l.Send(from, msgVoteReply{ClusterID: l.clusterID, Term: l.term, Granted: false})
		return nil
	}

	l.lastHeartbeatIn = time.Now()

	if msg.Term > l.term {
		l.setTerm(msg.Term)
	}

	if l.isLeader == true {
		l.Log().Warning("heartbeat from %s claims leadership in our own term %d - stepping down",
			from, msg.Term)
	}

	// One path for every case: accept the sender as leader. That also retires our own
	// candidacy for this term and reports the transition once, with the real leader.
	return l.becomeFollower(msg.Leader)
}

func (l *Actor) handleElectionTimeout(msg msgElectionTimeout) error {
	if msg.Gen != l.electionGen {
		l.drop("stale_election_timeout",
			"ignoring election timeout from generation %d, current is %d",
			msg.Gen, l.electionGen)
		return nil
	}
	l.electionArmed = false
	l.dropGhosts()

	if l.isLeader == true {
		return nil
	}

	if l.clustered() == false {
		l.reportUnclustered()
		l.resetElectionTimer()
		return nil
	}

	return l.becomeCandidate()
}

func (l *Actor) handleHeartbeatTimeout(msg msgHeartbeatTimeout) error {
	if msg.Gen != l.heartbeatGen {
		return nil
	}
	l.heartbeatArmed = false
	l.dropGhosts()

	if l.isLeader == false {
		return nil
	}

	l.sendHeartbeat()

	// Leadership needs continuing evidence of reaching a quorum: every other exit from
	// leadership requires an inbound message, and an isolated node receives none. The
	// evidence is transport-level - an accepted send or any inbound message within one
	// election timeout - so it catches partition; a wedged peer is caught by its
	// monitor instead.
	if reachable := l.quorumContact(); reachable < l.quorum() {
		l.Log().Warning("lost quorum contact: %d of %d reachable - stepping down",
			reachable, l.quorum())
		return l.becomeFollower(gen.PID{})
	}

	l.resetHeartbeatTimer()
	return nil
}

// quorumContact counts the members reachable within one election timeout, including
// this node.
func (l *Actor) quorumContact() int {
	window := time.Duration(l.electionTimeoutMax) * time.Millisecond
	reachable := 1
	for node := range l.declared {
		if at, ok := l.lastContact[node]; ok && time.Since(at) < window {
			reachable++
		}
	}
	return reachable
}

// sendPeer sends a protocol message and records the outcome, so a peer that
// silently swallows everything becomes a counter plus one log line per state
// change, rather than twenty discarded errors a second.
func (l *Actor) sendPeer(pid gen.PID, message any) {
	err := l.Send(pid, message)
	_, wasFailing := l.sendFailing[pid]

	if err == nil {
		l.lastContact[pid.Node] = time.Now()
	}

	if err != nil {
		l.sendFailures[pid]++
		if wasFailing == false {
			l.sendFailing[pid] = struct{}{}
			l.Log().Warning("protocol send to peer %s is failing: %s (failures %d)",
				pid, err, l.sendFailures[pid])
		}
		return
	}

	if wasFailing == true {
		delete(l.sendFailing, pid)
		l.Log().Info("protocol send to peer %s recovered after %d failures",
			pid, l.sendFailures[pid])
	}
}

func (l *Actor) sendHeartbeat() {
	hb := msgHeartbeat{
		ClusterID: l.clusterID,
		Term:      l.term,
		Leader:    l.PID(),
	}

	l.sendDeclared(hb, false)
	l.lastHeartbeatOut = time.Now()
}

func (l *Actor) randomElectionTimeout() time.Duration {
	diff := l.electionTimeoutMax - l.electionTimeoutMin
	if diff <= 0 {
		// should never happen due to validation, but be defensive
		return time.Duration(l.electionTimeoutMin) * time.Millisecond
	}
	timeout := l.electionTimeoutMin + rand.Intn(diff)
	return time.Duration(timeout) * time.Millisecond
}

func (l *Actor) resetElectionTimer() {
	l.armElectionTimerAfter(l.randomElectionTimeout())
}

func (l *Actor) armElectionTimerAfter(after time.Duration) {
	l.cancelElectionTimer()
	l.electionGen++
	timer, err := l.SendAfter(l.PID(), msgElectionTimeout{Gen: l.electionGen}, after)
	if err != nil {
		// A failed arm is a node that will never campaign again. It must not be
		// silent, and the caller cannot do anything about it, so say so here.
		l.Log().Error("could not arm election timer: %s - this node can no longer start an election", err)
		return
	}
	l.electionTimer = timer
	l.electionArmed = true
}

func (l *Actor) cancelElectionTimer() {
	l.electionArmed = false
	if l.electionTimer != nil {
		l.electionTimer()
		l.electionTimer = nil
	}
}

func (l *Actor) resetHeartbeatTimer() {
	l.cancelHeartbeatTimer()
	timeout := time.Duration(l.heartbeatInterval) * time.Millisecond
	l.heartbeatGen++
	timer, err := l.SendAfter(l.PID(), msgHeartbeatTimeout{Gen: l.heartbeatGen}, timeout)
	if err != nil {
		l.Log().Error("could not arm heartbeat timer: %s - stepping down rather than "+
			"holding leadership without heartbeats", err)
		l.becomeFollower(gen.PID{})
		return
	}
	l.heartbeatTimer = timer
	l.heartbeatArmed = true
}

func (l *Actor) cancelHeartbeatTimer() {
	l.heartbeatArmed = false
	if l.heartbeatTimer != nil {
		l.heartbeatTimer()
		l.heartbeatTimer = nil
	}
}

// setTerm updates the term. There is deliberately no callback: a change that does not
// move leadership means nothing to a consumer, one that does arrives as
// HandleBecomeFollower, and churn is term / term_changed_at in Inspect.
func (l *Actor) setTerm(newTerm uint64) {
	if newTerm == l.term {
		return
	}

	l.term = newTerm
	l.termChangedAt = time.Now()

	// A vote belongs to a term, so a new term frees it - and only a new term does.
	// Clearing votedFor on any step-down let one node grant two votes in one term.
	l.votedFor = gen.PID{}
	l.votesReceived = nil
}

//
// default callbacks
//

func (l *Actor) HandleMessage(from gen.PID, message any) error {
	l.Log().Warning("Leader.HandleMessage: unhandled message from %s: %T", from, message)
	return nil
}

func (l *Actor) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	l.Log().Warning("Leader.HandleCall: unhandled request from %s: %T", from, request)
	return nil, nil
}

func (l *Actor) Terminate(reason error) {}

// HandleInspect reports the full election state. Passing item names returns only those
// keys; pass "help" for the key list.
//
// The election state uses the reserved "ergo:" prefix for its keys. ProcessRun computes
// it first and merges the behavior's map on top, so a consumer adds fields of its own and
// can override a reserved key only by naming it deliberately.
func (l *Actor) HandleInspect(from gen.PID, item ...string) map[string]string {
	return l.inspect(item...)
}

func (l *Actor) inspect(item ...string) map[string]string {
	peers := make([]string, 0, len(l.peers))
	for pid := range l.peers {
		peers = append(peers, pid.String())
	}
	sort.Strings(peers)

	declared := make([]string, 0, len(l.declared))
	for node, d := range l.declared {
		state := "pending"
		if d.pid != (gen.PID{}) {
			state = d.pid.String()
		}
		declared = append(declared, fmt.Sprintf("%s=%s", string(node), state))
	}
	sort.Strings(declared)

	votes := make([]string, 0, len(l.votesReceived))
	for pid, granted := range l.votesReceived {
		if granted == true {
			votes = append(votes, pid.String())
		}
	}
	sort.Strings(votes)

	unreachable := make([]string, 0, len(l.unreachableSince))
	for node, since := range l.unreachableSince {
		unreachable = append(unreachable, fmt.Sprintf("%s=%s", string(node),
			time.Since(since).Round(time.Millisecond)))
	}
	sort.Strings(unreachable)

	failing := make([]string, 0, len(l.sendFailing))
	for pid := range l.sendFailing {
		failing = append(failing, fmt.Sprintf("%s=%d", pid, l.sendFailures[pid]))
	}
	sort.Strings(failing)

	dropped := make([]string, 0, len(l.dropped))
	for reason, n := range l.dropped {
		dropped = append(dropped, fmt.Sprintf("%s=%d", reason, n))
	}
	sort.Strings(dropped)

	// Derived, not stored: becomeCandidate is the only writer of votedFor == self.
	state := "follower"
	if l.votedFor == l.PID() {
		state = "candidate"
	}
	if l.isLeader == true {
		state = "leader"
	}
	if l.clustered() == false {
		state = "unclustered"
	}

	all := map[string]string{
		"ergo:cluster": l.clusterID,
		"ergo:state":   state,
		"ergo:term":    fmt.Sprintf("%d", l.term),
		"ergo:leader":  fmt.Sprintf("%v", l.isLeader),

		"ergo:leader_pid":  l.leader.String(),
		"ergo:leader_node": string(l.leader.Node),
		"ergo:voted_for":   l.votedFor.String(),

		// peers stays the count it has always been; the identities that were
		// missing arrive as an additional key rather than by redefining this one.
		"ergo:view_size":        fmt.Sprintf("%d", l.viewSize()),
		"ergo:min_cluster_size": fmt.Sprintf("%d", l.minClusterSize),
		"ergo:ghost_ttl":        l.ghostTTL.String(),
		"ergo:unreachable":      strings.Join(unreachable, ","),
		"ergo:peers":            fmt.Sprintf("%d", len(l.peers)),
		"ergo:peers_list":       strings.Join(peers, ","),
		"ergo:quorum":           fmt.Sprintf("%d", l.quorum()),
		"ergo:declared":         strings.Join(declared, ","),
		"ergo:bootstrap":        fmt.Sprintf("%d", len(l.bootstrap)),

		"ergo:votes_granted": strings.Join(votes, ","),
		"ergo:votes_count":   fmt.Sprintf("%d", len(votes)),

		// Whether a timer is armed is the difference between a follower waiting to
		// campaign and one that has stopped electing altogether.
		"ergo:election_timer_armed":  fmt.Sprintf("%v", l.electionArmed),
		"ergo:heartbeat_timer_armed": fmt.Sprintf("%v", l.heartbeatArmed),
		"ergo:election_timeout_min":  fmt.Sprintf("%dms", l.electionTimeoutMin),
		"ergo:election_timeout_max":  fmt.Sprintf("%dms", l.electionTimeoutMax),
		"ergo:heartbeat_interval":    fmt.Sprintf("%dms", l.heartbeatInterval),

		"ergo:term_changed_at":    l.termChangedAt.Format(time.RFC3339Nano),
		"ergo:heartbeat_in_last":  inspectStamp(l.lastHeartbeatIn),
		"ergo:heartbeat_out_last": inspectStamp(l.lastHeartbeatOut),
		"ergo:confirm_denied":     fmt.Sprintf("%d", l.confirmDenied),
		"ergo:last_denied_at":     inspectStamp(l.lastDeniedAt),
		"ergo:send_failing_peers": strings.Join(failing, ","),
		"ergo:dropped_by_reason":  strings.Join(dropped, ","),
	}

	if len(item) == 0 {
		return all
	}

	selected := make(map[string]string, len(item))
	for _, name := range item {
		if name == "help" {
			keys := make([]string, 0, len(all))
			for k := range all {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			selected["ergo:help"] = strings.Join(keys, ",")
			continue
		}
		value, ok := all[name]
		if ok == false {
			selected[name] = "<unknown item>"
			continue
		}
		selected[name] = value
	}
	return selected
}

func inspectStamp(at time.Time) string {
	if at.IsZero() {
		return "never"
	}
	return at.Format(time.RFC3339Nano)
}

// HandleConfirmLeader defaults to granting leadership: an election win is
// sufficient unless the behavior says otherwise.
func (l *Actor) HandleConfirmLeader() (bool, error) { return true, nil }

// SetTrapExit enables the trap on exit requests sent by SendExit. With the trap set,
// such a request from anyone but the parent arrives as a regular gen.MessageExitPID
// message instead of terminating this process.
func (l *Actor) SetTrapExit(trap bool) { l.trap = trap }

// TrapExit reports whether the trap is enabled.
func (l *Actor) TrapExit() bool { return l.trap }

func (l *Actor) sendSpanProcessed(message *gen.MailboxMessage, kind gen.TracingKind, errStr string) {
	var msgType string
	if message.Message != nil {
		msgType = reflect.TypeOf(message.Message).String()
	}
	l.SendTracingSpan(gen.TracingSpan{
		TraceID:      message.Tracing.ID,
		SpanID:       message.Tracing.SpanID,
		Point:        gen.TracingPointProcessed,
		Kind:         kind,
		Timestamp:    l.spanStart,
		EndTimestamp: time.Now().UnixNano(),
		Node:         l.Node().Name(),
		From:         message.From,
		To:           l.PID(),
		Ref:          message.Ref,
		Behavior:     l.BehaviorName(),
		Message:      msgType,
		Error:        errStr,
		Attributes:   l.TracingAttributes(),
	})
	l.CloseTracingSpans()
	l.ClearTracingSpanAttributes()
}

// Defaults so that embedding leader.Actor satisfies ActorBehavior. They warn rather
// than stay silent: winning leadership and doing nothing with it is a missing
// implementation, not an intention.
func (l *Actor) HandleBecomeLeader() error {
	l.Log().Warning("Actor.HandleBecomeLeader: became leader but the behavior does not implement it")
	return nil
}

func (l *Actor) HandleBecomeFollower(leader gen.PID) error {
	l.Log().Warning("Actor.HandleBecomeFollower: became follower of %s but the behavior does not implement it", leader)
	return nil
}

func (l *Actor) HandleEvent(event gen.MessageEvent) error {
	l.Log().Warning("Actor.HandleEvent: unhandled event %#v", event)
	return nil
}

func (l *Actor) HandleSpan(span gen.TracingSpan) error {
	l.Log().Warning("Actor.HandleSpan: unhandled span %#v", span)
	return nil
}

func (l *Actor) HandleLog(message gen.MessageLog) error {
	l.Log().Warning("Actor.HandleLog: unhandled log message %#v", message)
	return nil
}

func (l *Actor) HandlePeerJoined(peer gen.PID) error { return nil }
func (l *Actor) HandlePeerLeft(peer gen.PID) error   { return nil }
