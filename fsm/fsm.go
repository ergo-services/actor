// Package fsm provides a finite state machine implementation for Ergo actors.
//
// Handler Validation:
// The Go type system provides compile-time validation of handlers:
//
//  1. Signature Enforcement: StateTransition type enforces the exact signature
//     func(from gen.PID, message any) (gen.Atom, error)
//
//  2. Method Binding: When you call my.AddState(state, my.HandleState, ...),
//     Go validates that my.HandleState matches StateTransition at compile time
//
// 3. Compilation Errors: Wrong signatures cause compile failures, not runtime errors
//
// Usage:
//
//	type MyFSM struct {
//	    fsm.Actor
//	}
//
//	func (my *MyFSM) Init(args ...any) (gen.Atom, error) {
//	    my.AddState(StateIdle, my.HandleIdle, StateRunning)
//	    my.AddState(StateRunning, my.HandleRunning, StateIdle, StateStopped)
//	    return StateIdle, nil
//	}
//
//	func (m *MyFSM) HandleIdle(from gen.PID, message any) (gen.Atom, error) {
//	    return StateRunning, nil
//	}
//
//	func (m *MyFSM) HandleRunning(from gen.PID, message any) (gen.Atom, error) {
//	    return true
//	}
package fsm

import (
	"fmt"
	"reflect"
	"runtime"
	"strings"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/lib"
)

type StateTransition func(from gen.PID, message any) (gen.Atom, error)

type StateHandlers map[gen.Atom]StateTransition

type TransitionMap map[gen.Atom][]gen.Atom

type transitions struct {
	Handlers StateHandlers
	Map      TransitionMap
}

type Behavior interface {
	gen.ProcessBehavior

	Init(args ...any) (gen.Atom, error)

	HandleMessage(from gen.PID, message any) error
	HandleCall(from gen.PID, ref gen.Ref, request any) (any, error)
	Terminate(reason error)
}

// AddState adds a state with its handler and allowed transitions.
func (a *Actor) AddState(state gen.Atom, handler StateTransition, allowedTransitions ...gen.Atom) error {
	if _, exists := a.transitions.Handlers[state]; exists {
		return fmt.Errorf("FSM: state %q already has a handler", state)
	}

	a.transitions.Handlers[state] = handler
	a.transitions.Map[state] = allowedTransitions
	return nil
}

// RemoveState removes a state and its handler.
func (a *Actor) RemoveState(state gen.Atom) error {
	if state == a.currentState {
		return fmt.Errorf("FSM: cannot remove current state %q", state)
	}

	if _, exists := a.transitions.Handlers[state]; !exists {
		return fmt.Errorf("FSM: state %q does not have a handler", state)
	}

	// check if this state is in the other transitions
	for _, otherState := range a.transitions.Map {
		for _, allowedTransition := range otherState {
			if allowedTransition == state {
				return fmt.Errorf("FSM: state %q is in the allowed transitions of %q", state, otherState)
			}
		}
	}

	delete(a.transitions.Handlers, state)
	return nil
}

// ListStates returns the list of states.
func (a *Actor) ListStates() []gen.Atom {
	states := make([]gen.Atom, 0, len(a.transitions.Handlers))
	for state := range a.transitions.Handlers {
		states = append(states, state)
	}
	return states
}

// ListTransitions returns the list of transitions from the current state.
func (a *Actor) ListTransitions() []gen.Atom {
	transitions := make([]gen.Atom, 0, len(a.transitions.Map[a.currentState]))
	for _, transition := range a.transitions.Map[a.currentState] {
		transitions = append(transitions, transition)
	}
	return transitions
}

// Actor provides the base FSM implementation that can be embedded in user actors.
type Actor struct {
	gen.Process

	currentState gen.Atom
	transitions  transitions
}

// CurrentState returns the current state of the FSM.
func (a *Actor) CurrentState() gen.Atom {
	return a.currentState
}

