package fsm

import (
	"testing"
	"time"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/unit"
)

func TestMyFSM_StateTransitions(t *testing.T) {
	// Spawn the FSM actor
	actor, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &MyFSM{}
	})
	if err != nil {
		t.Fatal(err)
	}

	behavior := actor.Behavior().(*MyFSM)
	actor.ClearEvents()

	// Test all valid state transitions
	unit.Equal(t, StateIdle, behavior.CurrentState(), "Should start in idle state")

	// Idle -> Running
	actor.SendMessage(gen.PID{}, StartMsg{})
	unit.Equal(t, StateRunning, behavior.CurrentState(), "Should transition to running")

	// Running -> Paused
	actor.SendMessage(gen.PID{}, PauseMsg{})
	unit.Equal(t, StatePaused, behavior.CurrentState(), "Should transition to paused")

	// Paused -> Running
	actor.SendMessage(gen.PID{}, ResumeMsg{})
	unit.Equal(t, StateRunning, behavior.CurrentState(), "Should transition back to running")

	// Running -> Stopped
	actor.SendMessage(gen.PID{}, StopMsg{})
	unit.Equal(t, StateStopped, behavior.CurrentState(), "Should transition to stopped")

	// Stopped -> Running
	actor.SendMessage(gen.PID{}, StartMsg{})
	unit.Equal(t, StateRunning, behavior.CurrentState(), "Should transition to running from stopped")

	// Any state -> Error
	actor.SendMessage(gen.PID{}, ErrorMsg{Code: 500, Message: "Test error", Source: "test"})
	unit.Equal(t, StateError, behavior.CurrentState(), "Should transition to error state")

	// Error -> Idle (recovery)
	actor.SendMessage(gen.PID{}, StartMsg{})
	unit.Equal(t, StateIdle, behavior.CurrentState(), "Should recover to idle state")
}

func TestMyFSM_TickingBehavior(t *testing.T) {
	// Spawn the FSM actor
	actor, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &MyFSM{}
	})
	if err != nil {
		t.Fatal(err)
	}

	behavior := actor.Behavior().(*MyFSM)
	actor.ClearEvents()

	// Configure for faster ticking and fewer max ticks
	configMsg := ConfigureMsg{
		TickInterval: 100 * time.Millisecond,
		MaxTicks:     2, // Only 2 ticks before stopping
		LogLevel:     "debug",
	}
	actor.SendMessage(gen.PID{}, configMsg)

	// Start FSM
	actor.SendMessage(gen.PID{}, StartMsg{})
	unit.Equal(t, StateRunning, behavior.CurrentState(), "FSM should be running")

	// Manually send tick messages to simulate the SendAfter behavior
	actor.SendMessage(gen.PID{}, TickMsg{}) // First tick
	unit.Equal(t, StateRunning, behavior.CurrentState(), "FSM should still be running after first tick")
	unit.Equal(t, 1, behavior.data["tick_count"].(int), "Tick count should be 1")

	actor.SendMessage(gen.PID{}, TickMsg{}) // Second tick - should stop
	unit.Equal(t, StateStopped, behavior.CurrentState(), "FSM should be stopped after max ticks")
	unit.Equal(t, 2, behavior.data["tick_count"].(int), "Tick count should be 2")

	// Test restart after stopping
	actor.SendMessage(gen.PID{}, StartMsg{})
	unit.Equal(t, StateRunning, behavior.CurrentState(), "FSM should be running after restart")
	unit.Equal(t, 0, behavior.data["tick_count"].(int), "Tick count should be reset to 0")

	// Send another tick to verify it's working
	actor.SendMessage(gen.PID{}, TickMsg{})
	unit.Equal(t, StateRunning, behavior.CurrentState(), "FSM should still be running")
	unit.Equal(t, 1, behavior.data["tick_count"].(int), "Tick count should be 1 after restart")
}

