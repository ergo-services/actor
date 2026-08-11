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

// RegisterRequest registers a signal with the health actor (sync Call).
// The registering process will be monitored. If the process terminates,
// the signal is marked as down automatically.
type RegisterRequest struct {
	Signal  gen.Atom
	Probe   Probe         // bitmask, default ProbeLiveness if 0
	Timeout time.Duration // heartbeat timeout, 0 = no heartbeat needed
}

// RegisterResponse is the response to RegisterRequest.
type RegisterResponse struct {
	Error string
}

// UnregisterRequest removes a signal from the health actor (sync Call).
type UnregisterRequest struct {
	Signal gen.Atom
}

// UnregisterResponse is the response to UnregisterRequest.
type UnregisterResponse struct {
	Error string
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

// NetworkTypes returns the wire types this actor exchanges with the processes it serves.
//
// Registering them is the caller's job, not the library's, and it must happen before the
// node carries any traffic. Type registration is node-scoped, so it cannot be done from a
// package init(); doing it in the actor's own Init would be too late for a node whose
// health process starts after a connection is already established.
//
// Declare them on the application that hosts the actor, which is processed during
// ApplicationLoad before any of its processes are spawned:
//
//	gen.ApplicationSpec{
//	    Network: gen.ApplicationNetwork{
//	        RegisterTypes:  health.NetworkTypes(),
//	        RegisterErrors: health.ErrorTypes(),
//	    },
//	}
//
// or register them on the node directly before it starts serving:
//
//	node.Network().RegisterTypes(health.NetworkTypes())
//
// Init refuses to start if the node does not know them: a remote Register or Heartbeat
// that cannot be decoded is a signal silently missing from the probe answer, which is
// worse than not starting.
func NetworkTypes() []any {
	return []any{
		Probe(0),
		time.Duration(0),
		RegisterRequest{},
		RegisterResponse{},
		UnregisterRequest{},
		UnregisterResponse{},
		MessageHeartbeat{},
		MessageSignalUp{},
		MessageSignalDown{},
	}
}

// ErrorTypes returns the sentinel errors this actor sends over the wire. There are none
// today - failures ride as strings on the responses - so this exists to keep consumer
// setup uniform and keep working if that changes.
func ErrorTypes() []error {
	return nil
}
