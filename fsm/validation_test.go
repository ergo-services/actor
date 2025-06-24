package fsm

import (
	"testing"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/unit"
)

// Test message types
type StartMsg struct{}
type StopMsg struct{}
type PauseMsg struct{}
type ResumeMsg struct{}
type ErrorMsg struct {
	Code    int
	Message string
}

// Test states
const (
	StateIdle    gen.Atom = "idle"
	StateRunning gen.Atom = "running"
	StatePaused  gen.Atom = "paused"
	StateStopped gen.Atom = "stopped"
	StateError   gen.Atom = "error"
)

// Valid FSM implementation
type TestFSM struct {
	Actor
	data map[string]any
}

func (t *TestFSM) Init(args ...any) (gen.Atom, error) {
	t.data = make(map[string]any)

	// Add states with proper error handling
	if err := t.AddState(StateIdle, t.HandleIdle, StateRunning, StateError); err != nil {
		return "", err
	}
	if err := t.AddState(StateRunning, t.HandleRunning, StatePaused, StateStopped, StateError); err != nil {
		return "", err
	}
	if err := t.AddState(StatePaused, t.HandlePaused, StateRunning, StateStopped, StateError); err != nil {
		return "", err
	}
	if err := t.AddState(StateStopped, t.HandleStopped, StateRunning, StateIdle); err != nil {
		return "", err
	}
	if err := t.AddState(StateError, t.HandleError, StateIdle); err != nil {
		return "", err
	}

	return StateIdle, nil
}

func (t *TestFSM) HandleIdle(from gen.PID, message any) (gen.Atom, error) {
	switch message.(type) {
	case StartMsg:
		return StateRunning, nil
	case ErrorMsg:
		return StateError, nil
	default:
		return StateIdle, nil
	}
}

func (t *TestFSM) HandleRunning(from gen.PID, message any) (gen.Atom, error) {
	switch message.(type) {
	case PauseMsg:
		return StatePaused, nil
	case StopMsg:
		return StateStopped, nil
	case ErrorMsg:
		return StateError, nil
	default:
		return StateRunning, nil
	}
}

func (t *TestFSM) HandlePaused(from gen.PID, message any) (gen.Atom, error) {
	switch message.(type) {
	case ResumeMsg:
		return StateRunning, nil
	case StopMsg:
		return StateStopped, nil
	case ErrorMsg:
		return StateError, nil
	default:
		return StatePaused, nil
	}
}

func (t *TestFSM) HandleStopped(from gen.PID, message any) (gen.Atom, error) {
	switch message.(type) {
	case StartMsg:
		return StateRunning, nil
	default:
		return StateStopped, nil
	}
}

func (t *TestFSM) HandleError(from gen.PID, message any) (gen.Atom, error) {
	switch message.(type) {
	case StartMsg:
		return StateIdle, nil
	default:
		return StateError, nil
	}
}

// Optional high priority handlers
func (t *TestFSM) HandleMessage(from gen.PID, message any) error {
	t.data["last_high_priority_message"] = message
	return nil
}

func (t *TestFSM) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	return map[string]any{
		"state":         string(t.CurrentState()),
		"high_priority": true,
	}, nil
}

func (t *TestFSM) Terminate(reason error) {
	t.data = nil
}

// Test basic FSM functionality
func TestFSMBasicTransitions(t *testing.T) {
	fsm, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &TestFSM{}
	})
	if err != nil {
		t.Fatal(err)
	}

	behavior := fsm.Behavior().(*TestFSM)
	fsm.ClearEvents()

	// Test initial state
	unit.Equal(t, StateIdle, behavior.CurrentState(), "Should start in idle state")

	// Test state transitions
	fsm.SendMessage(gen.PID{}, StartMsg{})
	unit.Equal(t, StateRunning, behavior.CurrentState(), "Should transition to running")

	fsm.SendMessage(gen.PID{}, PauseMsg{})
	unit.Equal(t, StatePaused, behavior.CurrentState(), "Should transition to paused")

	fsm.SendMessage(gen.PID{}, ResumeMsg{})
	unit.Equal(t, StateRunning, behavior.CurrentState(), "Should transition back to running")

	fsm.SendMessage(gen.PID{}, StopMsg{})
	unit.Equal(t, StateStopped, behavior.CurrentState(), "Should transition to stopped")

	fsm.SendMessage(gen.PID{}, StartMsg{})
	unit.Equal(t, StateRunning, behavior.CurrentState(), "Should transition to running from stopped")

	// Test error state
	fsm.SendMessage(gen.PID{}, ErrorMsg{Code: 500, Message: "Test error"})
	unit.Equal(t, StateError, behavior.CurrentState(), "Should transition to error state")

	fsm.SendMessage(gen.PID{}, StartMsg{})
	unit.Equal(t, StateIdle, behavior.CurrentState(), "Should recover to idle state")
}

// Test dynamic state management
func TestDynamicStateManagement(t *testing.T) {
	fsm, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &TestFSM{}
	})
	if err != nil {
		t.Fatal(err)
	}

	behavior := fsm.Behavior().(*TestFSM)
	fsm.ClearEvents()

	// Test listing states
	states := behavior.ListStates()
	expectedStates := 5 // idle, running, paused, stopped, error
	unit.Equal(t, expectedStates, len(states), "Should have 5 initial states")

	// Test listing transitions from idle
	unit.Equal(t, StateIdle, behavior.CurrentState(), "Should be in idle state")
	transitions := behavior.ListTransitions()
	unit.Equal(t, 2, len(transitions), "Should have 2 transitions from idle")

	// Test duplicate state detection
	err = behavior.AddState(StateIdle, behavior.HandleIdle)
	if err == nil {
		t.Error("Expected error when adding duplicate state")
	}

	// Test removing non-existent state
	err = behavior.RemoveState("nonexistent")
	if err == nil {
		t.Error("Expected error when removing non-existent state")
	}

	// Test removing current state
	err = behavior.RemoveState(StateIdle)
	if err == nil {
		t.Error("Expected error when removing current state")
	}
}

