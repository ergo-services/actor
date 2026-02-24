package statemachine

import (
	"testing"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/unit"
)

type Data struct{}

type BasicStatemachine struct {
	FSM[Data]
}

type StateChange struct{}

func factoryBasicStatemachine() gen.ProcessBehavior {
	return &BasicStatemachine{}
}

func (b *BasicStatemachine) Init(args ...any) (Spec[Data], error) {
	spec := NewSpec(
		gen.Atom("StateA"),

		WithStateEnterCallback(stateEnter),
		WithStateMessageHandler(gen.Atom("StateA"), changeState),
		WithStateCallHandler(gen.Atom("StateA"), changeStateSync),
	)
	return spec, nil
}

func changeState(_ gen.PID, state gen.Atom, data Data, msg StateChange, proc gen.Process) (gen.Atom, Data, []Action, error) {
	return gen.Atom("StateB"), data, nil, nil
}

func changeStateSync(_ gen.PID, state gen.Atom, data Data, msg StateChange, proc gen.Process) (gen.Atom, Data, gen.Atom, []Action, error) {
	return gen.Atom("StateB"), data, gen.Atom("StateB"), nil, nil
}

func stateEnter(oldState gen.Atom, newState gen.Atom, data Data, proc gen.Process) (gen.Atom, Data, error) {
	if newState == gen.Atom("StateB") {
		return newState, data, gen.TerminateReasonNormal
	}
	return newState, data, nil
}

func TestStateEnterCallback_SendShouldPropagateErrors(t *testing.T) {
	actor, err := unit.Spawn(t, factoryBasicStatemachine)
	unit.Nil(t, err)

	actor.SendMessage(
		gen.PID{Node: "test", ID: 100, Creation: 0},
		StateChange{})

	unit.Equal(t, true, actor.IsTerminated())
	unit.Equal(t, gen.TerminateReasonNormal, actor.TerminationReason())
}

func TestStateEnterCallback_CallShouldPropagateErrors(t *testing.T) {
	actor, err := unit.Spawn(t, factoryBasicStatemachine)
	unit.Nil(t, err)

	res := actor.Call(
		gen.PID{Node: "test", ID: 100, Creation: 0},
		StateChange{})

	unit.Equal(t, gen.TerminateReasonNormal, res.Error)
	unit.Equal(t, true, actor.IsTerminated())
	unit.Equal(t, gen.TerminateReasonNormal, actor.TerminationReason())
}
