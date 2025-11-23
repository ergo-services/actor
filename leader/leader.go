package leader

import (
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
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

	HandleBecomeLeader()
	HandleBecomeFollower(leader gen.PID)
}

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
	term               uint64
	votedFor           gen.PID
	isLeader           bool
	leader             gen.PID
	peers              map[gen.PID]bool
	votesReceived      map[gen.PID]bool

	electionTimer  gen.CancelFunc
	heartbeatTimer gen.CancelFunc
}

// Options for leader election
type Options struct {
	ClusterID string
	Bootstrap []gen.ProcessID

	ElectionTimeoutMin int // Minimum election timeout (ms, default: 150)
	ElectionTimeoutMax int // Maximum election timeout (ms, default: 300)
	HeartbeatInterval  int // Heartbeat interval (ms, default: 50)
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

type msgElectionTimeout struct{}
type msgHeartbeatTimeout struct{}

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

	l.electionTimeoutMin = opts.ElectionTimeoutMin
	if l.electionTimeoutMin <= 0 {
		l.electionTimeoutMin = 150
	}

	l.electionTimeoutMax = opts.ElectionTimeoutMax
	if l.electionTimeoutMax <= 0 {
		l.electionTimeoutMax = 300
	}

	if l.electionTimeoutMax <= l.electionTimeoutMin {
		return fmt.Errorf("ElectionTimeoutMax (%d) must be greater than ElectionTimeoutMin (%d)",
			l.electionTimeoutMax, l.electionTimeoutMin)
	}

	l.heartbeatInterval = opts.HeartbeatInterval
	if l.heartbeatInterval <= 0 {
		l.heartbeatInterval = 50
	}

	if l.heartbeatInterval >= l.electionTimeoutMin {
		l.Log().Warning("HeartbeatInterval (%dms) >= ElectionTimeoutMin (%dms) - elections may be unstable",
			l.heartbeatInterval, l.electionTimeoutMin)
	}

	l.peers = make(map[gen.PID]bool)

	l.resetElectionTimer()

	// Call initial follower callback
	l.behavior.HandleBecomeFollower(gen.PID{})

	return nil
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

			return nil
		}

		switch message.Type {
		case gen.MailboxMessageTypeRegular:
			if reason := l.handleMessage(message.From, message.Message); reason != nil {
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

		case gen.MailboxMessageTypeExit:
			switch exit := message.Message.(type) {
			case gen.MessageExitPID:
				return fmt.Errorf("%s: %w", exit.PID, exit.Reason)
			case gen.MessageExitProcessID:
				return fmt.Errorf("%s: %w", exit.ProcessID, exit.Reason)
			case gen.MessageExitAlias:
				return fmt.Errorf("%s: %w", exit.Alias, exit.Reason)
			case gen.MessageExitEvent:
				return fmt.Errorf("%s: %w", exit.Event, exit.Reason)
			case gen.MessageExitNode:
				return fmt.Errorf("%s: %w", exit.Name, gen.ErrNoConnection)
			default:
				panic(fmt.Sprintf("unknown exit: %#v", exit))
			}

		case gen.MailboxMessageTypeInspect:
			result := l.behavior.HandleInspect(message.From, message.Message.([]string)...)
			l.SendResponse(message.From, message.Ref, result)
		}
	}
}

func (l *Actor) ProcessTerminate(reason error) {
	l.cancelElectionTimer()
	l.cancelHeartbeatTimer()
	l.behavior.Terminate(reason)
}

func (l *Actor) handleMessage(from gen.PID, message any) error {
	switch msg := message.(type) {
	case gen.MessageDownPID:
		delete(l.peers, msg.PID)
		if l.leader == msg.PID {
			l.Log().Info("leader down")
			l.becomeFollower()
		}

	case msgVote:
		if msg.ClusterID == l.clusterID {
			l.discoverPeer(from)
			return l.handleVote(from, msg)
		}

	case msgVoteReply:
		if msg.ClusterID == l.clusterID {
			l.discoverPeer(from)
			return l.handleVoteReply(from, msg)
		}

	case msgHeartbeat:
		if msg.ClusterID == l.clusterID {
			l.discoverPeer(from) // only discover if ClusterID matches
			return l.handleHeartbeat(from, msg)
		}

	case msgElectionTimeout:
		return l.handleElectionTimeout()

	case msgHeartbeatTimeout:
		return l.handleHeartbeatTimeout()

	default:
		return l.behavior.HandleMessage(from, message)
	}

	return nil
}

func (l *Actor) discoverPeer(pid gen.PID) {
	if pid == l.PID() {
		return
	}

	if _, exists := l.peers[pid]; exists {
		return
	}

	l.peers[pid] = true
	l.Monitor(pid)
}

// Default implementations
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
	return map[string]string{
		"cluster": l.clusterID,
		"term":    fmt.Sprintf("%d", l.term),
		"leader":  fmt.Sprintf("%v", l.isLeader),
		"peers":   fmt.Sprintf("%d", len(l.peers)),
	}
}

// IsLeader returns true if this node is the leader
func (l *Actor) IsLeader() bool {
	return l.isLeader
}

// Leader returns the current leader PID (empty if no leader)
func (l *Actor) Leader() gen.PID {
	return l.leader
}

