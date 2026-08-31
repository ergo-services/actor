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

	HandleConfirmLeader() (bool, error)
	HandleBecomeLeader() error
	HandleBecomeFollower(leader gen.PID) error

	HandlePeerJoined(peer gen.PID) error
	HandlePeerLeft(peer gen.PID) error

	HandleEvent(event gen.MessageEvent) error
	HandleSpan(span gen.TracingSpan) error
	HandleLog(message gen.MessageLog) error
}

const processKindCandidate gen.ProcessKind = "candidate"

const docsURL = "https://docs.ergo.services/extra-library/actors/leader"

type Actor struct {
	gen.Process

	behavior ActorBehavior
	mailbox  gen.ProcessMailbox

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

	electionArmed  bool
	heartbeatArmed bool
	electionGen    uint64
	heartbeatGen   uint64

	reported gen.PID

	trap      bool
	spanStart int64

	unreachableSince map[gen.Atom]time.Time

	withdrawn map[gen.Atom]time.Time

	lastContact map[gen.Atom]time.Time

	termChangedAt    time.Time
	lastHeartbeatIn  time.Time
	lastHeartbeatOut time.Time
	declared         map[gen.Atom]declaredPeer
	confirmDenied    uint64
	lastDeniedAt     time.Time
	unclusteredAt    time.Time
	unclusteredView  int
	sendFailures     map[gen.PID]uint64
	sendFailing      map[gen.PID]struct{}
	dropped          map[string]uint64
}

type declaredPeer struct {
	target gen.ProcessID
	pid    gen.PID
}

// Options configure the election. The behavior returns them from its Init, and every
// zero value takes the default named below.
type Options struct {
	// ClusterID names the election this actor takes part in. Required - an empty one
	// fails Init - and peers configured with different values ignore each other, which
	// is how two clusters share a node without interfering.
	ClusterID string

	// Bootstrap declares peers up front, seeding the same membership Join does. Either
	// is fine and both may be used; discovery itself is the consumer's job.
	Bootstrap []gen.ProcessID

	// ElectionTimeoutMin and ElectionTimeoutMax bound the randomised wait before a
	// follower campaigns (ms, defaults 150 and 300). The randomisation is what keeps
	// two followers from campaigning in lockstep, so the gap between them matters.
	// Setting one alone derives the other: Max becomes Min*2, Min becomes Max/2.
	ElectionTimeoutMin int
	ElectionTimeoutMax int

	// HeartbeatInterval is how often a leader proves it is still there (ms, default 50).
	// It has to stay well below ElectionTimeoutMin or followers time out and campaign
	// against a healthy leader; a value above it is warned about at startup.
	HeartbeatInterval int

	// GhostTTL is how long an unreachable peer stays in the view before it is dropped
	// (ms, default: 5000). It buys a blip the benefit of the doubt without letting dead
	// pod names accumulate: with dynamic names, keeping them forever grows the quorum
	// past the number of nodes that exist. Not a safety control - MinClusterSize is.
	GhostTTL int

	// MinClusterSize is the smallest view - this node plus the peers it knows - that may
	// have a leader; below it the actor neither campaigns nor keeps leadership and
	// reports "unclustered". Not a quorum, which stays a majority of the current view.
	// Default 3. Lower values are warned about, not rejected: 1 lets a lone node appoint
	// itself, 2 tolerates no failure.
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

	if opts.ClusterID == "" {
		return fmt.Errorf("ClusterID cannot be empty")
	}

	l.clusterID = opts.ClusterID
	l.bootstrap = opts.Bootstrap

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

	for _, b := range l.bootstrap {
		if b.Node == l.Node().Name() && b.Name == l.Name() {
			continue
		}
		l.declared[b.Node] = declaredPeer{target: b}
	}
	l.termChangedAt = time.Now()

	if err := l.checkNetworkTypes(); err != nil {
		return err
	}

	l.resetElectionTimer()

	return nil
}

func (l *Actor) checkNetworkTypes() error {
	for _, v := range NetworkTypes() {
		t := reflect.TypeOf(v)
		got, ok := l.Node().Network().LookupType(fmt.Sprintf("#%s/%s", t.PkgPath(), t.Name()))
		if ok == true && got == t {
			continue
		}
		return fmt.Errorf("%s is not registered on this node: pass leader.NetworkTypes() to "+
			"ApplicationSpec.Network.RegisterTypes (or Network().RegisterTypes) before the node "+
			"serves traffic - see %s for how and when", t, docsURL)
	}
	return nil
}