func TestMyFSM_TransitionValidation(t *testing.T) {
	// Spawn the FSM actor
	fsm, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &MyFSM{}
	})
	if err != nil {
		t.Fatal(err)
	}

	behavior := fsm.Behavior().(*MyFSM)

	// Test initial state and valid transitions
	if behavior.CurrentState() != StateIdle {
		t.Errorf("Expected current_state to be 'idle', got %s", behavior.CurrentState())
	}

	// From idle state, should only be able to transition to running or error
	validTransitions := behavior.GetValidTransitions()
	if len(validTransitions) != 2 {
		t.Errorf("Expected 2 valid transitions from idle, got %d", len(validTransitions))
	}

	// Check that running and error are in the valid transitions
	hasRunning := false
	hasError := false
	for _, state := range validTransitions {
		if state == StateRunning {
			hasRunning = true
		}
		if state == StateError {
			hasError = true
		}
	}
	if !hasRunning || !hasError {
		t.Errorf("Expected valid_transitions from idle to include both running and error, got %v", validTransitions)
	}

	// Transition to running state
	fsm.SendMessage(fsm.PID(), StartMsg{})

	// Check valid transitions from running state
	if behavior.CurrentState() != StateRunning {
		t.Errorf("Expected current_state to be 'running', got %s", behavior.CurrentState())
	}

	// From running state, should be able to transition to paused, stopped, or error
	validTransitions = behavior.GetValidTransitions()
	if len(validTransitions) != 3 {
		t.Errorf("Expected 3 valid transitions from running, got %d", len(validTransitions))
	}
}

func TestMyFSM_MessageHandling(t *testing.T) {
	// Spawn the FSM actor
	fsm, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &MyFSM{}
	})
	if err != nil {
		t.Fatal(err)
	}

	// Clear initialization events
	fsm.ClearEvents()

	// Send a status request message (not a call)
	fsm.SendMessage(fsm.PID(), StatusRequest{RequestID: "test-123", Details: false})

	// Verify the FSM sends back a status response
	fsm.ShouldSend().
		To(fsm.PID()).
		MessageMatching(func(msg any) bool {
			if statusMap, ok := msg.(map[string]any); ok {
				return statusMap["request_id"] == "test-123" && statusMap["state"] == "idle"
			}
			return false
		}).
		Once().
		Assert()

	// Start the FSM and verify tick messages are sent
	fsm.SendMessage(fsm.PID(), StartMsg{})

	// Wait a bit for the ticker to start
	time.Sleep(1500 * time.Millisecond)

	// Should receive at least one tick message
	fsm.ShouldSend().
		To(fsm.PID()).
		MessageMatching(func(msg any) bool {
			_, ok := msg.(TickMsg)
			return ok
		}).
		Once().
		Assert()
}