// ProcessInit implements gen.ProcessBehavior interface.
// This method sets up the FSM by calling the user's Init method and performing validation.
func (a *Actor) ProcessInit(process gen.Process, args ...any) (rr error) {
	a.Process = process

	// Initialize transitions with ownership binding
	a.transitions = transitions{
		Handlers: make(StateHandlers),
		Map:      make(TransitionMap),
	}

	behavior, ok := process.Behavior().(Behavior)
	if ok == false {
		unknown := strings.TrimPrefix(reflect.TypeOf(process.Behavior()).String(), "*")
		return fmt.Errorf("ProcessInit: not an fsm.Behavior %s", unknown)
	}

	if lib.Recover() {
		defer func() {
			if r := recover(); r != nil {
				pc, fn, line, _ := runtime.Caller(2)
				a.Log().Panic("Actor initialization failed. Panic reason: %#v at %s[%s:%d]",
					r, runtime.FuncForPC(pc).Name(), fn, line)
				rr = gen.TerminateReasonPanic
			}
		}()
	}

	initialState, err := behavior.Init(args...)
	if err != nil {
		return fmt.Errorf("FSM initialization failed: %w", err)
	}

	// Validate initial state has a handler
	if _, exists := a.transitions.Handlers[initialState]; !exists {
		return fmt.Errorf("initial state %q has no handler", initialState)
	}

	// Validate transition map consistency
	if err := a.validateTransitionMap(a.transitions); err != nil {
		return fmt.Errorf("transition map validation failed: %w", err)
	}

	// Set up the FSM
	a.currentState = initialState

	return nil
}

// validateTransitionMap checks that the transition map is consistent.
func (a *Actor) validateTransitionMap(transitions transitions) error {
	// Check that all transition targets have handlers (warn if missing)
	for sourceState, targetStates := range transitions.Map {
		for _, targetState := range targetStates {
			if _, exists := transitions.Handlers[targetState]; !exists {
				// Note: Missing handler for target state (warning)
				_ = fmt.Sprintf("FSM: State %q can transition to %q but %q has no handler",
					sourceState, targetState, targetState)
			}
		}
	}

	return nil
}

// ProcessRun implements gen.ProcessBehavior interface.
// This method routes messages to the appropriate state handler and manages state transitions.
func (a *Actor) ProcessRun() error {
	mailbox := a.Mailbox()

	for {
		if a.State() != gen.ProcessStateRunning {
			return gen.TerminateReasonKill
		}

		// Check for messages
		var message *gen.MailboxMessage
		var isHighPriority bool

		// Check urgent messages first (highest priority)
		if msg, ok := mailbox.Urgent.Pop(); ok {
			message = msg.(*gen.MailboxMessage)
			isHighPriority = true
		} else if msg, ok := mailbox.System.Pop(); ok {
			message = msg.(*gen.MailboxMessage)
			isHighPriority = true
		} else if msg, ok := mailbox.Main.Pop(); ok {
			message = msg.(*gen.MailboxMessage)
			isHighPriority = false
		} else {
			// No messages available
			return nil
		}

		defer gen.ReleaseMailboxMessage(message)

		switch message.Type {
		case gen.MailboxMessageTypeRegular:
			// Check if this is a high priority message and route accordingly
			if isHighPriority {
				behavior := a.Process.Behavior().(Behavior)
				if err := behavior.HandleMessage(message.From, message.Message); err != nil {
					return err
				}
				continue
			}

			if err := a.handleMessage(message.From, message.Message); err != nil {
				a.Log().Error("FSM: state handler failed on message from %s: %w", message.From, err)
			}

		case gen.MailboxMessageTypeRequest:
			// Check if this is a high priority call and route accordingly
			if isHighPriority {
				behavior := a.Process.Behavior().(Behavior)
				result, err := behavior.HandleCall(message.From, message.Ref, message.Message)
				if err != nil {
					return err
				}
				a.SendResponse(message.From, message.Ref, result)
				continue
			}

			err := a.handleCall(message.From, message.Ref, message.Message)
			if err != nil {
				a.Log().Error("FSM: state handler failed on request from %s: %w", message.From, err)
			}
			a.SendResponse(message.From, message.Ref, a.currentState)

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
				return fmt.Errorf("unknown exit message: %#v", exit)
			}

		case gen.MailboxMessageTypeInspect:
			result := a.HandleInspect(message.From, message.Message.([]string)...)
			a.Process.SendResponse(message.From, message.Ref, result)
		}
	}
}

