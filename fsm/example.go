package fsm

import (
	"fmt"
	"time"

	"ergo.services/ergo/gen"
)

// Example messages - various types including structs
type StartMsg struct{}
type StopMsg struct{}
type PauseMsg struct{}
type ResumeMsg struct{}
type TickMsg struct{}

// Complex message structures
type ConfigureMsg struct {
	TickInterval time.Duration
	MaxTicks     int
	LogLevel     string
}

type ProcessDataMsg struct {
	ID       string
	Data     []byte
	Priority int
	Callback gen.PID
}

type StatusRequest struct {
	RequestID string
	Details   bool
}

type ErrorMsg struct {
	Code    int
	Message string
	Source  string
}

// Example states
const (
	StateIdle    gen.Atom = "idle"
	StateRunning gen.Atom = "running"
	StatePaused  gen.Atom = "paused"
	StateStopped gen.Atom = "stopped"
	StateError   gen.Atom = "error"
)

// MyFSM demonstrates the FSM pattern using the new my.AddState() approach
// This provides ownership validation through API design without reflection
type MyFSM struct {
	Actor

	data       map[string]any
	tickCancel gen.CancelFunc // To cancel the periodic tick
}

// Init implements the fsm.Behavior interface using the simplified API design
func (my *MyFSM) Init(args ...any) (gen.Atom, error) {
	my.data = make(map[string]any)
	my.data["start_time"] = time.Now()
	my.data["tick_count"] = 0
	my.data["tick_interval"] = 1 * time.Second
	my.data["max_ticks"] = 10
	my.data["log_level"] = "info"

	// ✅ SIMPLIFIED API: Use my.AddState() for ownership validation through design
	// The transitions are built inside the Actor automatically
	// The 'my' receiver ensures handlers belong to this FSM instance
	my.AddState(StateIdle, my.HandleIdle, StateRunning, StateError)
	my.AddState(StateRunning, my.HandleRunning, StatePaused, StateStopped, StateError)
	my.AddState(StatePaused, my.HandlePaused, StateRunning, StateStopped, StateError)
	my.AddState(StateStopped, my.HandleStopped, StateRunning, StateIdle)
	my.AddState(StateError, my.HandleError, StateIdle)

	// What this pattern prevents (these would be obvious violations in code review):
	// my.AddState(StateIdle, FakeHandler, StateRunning)        // ❌ No ownership relation
	// my.AddState(StateIdle, otherFSM.HandleIdle, StateRunning) // ❌ Wrong instance

	// Just return the initial state - transitions are already built inside
	return StateIdle, nil
}

// HandleIdle handles messages when in the idle state
func (my *MyFSM) HandleIdle(from gen.PID, message any) (gen.Atom, error) {
	switch msg := message.(type) {
	case StartMsg:
		my.data["start_time"] = time.Now()
		my.data["tick_count"] = 0

		// Start periodic ticking using SendAfter - proper actor pattern
		my.scheduleTick()

		return StateRunning, nil

	case ConfigureMsg:
		my.data["tick_interval"] = msg.TickInterval
		my.data["max_ticks"] = msg.MaxTicks
		my.data["log_level"] = msg.LogLevel

		// Send response to from if valid, otherwise to self
		target := from
		if target == (gen.PID{}) {
			target = my.PID()
		}
		my.Send(target, map[string]any{
			"status": "configured",
			"state":  "idle",
		})

		// Stay in idle state after configuration
		return StateIdle, nil

	case StatusRequest:
		response := map[string]any{
			"request_id": msg.RequestID,
			"state":      "idle",
			"start_time": my.data["start_time"],
			"tick_count": my.data["tick_count"],
		}

		if msg.Details {
			response["tick_interval"] = my.data["tick_interval"]
			response["max_ticks"] = my.data["max_ticks"]
			response["log_level"] = my.data["log_level"]
		}

		// Send response to from if valid, otherwise to self
		target := from
		if target == (gen.PID{}) {
			target = my.PID()
		}
		my.Send(target, response)
		return StateIdle, nil

	case ErrorMsg:
		my.data["error"] = msg
		return StateError, nil

	default:
		return StateIdle, nil
	}
}

