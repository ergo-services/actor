# FSM - Finite State Machine

This package provides a simple and efficient Finite State Machine (FSM) implementation for Ergo actors.

## Features

- **Compile-time safety**: No reflection - Go's type system ensures handler correctness at compile time
- **Simplified API**: Clean Init signature returning only initial state
- **Automatic transition management**: Transitions built inside Actor automatically
- **Ownership validation**: API design ensures handlers belong to FSM instance
- **Method-based handlers**: Use `Handle<State>` methods for state transitions
- **Built-in inspection**: Monitor FSM state and transitions
- **Flexible message types**: Handle any Go type as messages (structs, interfaces, etc.)

## Design Principles

1. **Compile-time Safety**: Go's type system prevents handler mismatches without reflection
2. **Performance**: Direct method calls for state transitions
3. **Simplicity**: Minimal API surface with clear patterns  
4. **Type Safety**: StateTransition signature enforced by compiler
5. **Ownership Clarity**: `my.AddState(state, my.HandleState, ...)` pattern makes ownership obvious

## Usage

### Basic Implementation

```go
package main

import (
    "ergo.services/actor/fsm"
    "ergo.services/ergo/gen"
)

type MyFSM struct {
    fsm.Actor
    data map[string]any
}

// Init implements the fsm.Behavior interface
func (my *MyFSM) Init(args ...any) (gen.Atom, error) {
    my.data = make(map[string]any)
    
    // Add states with ownership validation through API design
    // The 'my' receiver ensures handlers belong to this FSM instance
    my.AddState("idle", my.HandleIdle, "running", "error")
    my.AddState("running", my.HandleRunning, "paused", "stopped")
    my.AddState("paused", my.HandlePaused, "running", "stopped")
    my.AddState("stopped", my.HandleStopped, "idle")
    my.AddState("error", my.HandleError, "idle")
    
    // Just return the initial state - transitions are built automatically
    return "idle", nil
}

// HandleIdle handles messages when in the idle state
func (my *MyFSM) HandleIdle(from gen.PID, message any) (gen.Atom, error) {
    switch msg := message.(type) {
    case StartMsg:
        // Transition to running state
        return "running", nil
    case ErrorMsg:
        // Transition to error state
        return "error", nil
    default:
        // Stay in current state
        return "idle", nil
    }
}

// HandleRunning handles messages when in the running state
func (my *MyFSM) HandleRunning(from gen.PID, message any) (gen.Atom, error) {
    switch msg := message.(type) {
    case StopMsg:
        // Transition to stopped state
        return "stopped", nil
    case PauseMsg:
        // Transition to paused state
        return "paused", nil
    default:
        // Stay in current state
        return "running", nil
    }
}

// HandleStopped handles messages when in the stopped state
func (my *MyFSM) HandleStopped(from gen.PID, message any) (gen.Atom, error) {
    switch msg := message.(type) {
    case StartMsg:
        // Transition back to running state
        return "running", nil
    default:
        // Stay in current state
        return "stopped", nil
    }
}
```

### Ownership Validation Through API Design

The new API provides ownership validation without reflection through clear design patterns:

```go
func (my *MyFSM) Init(args ...any) (gen.Atom, error) {
    // ✅ CORRECT: All handlers called with 'my' receiver belong to 'my' instance
    my.AddState("idle", my.HandleIdle, "running")
    my.AddState("running", my.HandleRunning, "idle")
    
    // The pattern 'my.AddState(..., my.HandleXXX, ...)' makes ownership obvious
    // Any deviation from this pattern is immediately visible in code review:
    
    // ❌ WRONG: These patterns would be obvious violations:
    // my.AddState("idle", FakeHandler, "running")        // Standalone function
    // my.AddState("idle", otherFSM.HandleIdle, "running") // Different instance
    
    return "idle", nil
}
```

### Flexible Message Types

The FSM can handle any Go type as messages:

```go
// Simple message types
type StartMsg struct{}
type StopMsg struct{}

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

// Handler can switch on any message type
func (my *MyFSM) HandleIdle(from gen.PID, message any) (gen.Atom, error) {
    switch msg := message.(type) {
    case StartMsg:
        return "running", nil
    case ConfigureMsg:
        my.configure(msg)
        return "idle", nil  // Stay in idle after configuration
    case StatusRequest:
        my.sendStatus(from, msg)
        return "idle", nil
    default:
        return "idle", nil
    }
}
```

### Spawning the FSM

```go
// Create and spawn the FSM actor
pid, err := node.Spawn(func() gen.ProcessBehavior {
    return &MyFSM{}
}, gen.ProcessOptions{})
if err != nil {
    // handle error
}

// Send messages to trigger state transitions
node.Send(pid, StartMsg{})  // idle -> running
node.Send(pid, PauseMsg{})  // running -> paused
node.Send(pid, ResumeMsg{}) // paused -> running
```

## API Reference

### Types