// Test FSM with missing initial handler
type MissingInitialHandlerFSM struct {
	Actor
}

func (m *MissingInitialHandlerFSM) Init(args ...any) (gen.Atom, error) {
	// Only define handler for running state, but initial state is idle
	if err := m.AddState(StateRunning, m.HandleRunning); err != nil {
		return "", err
	}
	return StateIdle, nil // StateIdle has no handler!
}

func (m *MissingInitialHandlerFSM) HandleRunning(from gen.PID, message any) (gen.Atom, error) {
	return StateRunning, nil
}

func TestMissingInitialHandler(t *testing.T) {
	_, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &MissingInitialHandlerFSM{}
	})

	if err == nil {
		t.Fatal("Expected FSM with missing initial handler to fail during initialization")
	}

	t.Logf("Got expected error: %v", err)
}

// Test FSM with invalid transition attempt
type InvalidTransitionFSM struct {
	Actor
}

func (i *InvalidTransitionFSM) Init(args ...any) (gen.Atom, error) {
	// Define states with restricted transitions
	if err := i.AddState("locked", i.HandleLocked, "unlocked"); err != nil {
		return "", err
	}
	if err := i.AddState("unlocked", i.HandleUnlocked, "locked"); err != nil {
		return "", err
	}
	// No direct transition from locked to error allowed
	if err := i.AddState("error", i.HandleError, "locked"); err != nil {
		return "", err
	}

	return "locked", nil
}

func (i *InvalidTransitionFSM) HandleLocked(from gen.PID, message any) (gen.Atom, error) {
	switch message {
	case "unlock":
		return "unlocked", nil
	case "force_error":
		// This should fail because locked -> error is not in transition map
		return "error", nil
	default:
		return "locked", nil
	}
}

func (i *InvalidTransitionFSM) HandleUnlocked(from gen.PID, message any) (gen.Atom, error) {
	switch message {
	case "lock":
		return "locked", nil
	default:
		return "unlocked", nil
	}
}

func (i *InvalidTransitionFSM) HandleError(from gen.PID, message any) (gen.Atom, error) {
	switch message {
	case "reset":
		return "locked", nil
	default:
		return "error", nil
	}
}

func TestInvalidTransition(t *testing.T) {
	fsm, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &InvalidTransitionFSM{}
	})
	if err != nil {
		t.Fatalf("Expected FSM to start successfully, got error: %v", err)
	}

	behavior := fsm.Behavior().(*InvalidTransitionFSM)
	fsm.ClearEvents()

	unit.Equal(t, gen.Atom("locked"), behavior.CurrentState(), "Should start in locked state")

	// Test valid transition
	fsm.SendMessage(fsm.PID(), "unlock")
	unit.Equal(t, gen.Atom("unlocked"), behavior.CurrentState(), "Should transition to unlocked")

	// Go back to locked
	fsm.SendMessage(fsm.PID(), "lock")
	unit.Equal(t, gen.Atom("locked"), behavior.CurrentState(), "Should transition back to locked")

	// Test invalid transition - this should be rejected but logged
	fsm.SendMessage(fsm.PID(), "force_error")
	// State should remain "locked" because the transition was invalid
	unit.Equal(t, gen.Atom("locked"), behavior.CurrentState(), "Should remain locked after invalid transition")
}

// Test high priority message handling
func TestHighPriorityMessages(t *testing.T) {
	fsm, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &TestFSM{}
	})
	if err != nil {
		t.Fatal(err)
	}

	behavior := fsm.Behavior().(*TestFSM)
	fsm.ClearEvents()

	// Test that priority methods are available
	err = fsm.Process().SendWithPriority(gen.PID{}, "priority_test", gen.MessagePriorityHigh)
	if err != nil {
		t.Fatal("SendWithPriority should not return error:", err)
	}

	result, err := fsm.Process().CallWithPriority(gen.PID{}, "priority_call", gen.MessagePriorityHigh)
	if err != nil {
		t.Fatal("CallWithPriority should not return error:", err)
	}
	t.Logf("Priority call result: %v", result)

	// Test high priority handlers directly (since unit test framework may not fully simulate priority routing)
	err = behavior.HandleMessage(gen.PID{}, "direct_test_message")
	if err != nil {
		t.Error("HandleMessage should not return error for test message")
	}

	// Verify data was stored
	if lastMsg, exists := behavior.data["last_high_priority_message"]; !exists || lastMsg != "direct_test_message" {
		t.Error("HandleMessage did not store message correctly")
	}

	// Test HandleCall directly
	callResult, err := behavior.HandleCall(gen.PID{}, gen.Ref{}, "direct_test_call")
	if err != nil {
		t.Fatal(err)
	}

	// Verify response
	response, ok := callResult.(map[string]any)
	if !ok {
		t.Fatal("Expected map response from HandleCall")
	}

	if response["high_priority"] != true {
		t.Error("HandleCall response missing high_priority flag")
	}

	if response["state"] != string(behavior.CurrentState()) {
		t.Error("HandleCall response has incorrect state")
	}

	// Test that normal priority messages still go through FSM state handlers
	fsm.SendMessage(gen.PID{}, StartMsg{})
	unit.Equal(t, StateRunning, behavior.CurrentState(), "Normal priority message should trigger state transition")

	t.Log("High priority message handling test completed successfully")
}