// HandleRunning handles messages when in the running state
func (my *MyFSM) HandleRunning(from gen.PID, message any) (gen.Atom, error) {
	switch msg := message.(type) {
	case TickMsg:
		count := my.data["tick_count"].(int)
		count++
		my.data["tick_count"] = count

		maxTicks := my.data["max_ticks"].(int)
		if count >= maxTicks {
			my.cancelTick()
			return StateStopped, nil
		}

		// Schedule next tick - proper actor pattern
		my.scheduleTick()
		return StateRunning, nil

	case ProcessDataMsg:
		// Simulate data processing
		result := map[string]any{
			"id":        msg.ID,
			"processed": true,
			"timestamp": time.Now(),
			"size":      len(msg.Data),
		}

		// Send result back to callback if provided
		if msg.Callback != (gen.PID{}) {
			my.Send(msg.Callback, result)
		}

		return StateRunning, nil

	case PauseMsg:
		my.cancelTick() // Stop ticking when paused
		return StatePaused, nil

	case StopMsg:
		my.cancelTick() // Stop ticking when stopped
		return StateStopped, nil

	case StatusRequest:
		response := map[string]any{
			"request_id": msg.RequestID,
			"state":      "running",
			"start_time": my.data["start_time"],
			"tick_count": my.data["tick_count"],
		}

		if msg.Details {
			response["tick_interval"] = my.data["tick_interval"]
			response["max_ticks"] = my.data["max_ticks"]
			response["uptime"] = time.Since(my.data["start_time"].(time.Time))
		}

		// Send response to from if valid, otherwise to self
		target := from
		if target == (gen.PID{}) {
			target = my.PID()
		}
		my.Send(target, response)
		return StateRunning, nil

	case ErrorMsg:
		my.cancelTick() // Stop ticking on error
		my.data["error"] = msg
		return StateError, nil

	default:
		return StateRunning, nil
	}
}

// HandlePaused handles messages when in the paused state
func (my *MyFSM) HandlePaused(from gen.PID, message any) (gen.Atom, error) {
	switch msg := message.(type) {
	case ResumeMsg:
		// Restart ticking when resuming
		my.scheduleTick()
		return StateRunning, nil

	case StopMsg:
		return StateStopped, nil

	case StatusRequest:
		response := map[string]any{
			"request_id": msg.RequestID,
			"state":      "paused",
			"start_time": my.data["start_time"],
			"tick_count": my.data["tick_count"],
		}

		if msg.Details {
			response["paused_duration"] = time.Since(my.data["start_time"].(time.Time))
		}

		// Send response to from if valid, otherwise to self
		target := from
		if target == (gen.PID{}) {
			target = my.PID()
		}
		my.Send(target, response)
		return StatePaused, nil

	case ProcessDataMsg:
		// In paused state, we can still process urgent data
		if msg.Priority > 5 {
			result := map[string]any{
				"id":        msg.ID,
				"processed": true,
				"state":     "paused",
				"urgent":    true,
			}

			if msg.Callback != (gen.PID{}) {
				my.Send(msg.Callback, result)
			}
		}

		return StatePaused, nil

	case TickMsg:
		// Ignore tick messages while paused (shouldn't happen since we cancel them)
		return StatePaused, nil

	case ErrorMsg:
		my.data["error"] = msg
		return StateError, nil

	default:
		return StatePaused, nil
	}
}

// HandleStopped handles messages when in the stopped state
func (my *MyFSM) HandleStopped(from gen.PID, message any) (gen.Atom, error) {
	switch msg := message.(type) {
	case StartMsg:
		my.data["start_time"] = time.Now()
		my.data["tick_count"] = 0

		// Start ticking again
		my.scheduleTick()

		return StateRunning, nil

	case ConfigureMsg:
		my.data["tick_interval"] = msg.TickInterval
		my.data["max_ticks"] = msg.MaxTicks
		my.data["log_level"] = msg.LogLevel

		// Transition to idle after reconfiguration
		return StateIdle, nil

	case StatusRequest:
		response := map[string]any{
			"request_id": msg.RequestID,
			"state":      "stopped",
			"start_time": my.data["start_time"],
			"tick_count": my.data["tick_count"],
		}

		// Send response to from if valid, otherwise to self
		target := from
		if target == (gen.PID{}) {
			target = my.PID()
		}
		my.Send(target, response)
		return StateStopped, nil

	case TickMsg, ProcessDataMsg:
		// Ignore processing messages while stopped
		return StateStopped, nil

	default:
		return StateStopped, nil
	}
}

// HandleError handles messages when in the error state
func (my *MyFSM) HandleError(from gen.PID, message any) (gen.Atom, error) {
	switch msg := message.(type) {
	case StartMsg:
		// Clear error and reset to idle
		delete(my.data, "error")
		return StateIdle, nil

	case StatusRequest:
		response := map[string]any{
			"request_id": msg.RequestID,
			"state":      "error",
		}

		if errorInfo, exists := my.data["error"]; exists {
			response["error"] = errorInfo
		}

		// Send response to from if valid, otherwise to self
		target := from
		if target == (gen.PID{}) {
			target = my.PID()
		}
		my.Send(target, response)
		return StateError, nil

	default:
		return StateError, nil
	}
}