#### `StateTransition`
```go
type StateTransition func(from gen.PID, message any) (gen.Atom, error)
```
Function signature for state transition handlers. Compile-time enforced.

#### `Transitions`
```go
type Transitions struct {
    Handlers StateHandlers
    Map      TransitionMap
}
```
Contains both state handlers and transition map. Built automatically inside Actor.

#### `Behavior`
```go
type Behavior interface {
    gen.ProcessBehavior
    
    // Simplified Init - just return initial state and error
    Init(args ...any) (gen.Atom, error)
}
```
Interface that FSM behaviors must implement.

### Actor Methods

#### `AddState(state, handler, allowedTransitions...)`
Adds a state with its handler and allowed next states. Called on FSM instance for ownership validation.

#### `CurrentState()`
Returns the current state of the FSM.

#### `GetValidTransitions()`
Returns valid transitions from the current state.

## Compile-Time Safety

The FSM ensures type safety at compile time:

```go
// ✅ This compiles - correct signature and ownership
func (my *MyFSM) HandleIdle(from gen.PID, message any) (gen.Atom, error) {
    return "running", nil
}

// ❌ This won't compile - wrong signature
func (my *MyFSM) BadHandler(message any) gen.Atom {  // Missing 'from' parameter and error return
    return "state"
}

func (my *MyFSM) Init(args ...any) (gen.Atom, error) {
    // ❌ This line would cause a compile-time error
    my.AddState("state", my.BadHandler)  // Type mismatch!
    return "state", nil
}
```

## State Transition Rules

1. State handlers must match the `StateTransition` signature (compile-time enforced)
2. Returning the same state name keeps the current state
3. Returning an error terminates the FSM
4. State transitions are validated against the transition map at runtime
5. Invalid transitions are logged and rejected

## Runtime Validation

- **Initialization**: Validates that initial state has a handler
- **Transitions**: Validates state transitions against the transition map
- **Warnings**: Logs warnings for target states without handlers

## Error Handling

- If a state handler returns an error, the FSM terminates
- If no handler is found for the current state, the FSM terminates with error
- Invalid transitions are logged and the state remains unchanged

## Inspection

The FSM supports inspection through the `HandleInspect` method:

```go
// Inspect the FSM state
result, err := node.Call(pid, gen.MessageInspect{})
inspection := result.(map[string]string)

fmt.Println("Current state:", inspection["fsm_current_state"])
fmt.Println("Total states:", inspection["fsm_total_states"])
fmt.Println("Total transitions:", inspection["fsm_total_transitions"])
```

## API Evolution

### Before (Redundant)
```go
func (my *MyFSM) Init(args ...any) (gen.Atom, Transitions, error) {
    my.transitions = NewTransitions(my)
    my.AddState("idle", my.HandleIdle, "running")
    return "idle", my.GetTransitions(), nil  // ❌ Redundant return
}
```

### After (Clean)
```go
func (my *MyFSM) Init(args ...any) (gen.Atom, error) {
    my.AddState("idle", my.HandleIdle, "running")
    return "idle", nil  // ✅ Just the initial state
}
```

The transitions are now built automatically inside the Actor, eliminating redundancy.

## Benefits Over Reflection-Based Approaches

| Aspect | FSM (No Reflection) | Reflection-Based |
|--------|-------------------|------------------|
| Compile-time Safety | ✅ Full | ❌ Runtime only |
| Performance | ✅ High | ❌ Slower |
| Build Time | ✅ Fast | ❌ Slower |
| IDE Support | ✅ Full autocomplete | ❌ Limited |
| Debugging | ✅ Easy | ❌ Complex |
| Type Errors | ✅ Compile-time | ❌ Runtime |
| Ownership Validation | ✅ API Design | ❌ Complex runtime checks |

## Best Practices

1. **Use the ownership pattern**: Always call `my.AddState(state, my.HandleXXX, ...)`
2. **Name handlers clearly**: Use `Handle<StateName>` pattern
3. **Keep handlers focused**: Each handler should manage one state's logic
4. **Use meaningful state names**: Choose descriptive atom names
5. **Handle all message types**: Use default cases to handle unexpected messages
6. **Validate transitions**: Define clear transition maps to prevent invalid state changes
7. **Handle empty PIDs**: Send responses to `my.PID()` when `from` is empty

## Comparison with Statemachine Package

| Feature | FSM | Statemachine |
|---------|-----|--------------|
| Reflection | No | Yes |
| Performance | High | Medium |
| API Complexity | Low | High |
| Type Safety | Compile-time | Runtime |
| Ownership Validation | API Design | Runtime |
| Init Signature | Simple | Complex |

Choose FSM when you need:
- High performance
- Simple, clean API
- Compile-time safety
- Obvious ownership patterns
- Minimal complexity

Choose Statemachine when you need:
- Advanced features (timeouts, events)
- Generic data handling
- Complex state machine behaviors 