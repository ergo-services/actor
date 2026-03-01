package health

import (
	"fmt"
	"time"

	"ergo.services/ergo/gen"
)

// Register sends a RegisterRequest to the health actor (sync Call).
// The calling process will be monitored by the health actor. If it terminates,
// the signal is automatically marked as down.
func Register(process gen.Process, to any, signal gen.Atom, probe Probe, timeout time.Duration) error {
	result, err := process.Call(to, RegisterRequest{
		Signal:  signal,
		Probe:   probe,
		Timeout: timeout,
	})
	if err != nil {
		return err
	}
	resp, ok := result.(RegisterResponse)
	if ok == false {
		return fmt.Errorf("unexpected response type: %T", result)
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// Unregister sends an UnregisterRequest to the health actor (sync Call).
func Unregister(process gen.Process, to any, signal gen.Atom) error {
	result, err := process.Call(to, UnregisterRequest{Signal: signal})
	if err != nil {
		return err
	}
	resp, ok := result.(UnregisterResponse)
	if ok == false {
		return fmt.Errorf("unexpected response type: %T", result)
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
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
