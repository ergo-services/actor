package statemachine_test

import (
	"testing"

	"ergo.services/actor/statemachine"
	"ergo.services/actor/statemachine/action"
	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/unit"
)

type Data struct{}

type BasicStatemachine struct {
	statemachine.StateMachine[Data]
}

const (
	stateA = gen.Atom("StateA")
	stateB = gen.Atom("StateB")
)

type StateChange struct{}

func factoryBasicStatemachine() gen.ProcessBehavior {
	return &BasicStatemachine{}
}

func (b *BasicStatemachine) Init(args ...any) (statemachine.StateMachineSpec[Data], error) {
	spec := statemachine.NewStateMachineSpec(
		stateA,

		statemachine.WithStateEnterCallback(stateEnter),
		statemachine.WithStateMessageHandler(stateA, changeState),
		statemachine.WithStateCallHandler(stateA, changeStateSync),
	)
	return spec, nil
}

func changeState(_ gen.PID, state gen.Atom, data Data, msg StateChange, proc gen.Process) (gen.Atom, Data, []action.Action, error) {
	return stateB, data, nil, nil
}

func changeStateSync(_ gen.PID, state gen.Atom, data Data, msg StateChange, proc gen.Process) (gen.Atom, Data, gen.Atom, []action.Action, error) {
	return stateB, data, stateB, nil, nil
}

func stateEnter(oldState gen.Atom, newState gen.Atom, data Data, proc gen.Process) (gen.Atom, Data, error) {
	if newState == stateB {
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
