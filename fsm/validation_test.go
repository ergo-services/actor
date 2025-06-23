package fsm

import (
	"testing"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/unit"
)

// Test FSM with valid handlers
type ValidFSM struct {
	Actor
}

func (v *ValidFSM) Init(args ...any) (gen.Atom, error) {
	// Use new simplified API - transitions are built automatically inside Actor
	v.AddState("state1", v.HandleState1, "state2")
	v.AddState("state2", v.HandleState2, "state1")

	return "state1", nil
}

func (v *ValidFSM) HandleState1(from gen.PID, message any) (gen.Atom, error) {
	switch message {
	case "go_to_state2":
		return "state2", nil
	default:
		return "state1", nil
	}
}

func (v *ValidFSM) HandleState2(from gen.PID, message any) (gen.Atom, error) {
	switch message {
	case "go_to_state1":
		return "state1", nil
	default:
		return "state2", nil
	}
}

// Test FSM missing handler for initial state
type MissingInitialHandlerFSM struct {
	Actor
}

func (m *MissingInitialHandlerFSM) Init(args ...any) (gen.Atom, error) {
	// Only define handler for state2, but initial state is state1
	m.AddState("state2", m.HandleState2)

	return "state1", nil // state1 has no handler!
}

func (m *MissingInitialHandlerFSM) HandleState2(from gen.PID, message any) (gen.Atom, error) {
	return "state2", nil
}

// Test FSM with missing handlers in transition map
type MissingTransitionHandlerFSM struct {
	Actor
}

func (m *MissingTransitionHandlerFSM) Init(args ...any) (gen.Atom, error) {
	// Define handler for state1
	m.AddState("state1", m.HandleState1, "state2", "state3")

	// state2 and state3 referenced in transitions but have no handlers
	// This should generate warnings but not fail

	return "state1", nil
}

func (m *MissingTransitionHandlerFSM) HandleState1(from gen.PID, message any) (gen.Atom, error) {
	return "state1", nil
}

// Test FSM with invalid transition attempt
type InvalidTransitionFSM struct {
	Actor
}

func (i *InvalidTransitionFSM) Init(args ...any) (gen.Atom, error) {
	// Define states with restricted transitions
	i.AddState("locked", i.HandleLocked, "unlocked")   // locked can only go to unlocked
	i.AddState("unlocked", i.HandleUnlocked, "locked") // unlocked can only go to locked
	// No direct transition from locked to error allowed
	i.AddState("error", i.HandleError, "locked")

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

func TestValidHandlers(t *testing.T) {
	// This should succeed - all handlers belong to ValidFSM
	fsm, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &ValidFSM{}
	})

	if err != nil {
		t.Fatalf("Expected valid FSM to start successfully, got error: %v", err)
	}

	behavior := fsm.Behavior().(*ValidFSM)
	if behavior.CurrentState() != "state1" {
		t.Errorf("Expected initial state to be 'state1', got %s", behavior.CurrentState())
	}

	// Test state transitions work
	fsm.SendMessage(fsm.PID(), "go_to_state2")
	if behavior.CurrentState() != "state2" {
		t.Errorf("Expected state to be 'state2' after transition, got %s", behavior.CurrentState())
	}

	fsm.SendMessage(fsm.PID(), "go_to_state1")
	if behavior.CurrentState() != "state1" {
		t.Errorf("Expected state to be 'state1' after transition, got %s", behavior.CurrentState())
	}
}

func TestMissingInitialHandler(t *testing.T) {
	// This should fail - initial state has no handler
	_, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &MissingInitialHandlerFSM{}
	})

	if err == nil {
		t.Fatal("Expected FSM with missing initial handler to fail during initialization")
	}

	// Should contain error about missing handler for initial state
	t.Logf("Got expected error: %v", err)
}

func TestMissingTransitionHandlers(t *testing.T) {
	// This should succeed but generate warnings
	fsm, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &MissingTransitionHandlerFSM{}
	})

	if err != nil {
		t.Fatalf("Expected FSM with missing transition handlers to start (with warnings), got error: %v", err)
	}

	behavior := fsm.Behavior().(*MissingTransitionHandlerFSM)
	if behavior.CurrentState() != "state1" {
		t.Errorf("Expected initial state to be 'state1', got %s", behavior.CurrentState())
	}

	// The warnings should be logged but FSM should still work for defined states
}

