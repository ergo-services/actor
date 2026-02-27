package health

import (
	"time"

	"ergo.services/ergo/gen"
)

// Register sends a MessageRegister to the health actor. The calling process
// will be monitored by the health actor. If it terminates, the signal is
// automatically marked as down.
func Register(process gen.Process, to any, signal gen.Atom, probe Probe, timeout time.Duration) error {
	return process.Send(to, MessageRegister{
		Signal:  signal,
		Probe:   probe,
		Timeout: timeout,
	})
}

// Unregister sends a MessageUnregister to the health actor.
func Unregister(process gen.Process, to any, signal gen.Atom) error {
	return process.Send(to, MessageUnregister{Signal: signal})
}

// Heartbeat sends a MessageHeartbeat to the health actor.
func Heartbeat(process gen.Process, to any, signal gen.Atom) error {
	return process.Send(to, MessageHeartbeat{Signal: signal})
}

// SignalUp sends a MessageSignalUp to the health actor.
func SignalUp(process gen.Process, to any, signal gen.Atom) error {
	return process.Send(to, MessageSignalUp{Signal: signal})
}

// SignalDown sends a MessageSignalDown to the health actor.
func SignalDown(process gen.Process, to any, signal gen.Atom) error {
	return process.Send(to, MessageSignalDown{Signal: signal})
}
