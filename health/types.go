package health

import (
	"time"

	"ergo.services/ergo/gen"
)

// Probe represents the type of Kubernetes health probe.
// Values can be combined as a bitmask.
type Probe int

const (
	ProbeLiveness  Probe = 1 << iota // 1
	ProbeReadiness                   // 2
	ProbeStartup                     // 4
)

// MessageRegister registers a signal with the health actor.
// The registering process will be monitored. If the process terminates,
// the signal is marked as down automatically.
type MessageRegister struct {
	Signal  gen.Atom
	Probe   Probe         // bitmask, default ProbeLiveness if 0
	Timeout time.Duration // heartbeat timeout, 0 = no heartbeat needed
}

// MessageUnregister removes a signal from the health actor.
type MessageUnregister struct {
	Signal gen.Atom
}

// MessageHeartbeat updates the heartbeat timestamp for a signal.
// If the signal was down due to a missed heartbeat, it is marked as up
// and HandleSignalUp is called.
type MessageHeartbeat struct {
	Signal gen.Atom
}

// MessageSignalUp marks a signal as up (healthy).
type MessageSignalUp struct {
	Signal gen.Atom
}

// MessageSignalDown marks a signal as down (unhealthy).
type MessageSignalDown struct {
	Signal gen.Atom
}

// signalState holds the internal state for a registered signal.
type signalState struct {
	signal       gen.Atom
	probe        Probe
	up           bool
	timeout      time.Duration
	lastBeat     time.Time
	registeredBy gen.PID
}

// messageCheckTimeouts is an internal timer message for periodic heartbeat checking.
type messageCheckTimeouts struct{}