func TestInvalidTransition(t *testing.T) {
	// This should succeed during initialization
	fsm, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &InvalidTransitionFSM{}
	})

	if err != nil {
		t.Fatalf("Expected FSM to start successfully, got error: %v", err)
	}

	behavior := fsm.Behavior().(*InvalidTransitionFSM)
	if behavior.CurrentState() != "locked" {
		t.Errorf("Expected initial state to be 'locked', got %s", behavior.CurrentState())
	}

	// Test valid transition
	fsm.SendMessage(fsm.PID(), "unlock")
	if behavior.CurrentState() != "unlocked" {
		t.Errorf("Expected state to be 'unlocked' after valid transition, got %s", behavior.CurrentState())
	}

	// Go back to locked
	fsm.SendMessage(fsm.PID(), "lock")
	if behavior.CurrentState() != "locked" {
		t.Errorf("Expected state to be 'locked' after lock, got %s", behavior.CurrentState())
	}

	// Test invalid transition - this should be rejected at runtime
	// The handler will try to transition from "locked" to "error" but it's not allowed
	fsm.SendMessage(fsm.PID(), "force_error")

	// State should remain "locked" because the transition was invalid
	if behavior.CurrentState() != "locked" {
		t.Errorf("Expected state to remain 'locked' after invalid transition attempt, got %s", behavior.CurrentState())
	}
}

func TestCompileTimeSafety(t *testing.T) {
	// This test demonstrates compile-time safety
	// The following code would not compile:

	/*
		// This would be a compile-time error:
		type WrongSignature struct {
			Actor
		}

		func (w *WrongSignature) BadHandler(message any) gen.Atom {  // Wrong signature!
			return "state"
		}

		func (w *WrongSignature) Init(args ...any) (gen.Atom, error) {
			// This line would cause a compile-time error because BadHandler doesn't match StateTransition signature
			w.AddState("state", w.BadHandler)  // Compile error!
			return "state", nil
		}
	*/

	// This test passes just by compiling successfully
	t.Log("Compile-time safety test passed - code compiles without reflection")
}

// ExampleFSM demonstrates compile-time safe handler validation
type ExampleFSM struct {
	Actor
}

const (
	StateA gen.Atom = "a"
	StateB gen.Atom = "b"
)

func FakeHandler1(from gen.PID, message any) (gen.Atom, error) {
	return StateA, nil
}

// Init demonstrates proper handler setup with compile-time validation
func (e *ExampleFSM) Init(args ...any) (gen.Atom, error) {
	// ✅ THESE COMPILE - Correct signatures matching StateTransition
	e.AddState(StateA, e.HandleA, StateB)
	e.AddState(StateB, e.HandleB, StateA)
	e.AddState(StateB, FakeHandler1, StateA)

	// ❌ THESE WOULD NOT COMPILE - Wrong signatures
	// Uncomment any of these to see compilation errors:

	// e.AddState(StateA, e.WrongSignature1, StateB)
	// compiler error: cannot use e.WrongSignature1 (type func(string) error)
	// as type StateTransition in argument to AddState

	// e.AddState(StateA, e.WrongSignature2, StateB)
	// compiler error: cannot use e.WrongSignature2 (type func(gen.PID, any) string)
	// as type StateTransition in argument to AddState

	// e.AddState(StateA, e.WrongSignature3, StateB)
	// compiler error: cannot use e.WrongSignature3 (type func(gen.PID) (gen.Atom, error))
	// as type StateTransition in argument to AddState

	return StateA, nil
}

// ✅ CORRECT - Matches StateTransition signature exactly
func (e *ExampleFSM) HandleA(from gen.PID, message any) (gen.Atom, error) {
	return StateB, nil
}

// ✅ CORRECT - Matches StateTransition signature exactly
func (e *ExampleFSM) HandleB(from gen.PID, message any) (gen.Atom, error) {
	return StateA, nil
}

// ❌ WRONG SIGNATURES - These would cause compilation errors if used in AddState

// Wrong parameter types
func (e *ExampleFSM) WrongSignature1(message string) error {
	return nil
}

// Wrong return types
func (e *ExampleFSM) WrongSignature2(from gen.PID, message any) string {
	return "invalid"
}

// Missing parameter
func (e *ExampleFSM) WrongSignature3(from gen.PID) (gen.Atom, error) {
	return StateA, nil
}

// Extra parameter
func (e *ExampleFSM) WrongSignature4(from gen.PID, message any, extra int) (gen.Atom, error) {
	return StateA, nil
}

// TestHandlerCompileTimeSafety tests that valid handlers work correctly
func TestHandlerCompileTimeSafety(t *testing.T) {
	// Spawn the FSM actor
	actor, err := unit.Spawn(t, func() gen.ProcessBehavior {
		return &ExampleFSM{}
	})
	if err != nil {
		t.Fatal(err)
	}

	behavior := actor.Behavior().(*ExampleFSM)
	actor.ClearEvents()

	// Test initial state
	unit.Equal(t, StateA, behavior.CurrentState(), "Should start in StateA")

	// Test state transition
	actor.SendMessage(gen.PID{}, "test message")
	unit.Equal(t, StateB, behavior.CurrentState(), "Should transition to StateB")

	// Test transition back
	actor.SendMessage(gen.PID{}, "another message")
	unit.Equal(t, StateA, behavior.CurrentState(), "Should transition back to StateA")
}