// scheduleTick schedules the next tick using SendAfter - proper actor pattern
// This avoids the issue of running additional goroutines within the actor
func (my *MyFSM) scheduleTick() {
	interval := my.data["tick_interval"].(time.Duration)
	cancel, err := my.SendAfter(my.PID(), TickMsg{}, interval)
	if err != nil {
		// Handle error appropriately
		return
	}

	// Cancel any previous tick
	my.cancelTick()
	my.tickCancel = cancel
}

// cancelTick cancels the scheduled tick
func (my *MyFSM) cancelTick() {
	if my.tickCancel != nil {
		my.tickCancel()
		my.tickCancel = nil
	}
}

// HandleCall implements fsm.Behavior for synchronous requests
func (my *MyFSM) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	switch req := request.(type) {
	case StatusRequest:
		return map[string]any{
			"request_id": req.RequestID,
			"state":      string(my.CurrentState()),
			"start_time": my.data["start_time"],
			"tick_count": my.data["tick_count"],
		}, nil

	default:
		return nil, fmt.Errorf("unhandled call request %T", req)
	}
}

// HandleInspect implements fsm.Behavior
func (my *MyFSM) HandleInspect(from gen.PID, item ...string) map[string]string {
	result := my.Actor.HandleInspect(from, item...)
	result["tick_count"] = fmt.Sprintf("%v", my.data["tick_count"])
	result["tick_interval"] = fmt.Sprintf("%v", my.data["tick_interval"])
	result["max_ticks"] = fmt.Sprintf("%v", my.data["max_ticks"])
	result["has_active_tick"] = fmt.Sprintf("%t", my.tickCancel != nil)

	if startTime, ok := my.data["start_time"].(time.Time); ok {
		result["start_time"] = startTime.Format(time.RFC3339)
		result["uptime"] = time.Since(startTime).String()
	}

	if errorInfo, exists := my.data["error"]; exists {
		if err, ok := errorInfo.(ErrorMsg); ok {
			result["error_code"] = fmt.Sprintf("%d", err.Code)
			result["error_message"] = err.Message
		}
	}

	return result
}

// Terminate implements fsm.Behavior
func (my *MyFSM) Terminate(reason error) {
	// Cancel any active tick
	my.cancelTick()

	// Cleanup data
	my.data = nil

	// Call parent's terminate
	my.Actor.Terminate(reason)
}

// FakeHandler demonstrates a standalone function that would be caught by the API design
func FakeHandler(from gen.PID, message any) (gen.Atom, error) {
	// This function has the correct signature but doesn't belong to any FSM instance
	return StateIdle, nil
}

// BadFSMExample shows what ownership violations look like (for documentation)
type BadFSMExample struct {
	Actor
}

func (bad *BadFSMExample) BadInit(args ...any) (gen.Atom, error) {
	// ❌ These patterns would be obvious violations in code review:

	// Using standalone function - no ownership relation
	// bad.AddState(StateIdle, FakeHandler, StateRunning)

	// Using handler from different instance - wrong owner
	// var other MyFSM
	// bad.AddState(StateIdle, other.HandleIdle, StateRunning)

	// ✅ CORRECT: Handlers belong to the same instance as the caller
	bad.AddState(StateIdle, bad.HandleSomething, StateRunning)

	return StateIdle, nil
}

func (bad *BadFSMExample) HandleSomething(from gen.PID, message any) (gen.Atom, error) {
	return StateRunning, nil
}

// GoodFSMExample shows the correct pattern
type GoodFSMExample struct {
	Actor
}

func (good *GoodFSMExample) Init(args ...any) (gen.Atom, error) {
	// ✅ CORRECT: All handlers called with 'good' receiver belong to 'good' instance
	good.AddState(StateIdle, good.HandleIdle, StateRunning)
	good.AddState(StateRunning, good.HandleRunning, StateIdle)

	// The pattern 'good.AddState(..., good.HandleXXX, ...)' makes ownership obvious
	// Any deviation from this pattern is immediately visible in code review

	return StateIdle, nil
}

func (good *GoodFSMExample) HandleIdle(from gen.PID, message any) (gen.Atom, error) {
	return StateRunning, nil
}

func (good *GoodFSMExample) HandleRunning(from gen.PID, message any) (gen.Atom, error) {
	return StateIdle, nil
}