func TestMyFSM_ComplexMessages(t *testing.T) {
	// Spawn the FSM actor using the correct unit testing function
	actor, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &MyFSM{}
	})
	if err != nil {
		t.Fatal(err)
	}

	// Clear initialization events
	actor.ClearEvents()

	// Test configuration message
	configMsg := ConfigureMsg{
		TickInterval: 500 * time.Millisecond,
		MaxTicks:     3,
		LogLevel:     "debug",
	}
	actor.SendMessage(gen.PID{}, configMsg)

	// Should receive configuration response
	actor.ShouldSend().
		To(actor.PID()).
		MessageMatching(func(msg any) bool {
			if response, ok := msg.(map[string]any); ok {
				return response["status"] == "configured" && response["state"] == "idle"
			}
			return false
		}).
		Once().
		Assert()

	// Test status request
	statusReq := StatusRequest{
		RequestID: "test-123",
		Details:   true,
	}
	actor.SendMessage(gen.PID{}, statusReq)

	// Should receive status response
	actor.ShouldSend().
		To(actor.PID()).
		MessageMatching(func(msg any) bool {
			if response, ok := msg.(map[string]any); ok {
				return response["request_id"] == "test-123" && response["state"] == "idle"
			}
			return false
		}).
		Once().
		Assert()

	// Start the FSM
	actor.SendMessage(gen.PID{}, StartMsg{})

	// Wait for first tick to be scheduled and processed
	time.Sleep(600 * time.Millisecond)

	// Process any pending ticks by manually triggering message processing
	actor.SendMessage(gen.PID{}, TickMsg{})

	// Test process data message
	dataMsg := ProcessDataMsg{
		ID:       "data-001",
		Data:     []byte("test data"),
		Priority: 3,
		Callback: actor.PID(),
	}
	actor.SendMessage(gen.PID{}, dataMsg)

	// Should receive data processing result
	actor.ShouldSend().
		To(actor.PID()).
		MessageMatching(func(msg any) bool {
			if result, ok := msg.(map[string]any); ok {
				return result["id"] == "data-001" && result["processed"] == true && result["size"] == len(dataMsg.Data)
			}
			return false
		}).
		Once().
		Assert()

	// Test pause
	actor.SendMessage(gen.PID{}, PauseMsg{})

	// Send status request to verify paused state
	actor.SendMessage(gen.PID{}, StatusRequest{RequestID: "pause-check", Details: false})
	actor.ShouldSend().
		To(actor.PID()).
		MessageMatching(func(msg any) bool {
			if response, ok := msg.(map[string]any); ok {
				return response["state"] == "paused"
			}
			return false
		}).
		Once().
		Assert()

	// Test urgent data processing while paused
	urgentMsg := ProcessDataMsg{
		ID:       "urgent-001",
		Data:     []byte("urgent data"),
		Priority: 8, // High priority
		Callback: actor.PID(),
	}
	actor.SendMessage(gen.PID{}, urgentMsg)

	// Should receive urgent processing result
	actor.ShouldSend().
		To(actor.PID()).
		MessageMatching(func(msg any) bool {
			if result, ok := msg.(map[string]any); ok {
				return result["urgent"] == true && result["state"] == "paused"
			}
			return false
		}).
		Once().
		Assert()

	// Resume and test error handling
	actor.SendMessage(gen.PID{}, ResumeMsg{})

	// Send error message
	errorMsg := ErrorMsg{
		Code:    500,
		Message: "Test error",
		Source:  "test",
	}
	actor.SendMessage(gen.PID{}, errorMsg)

	// Check that FSM is in error state by inspecting behavior directly
	behavior := actor.Behavior().(*MyFSM)
	unit.Equal(t, StateError, behavior.CurrentState(), "FSM should be in error state")

	// Test recovery from error
	actor.SendMessage(gen.PID{}, StartMsg{})
	unit.Equal(t, StateIdle, behavior.CurrentState(), "FSM should recover to idle state")
}

func TestMyFSM_ErrorHandling(t *testing.T) {
	// Spawn the FSM actor
	actor, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &MyFSM{}
	})
	if err != nil {
		t.Fatal(err)
	}

	behavior := actor.Behavior().(*MyFSM)
	actor.ClearEvents()

	// Start FSM
	actor.SendMessage(gen.PID{}, StartMsg{})

	// Send error message to trigger error state
	errorMsg := ErrorMsg{
		Code:    404,
		Message: "Resource not found",
		Source:  "external_api",
	}
	actor.SendMessage(gen.PID{}, errorMsg)

	// Verify FSM is in error state
	unit.Equal(t, StateError, behavior.CurrentState(), "FSM should be in error state")

	// Check error details are stored
	if errorInfo, exists := behavior.data["error"]; !exists {
		t.Error("Expected error information to be stored")
	} else if errorDetails, ok := errorInfo.(ErrorMsg); ok {
		unit.Equal(t, 404, errorDetails.Code, "Error code should be 404")
		unit.Equal(t, "Resource not found", errorDetails.Message, "Error message should match")
	}

	// Test that other messages are ignored in error state by checking state doesn't change
	actor.SendMessage(gen.PID{}, ProcessDataMsg{ID: "ignored", Data: []byte("test")})
	actor.SendMessage(gen.PID{}, PauseMsg{})
	actor.SendMessage(gen.PID{}, ResumeMsg{})

	// Verify still in error state
	unit.Equal(t, StateError, behavior.CurrentState(), "FSM should remain in error state")

	// Test recovery from error state
	actor.SendMessage(gen.PID{}, StartMsg{})

	// Verify recovery to idle state
	unit.Equal(t, StateIdle, behavior.CurrentState(), "FSM should recover to idle state")

	// Error should be cleared
	if _, exists := behavior.data["error"]; exists {
		t.Error("Expected error to be cleared after recovery")
	}
}