func (l *Actor) viewSize() int {
	return len(l.declared) + 1
}

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

func (l *Actor) quorum() int {
	return l.viewSize()/2 + 1
}

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

func (l *Actor) clustered() bool {
	return l.viewSize() >= l.minClusterSize
}

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

	if at, ok := l.withdrawn[pid.Node]; ok {
		if time.Since(at) < time.Duration(l.electionTimeoutMax)*time.Millisecond {
			l.drop("withdrawn_peer", "ignoring %s: withdrawn by the consumer", pid)
			return nil
		}
		delete(l.withdrawn, pid.Node)
	}

	l.lastContact[pid.Node] = time.Now()

	if _, exists := l.peers[pid]; exists {
		return nil
	}

	l.peers[pid] = true
	d := l.declared[pid.Node]
	d.pid = pid
	if d.target == (gen.ProcessID{}) {
		d.target = gen.ProcessID{Name: l.Name(), Node: pid.Node}
	}
	l.declared[pid.Node] = d
	l.Monitor(pid)
	return l.behavior.HandlePeerJoined(pid)
}

func (l *Actor) IsLeader() bool {
	return l.isLeader
}

func (l *Actor) Leader() gen.PID {
	return l.leader
}

func (l *Actor) Term() uint64 {
	return l.term
}

func (l *Actor) Peers() []gen.PID {
	peers := make([]gen.PID, 0, len(l.peers))
	for pid := range l.peers {
		peers = append(peers, pid)
	}
	return peers
}

func (l *Actor) PeerCount() int {
	return len(l.peers)
}

func (l *Actor) ClusterID() string {
	return l.clusterID
}

func (l *Actor) Bootstrap() []gen.ProcessID {
	return l.bootstrap
}

func (l *Actor) HasPeer(pid gen.PID) bool {
	return l.peers[pid]
}

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

func (l *Actor) Join(peer gen.ProcessID) {
	if peer.Node == l.Node().Name() {
		return
	}

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
		l.Log().Warning("Join(%s): send failed: %s", peer, err)
	}
}

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

	votes := 1
	quorum := l.quorum()

	l.Log().Debug("election: term=%d view=%d quorum=%d", l.term, l.viewSize(), quorum)

	vote := msgVote{
		ClusterID: l.clusterID,
		Term:      l.term,
		Candidate: l.PID(),
	}

	l.sendDeclared(vote, true)

	if l.viewSize() == 1 && votes >= quorum {
		return l.becomeLeader()
	}

	l.resetElectionTimer()
	return nil
}

func (l *Actor) becomeLeader() error {
	confirmed, err := l.behavior.HandleConfirmLeader()
	if err != nil {
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
		if rollback := l.becomeFollower(gen.PID{}); rollback != nil {
			l.Log().Error("rollback after HandleBecomeLeader failed: %s", rollback)
		}
		return err
	}
	l.reported = l.PID()

	l.sendHeartbeat()
	return nil
}

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

	if _, member := l.declared[from.Node]; member == false {
		l.drop("vote_from_non_member",
			"ignoring grant from %s: not a member of this view", from)
		return nil
	}

	l.votesReceived[from] = true

	votes := 1
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
	if msg.Term < l.term {
		l.drop("stale_heartbeat",
			"dropping msgHeartbeat from %s: term %d is behind our term %d, sender still claims leadership",
			from, msg.Term, l.term)
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

	if reachable := l.quorumContact(); reachable < l.quorum() {
		l.Log().Warning("lost quorum contact: %d of %d reachable - stepping down",
			reachable, l.quorum())
		return l.becomeFollower(gen.PID{})
	}

	l.resetHeartbeatTimer()
	return nil
}

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

func (l *Actor) setTerm(newTerm uint64) {
	if newTerm == l.term {
		return
	}

	l.term = newTerm
	l.termChangedAt = time.Now()

	l.votedFor = gen.PID{}
	l.votesReceived = nil
}

func (l *Actor) HandleMessage(from gen.PID, message any) error {
	l.Log().Warning("Leader.HandleMessage: unhandled message from %s: %T", from, message)
	return nil
}

func (l *Actor) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	l.Log().Warning("Leader.HandleCall: unhandled request from %s: %T", from, request)
	return nil, nil
}

func (l *Actor) Terminate(reason error) {}

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

func (l *Actor) HandleConfirmLeader() (bool, error) { return true, nil }

func (l *Actor) SetTrapExit(trap bool) { l.trap = trap }

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
