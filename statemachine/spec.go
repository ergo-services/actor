package statemachine

import (
	"reflect"

	"ergo.services/actor/statemachine/cb"
	"ergo.services/ergo/gen"
)

type StateMachineSpec[D any] struct {
	initialState         gen.Atom
	data                 D
	stateMessageHandlers map[gen.Atom]map[string]any
	stateCallHandlers    map[gen.Atom]map[string]any
	eventHandlers        map[gen.Event]any
	stateEnterCallback   cb.StateEnterCallback[D]
}

// [StateMachineSpec] fragments defined in a functional manner.
type Option[D any] func(*StateMachineSpec[D])

/*
Use this function inside your stattemachine Init() to create [StateMachineSpec].

States of the machine are denoted by [gen.Atom] values.

First argument is the initial state, later arguments placed in functional manner define
different aspects of the statemachine:
  - [WithData]() specifies data object for the statemachine instance
  - [WithStateEnterCallback]() defines the callback invoked whenever statemachine changes its state

Special methods to bind current state, incoming message and callback handler together:
  - [WithStateMessageHandler]
  - [WithStateCallHandler]
  - [WithEventHandler]

Example:

	var (
		initialState = gen.Atom("init")
		otherState = gen.Atom("other")
	)

	spec := statrmachine.NewStateMachineSpec(
		initialState,
		statrmachine.WithStateEnterCallback(onEnterNewState),
		statrmachine.WithStateMessageHandler(initialState, onSomeMessage),
		statrmachine.WithStateMessageHandler(initialState, onAnotherMessage),
		...
		statrmachine.WithStateMessageHandler(anotherState, ...),
	)
*/
func NewStateMachineSpec[D any](initialState gen.Atom, options ...Option[D]) StateMachineSpec[D] {
	spec := StateMachineSpec[D]{
		initialState:         initialState,
		stateMessageHandlers: make(map[gen.Atom]map[string]any),
		stateCallHandlers:    make(map[gen.Atom]map[string]any),
		eventHandlers:        make(map[gen.Event]any),
	}
	for _, opt := range options {
		opt(&spec)
	}
	return spec
}

// Assigns data object of type D to the statemachine
func WithData[D any](data D) Option[D] {
	return func(s *StateMachineSpec[D]) {
		s.data = data
	}
}

// Binds a callback to the event of changing state
func WithStateEnterCallback[D any](callback cb.StateEnterCallback[D]) Option[D] {
	return func(s *StateMachineSpec[D]) {
		s.stateEnterCallback = callback
	}
}

/*
Binds a callback function `handler` to statemachine's state `state`

The specified handler will be executed if:
  - statemachine is in the state `state` and
  - statemachine actor receives a message of type M sent asynchronously via [gen.Process.Send]

To handle multiple message types while statemachine is in state `state`, use multiple [WithStateMessageHandler] calls with same state and different [StateMessageHandler]'s.

For handler implementation specifics see [StateMessageHandler].
*/
func WithStateMessageHandler[D any, M any](state gen.Atom, handler cb.StateMessageHandler[D, M]) Option[D] {
	messageType := reflect.TypeOf((*M)(nil)).Elem().String()
	return func(s *StateMachineSpec[D]) {
		if _, exists := s.stateMessageHandlers[state]; exists == false {
			s.stateMessageHandlers[state] = make(map[string]any)
		}
		s.stateMessageHandlers[state][messageType] = handler
	}
}

/*
Binds a callback function `handler` to statemachine's state `state`

The specified handler will be executed if:
  - statemachine is in the state `state` and
  - statemachine actor receives a message of type M sent synchronously via [gen.Process.Call]

To handle multiple message types while statemachine is in state `state`, use multiple [WithStateCallHandler] calls with same state and different [StateCallHandler]'s.

For handler implementation specifics see [StateCallHandler].
*/
func WithStateCallHandler[D any, M any, R any](state gen.Atom, handler cb.StateCallHandler[D, M, R]) Option[D] {
	messageType := reflect.TypeOf((*M)(nil)).Elem().String()
	return func(s *StateMachineSpec[D]) {
		if _, exists := s.stateCallHandlers[state]; exists == false {
			s.stateCallHandlers[state] = make(map[string]any)
		}
		s.stateCallHandlers[state][messageType] = handler
	}
}

/*
Binds a callback function `handler` to statemachine's state `state`

The specified handler will be executed if:
  - statemachine is in the state `state` and
  - statemachine actor receives an event of type E sent synchronously via [gen.Process.SendEvent]

To handle multiple event types while statemachine is in state `state`, use multiple [WithEventHandler] calls with same state and different [EventHandler]'s.

For handler implementation specifics see [EventHandler].
*/
func WithEventHandler[D any, E any](event gen.Event, handler cb.EventHandler[D, E]) Option[D] {
	return func(s *StateMachineSpec[D]) {
		s.eventHandlers[event] = handler
	}
}
