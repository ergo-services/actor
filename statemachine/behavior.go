package statemachine

import "ergo.services/ergo/gen"

type StateMachineBehavior[D any] interface {
	gen.ProcessBehavior

	Init(args ...any) (StateMachineSpec[D], error)

	HandleMessage(from gen.PID, message any) error

	HandleCall(from gen.PID, ref gen.Ref, message any) (any, error)

	HandleEvent(event gen.MessageEvent) error

	HandleInspect(from gen.PID, item ...string) map[string]string

	Terminate(reason error)

	CurrentState() gen.Atom

	SetCurrentState(gen.Atom) error

	Data() D

	SetData(data D)
}

func (s *StateMachine[D]) HandleMessage(from gen.PID, message any) error {
	s.Log().Warning("StateMachine.HandleMessage: unhandled message from %s", from)
	return nil
}

func (s *StateMachine[D]) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	s.Log().Warning("StateMachine.HandleCall: unhandled request from %s", from)
	return nil, nil
}
func (s *StateMachine[D]) HandleEvent(message gen.MessageEvent) error {
	s.Log().Warning("StateMachine.HandleEvent: unhandled event message %#v", message)
	return nil
}

func (s *StateMachine[D]) HandleInspect(from gen.PID, item ...string) map[string]string {
	return nil
}

func (s *StateMachine[D]) Terminate(reason error) {}

func (s *StateMachine[D]) CurrentState() gen.Atom {
	return s.currentState
}

func (s *StateMachine[D]) SetCurrentState(state gen.Atom) error {
	if state != s.currentState {
		s.Log().Debug("StateMachine: switching to state %s", state)
		oldState := s.currentState
		s.currentState = state

		// If there is a state timeout set up for the new state then we have
		// just registered this timeout in `ProcessActions` and we should not
		// touch it. Otherwise we should cancel the active state timeout if there
		// is one.
		if s.hasActiveStateTimeout() && s.stateTimeout.state != state {
			s.Log().Debug("StateMachine: canceling state timeout for state %s", state)
			s.stateTimeout.cancel()
			s.stateTimeout.cancelled = true
		}
		// Execute state enter callback until no new transition is triggered.
		if s.stateEnterCallback != nil {
			newState, newData, err := s.stateEnterCallback(oldState, state, s.data, s)
			if err != nil {
				return err
			}
			s.SetData(newData)
			s.SetCurrentState(newState)
		}
	}
	return nil
}

func (s *StateMachine[D]) Data() D {
	return s.data
}

func (s *StateMachine[D]) SetData(data D) {
	s.data = data
}