// Join adds a peer to the cluster
// Use this to manually add known peers or for dynamic cluster growth
func (l *Actor) Join(peer gen.ProcessID) {
	l.Send(peer, msgVote{
		ClusterID: l.clusterID,
		Term:      l.term,
		Candidate: l.PID(),
	})
}

// election logic

func (l *Actor) becomeFollower() {
	wasLeader := l.isLeader
	l.isLeader = false
	l.votedFor = gen.PID{}
	l.leader = gen.PID{}
	l.votesReceived = nil

	if wasLeader {
		l.behavior.HandleBecomeFollower(gen.PID{})
	}

	l.cancelHeartbeatTimer()
	l.resetElectionTimer()
}

func (l *Actor) becomeCandidate() {
	l.term++
	l.votedFor = l.PID()
	l.isLeader = false
	l.votesReceived = make(map[gen.PID]bool)

	votes := 1 // vote for self
	quorum := len(l.peers)/2 + 1

	l.Log().Info("election: term=%d peers=%d quorum=%d", l.term, len(l.peers), quorum)

	vote := msgVote{
		ClusterID: l.clusterID,
		Term:      l.term,
		Candidate: l.PID(),
	}

	for pid := range l.peers {
		l.Send(pid, vote)
	}

	for _, b := range l.bootstrap {
		if b.Name == l.Name() && b.Node == l.Node().Name() {
			continue
		}
		l.Send(b, vote)
	}

	// Only become leader immediately if we're the only node (no peers)
	if votes >= quorum {
		l.becomeLeader()
	}
}

func (l *Actor) becomeLeader() {
	l.isLeader = true
	l.leader = l.PID()

	l.cancelElectionTimer()
	l.resetHeartbeatTimer()

	l.Log().Info("became leader: term=%d", l.term)
	l.behavior.HandleBecomeLeader()

	l.sendHeartbeat()
}

func (l *Actor) handleVote(from gen.PID, msg msgVote) error {
	if msg.Term < l.term {
		l.Send(from, msgVoteReply{Term: l.term, Granted: false})
		return nil
	}

	if msg.Term > l.term {
		l.term = msg.Term
		l.becomeFollower()
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
		l.term = msg.Term
		l.becomeFollower()
		return nil
	}

	// Only process vote replies if we're still a candidate for this term
	if msg.Term == l.term && msg.Granted && l.isLeader == false && l.votedFor == l.PID() {
		// Track this vote
		l.votesReceived[from] = true

		// Count actual votes received
		votes := 1 // vote for self
		for _, granted := range l.votesReceived {
			if granted {
				votes++
			}
		}

		quorum := len(l.peers)/2 + 1
		if votes >= quorum {
			l.becomeLeader()
		}
	}

	return nil
}

func (l *Actor) handleHeartbeat(from gen.PID, msg msgHeartbeat) error {
	if msg.Term < l.term {
		return nil
	}

	if msg.Term > l.term {
		l.term = msg.Term
		l.becomeFollower()
	}

	// If we receive heartbeat in same term while being leader
	// This indicates split-brain - step down to be safe
	if msg.Term == l.term && l.isLeader {
		l.Log().Warning("received heartbeat from %s claiming leadership in same term %d - stepping down", from, msg.Term)
		l.becomeFollower()
	}

	if l.leader != msg.Leader {
		l.leader = msg.Leader
		l.behavior.HandleBecomeFollower(l.leader)
	}

	l.resetElectionTimer()
	return nil
}

func (l *Actor) handleElectionTimeout() error {
	if l.isLeader == false {
		l.becomeCandidate()
	}
	return nil
}

func (l *Actor) handleHeartbeatTimeout() error {
	if l.isLeader {
		l.sendHeartbeat()
		l.resetHeartbeatTimer()
	}
	return nil
}

func (l *Actor) sendHeartbeat() {
	hb := msgHeartbeat{
		ClusterID: l.clusterID,
		Term:      l.term,
		Leader:    l.PID(),
	}

	for pid := range l.peers {
		l.Send(pid, hb)
	}

	for _, b := range l.bootstrap {
		if b.Name == l.Name() && b.Node == l.Node().Name() {
			continue
		}
		l.Send(b, hb)
	}
}

func (l *Actor) randomElectionTimeout() time.Duration {
	diff := l.electionTimeoutMax - l.electionTimeoutMin
	if diff <= 0 {
		// Should never happen due to validation, but be defensive
		return time.Duration(l.electionTimeoutMin) * time.Millisecond
	}
	timeout := l.electionTimeoutMin + rand.Intn(diff)
	return time.Duration(timeout) * time.Millisecond
}

func (l *Actor) resetElectionTimer() {
	l.cancelElectionTimer()
	l.electionTimer, _ = l.SendAfter(l.PID(), msgElectionTimeout{}, l.randomElectionTimeout())
}

func (l *Actor) cancelElectionTimer() {
	if l.electionTimer != nil {
		l.electionTimer()
		l.electionTimer = nil
	}
}

func (l *Actor) resetHeartbeatTimer() {
	l.cancelHeartbeatTimer()
	timeout := time.Duration(l.heartbeatInterval) * time.Millisecond
	l.heartbeatTimer, _ = l.SendAfter(l.PID(), msgHeartbeatTimeout{}, timeout)
}

func (l *Actor) cancelHeartbeatTimer() {
	if l.heartbeatTimer != nil {
		l.heartbeatTimer()
		l.heartbeatTimer = nil
	}
}