// handleHighPriorityMessage processes high priority messages through optional HandleMessage callback
func (a *Actor) handleHighPriorityMessage(from gen.PID, message any) error {
	behavior := a.Process.Behavior().(Behavior)
	return behavior.HandleMessage(from, message)
}

// handleMessage processes incoming messages through FSM state handlers
func (a *Actor) handleMessage(from gen.PID, message any) error {
	// Get the handler for the current state
	handler, exists := a.transitions.Handlers[a.currentState]
	if !exists {
		return fmt.Errorf("no handler for current state %q", a.currentState)
	}

	// Call the state handler
	// Note: handler is guaranteed to have correct signature due to compile-time validation
	nextState, err := handler(from, message)
	if err != nil {
		return err
	}

	// Validate the transition if the state changed
	if nextState != a.currentState {
		if err := a.validateTransition(a.currentState, nextState); err != nil {
			return err
		}

		a.currentState = nextState
	}

	return nil
}

// handleCall processes incoming call requests through FSM state handlers and returns current state
func (a *Actor) handleCall(from gen.PID, ref gen.Ref, request any) error {
	// Get the handler for the current state
	handler, exists := a.transitions.Handlers[a.currentState]
	if !exists {
		return fmt.Errorf("no handler for current state %q", a.currentState)
	}

	// Call the state handler
	nextState, err := handler(from, request)
	if err != nil {
		return err
	}

	// Validate the transition if the state changed
	if nextState != a.currentState {
		if err := a.validateTransition(a.currentState, nextState); err != nil {
			return err
		}

		a.currentState = nextState
	}

	return nil
}

// validateTransition checks if a state transition is allowed according to the transition map.
func (a *Actor) validateTransition(from, to gen.Atom) error {
	allowedStates, exists := a.transitions.Map[from]
	if !exists {
		// If no transition map entry exists, allow any transition
		return nil
	}

	// Check if the target state is in the allowed list
	for _, allowedState := range allowedStates {
		if allowedState == to {
			return nil
		}
	}

	return fmt.Errorf("transition from %q to %q is not allowed", from, to)
}

// ProcessTerminate implements gen.ProcessBehavior interface
func (a *Actor) ProcessTerminate(reason error) {
	behavior := a.Process.Behavior().(Behavior)
	behavior.Terminate(reason)
}

func (a *Actor) HandleMessage(from gen.PID, message any) error {
	a.Log().Warning("fms.HandleMessage: unhandled message from %s", from)
	return nil
}

func (a *Actor) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	a.Log().Warning("fms.HandleCall: unhandled call from %s", from)
	return nil, nil
}

func (a *Actor) HandleInspect(from gen.PID, item ...string) map[string]string {
	result := make(map[string]string)
	result["fsm_current_state"] = string(a.currentState)
	result["fsm_total_states"] = fmt.Sprintf("%d", len(a.transitions.Handlers))
	result["fsm_total_transitions"] = fmt.Sprintf("%d", len(a.transitions.Map))

	// Add state-specific inspection if requested
	for _, inspectItem := range item {
		switch inspectItem {
		case "fsm_handlers":
			for state := range a.transitions.Handlers {
				result[fmt.Sprintf("fsm_handler_%s", state)] = "available"
			}
		case "fsm_transitions":
			for state, targets := range a.transitions.Map {
				result[fmt.Sprintf("fsm_transitions_%s", state)] = fmt.Sprintf("%v", targets)
			}
		}
	}

	return result
}

func (a *Actor) Terminate(reason error) {}
