/*
Statemachine package and corresponding actor is modelled after Erlang's [gen_statem].

The [StateMachine] is a [gen.ProcessBehavior] implementation. By Erlang ideology,
[gen_statem] has a Data associated with it. Here it is achieved by using a generic type
parameter D. A difference with the Erlang implementation is that the data and the new
state are not returned from the state callback functions, instead they are part of the
mutable state of the actor.

States are of type [gen.Atom] (to make it feel even more Erlang-ish) and the
[StateMachineSpec] is defined by means of [NewStateMachineSpec] function.

The Init function implementation is mandatory and returns the
[statemachine.StateMachineSpec] of the [StateMachine]. There are several helper functions
and types to aid capturing the type of the message being processed by the callbacks. Using
functional option pattern also avoids unnecessary copying of the spec.

When attempting an invalid state transition, following Erlang/OTP's "let it crash"
philosophy, the process terminates.

Quick glance at a statemachine creation:

	type yourFsmData struct {}
	type YourStatemachine struct {
	  statemachine.StateMachine[yourFsmData]
	}

	const (
	  initialState = gen.Atom("init")
	  otherState = gen.Atom("other")
	)

	func (s *YourStatemachine) Init(args ...any) (spec statemachine.StateMachineSpec[yourFsmData], err error) {
	  spec:= statemachine.NewStateMachineSpec(
	    initialState,
	    statrmachine.WithStateEnterCallback(...),
	    statrmachine.WithStateMessageHandler(initialState, ...),
	    statrmachine.WithStateMessageHandler(otherState, ...),
	  )

	  return spec, nil
	}

For specific details see below.

[gen_statem]: https://www.erlang.org/doc/apps/stdlib/gen_statem.html
*/
package statemachine
