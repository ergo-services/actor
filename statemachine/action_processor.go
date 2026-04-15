package statemachine

import (
	"ergo.services/actor/statemachine/action"
	"ergo.services/ergo/gen"
)

func (s *StateMachine[D]) ProcessActions(actions []action.Action, state gen.Atom) {
	for _, act := range actions {
		switch act := act.(type) {
		case action.StateTimeout:
			if s.hasActiveStateTimeout() {
				s.stateTimeout.cancel()
				s.stateTimeout.cancelled = true
			}
			// Use SendAfter instead of manual goroutine + Send.
			// In Ergo v3.2.0+, proc.Send() returns "not allowed" when called from
			// a goroutine while the process is in Sleep state. SendAfter properly
			// handles this by using the node's routing methods directly.
			cancelFunc, err := s.SendAfter(s.PID(), act.Message, act.Duration)
			if err != nil {
				s.Log().Error("StateMachine: failed to schedule state timeout: %v", err)
				continue
			}
			s.stateTimeout = &activeStateTimeout{
				state:   state,
				timeout: act,
				cancel:  func() { cancelFunc() },
			}
		case action.GenericTimeout:
			if s.hasActiveGenericTimeout(act.Name) {
				s.genericTimeouts[act.Name].cancel()
				s.genericTimeouts[act.Name].cancelled = true
			}
			// Use SendAfter instead of manual goroutine + Send.
			cancelFunc, err := s.SendAfter(s.PID(), act.Message, act.Duration)
			if err != nil {
				s.Log().Error("StateMachine: failed to schedule generic timeout %s: %v", act.Name, err)
				continue
			}
			s.genericTimeouts[act.Name] = &activeGenericTimeout{
				timeout: act,
				cancel:  func() { cancelFunc() },
			}
		case action.MessageTimeout:
			// Use SendAfter instead of manual goroutine + Send.
			cancelFunc, err := s.SendAfter(s.PID(), act.Message, act.Duration)
			if err != nil {
				s.Log().Error("StateMachine: failed to schedule message timeout: %v", err)
				continue
			}
			s.messageTimeout = &activeMessageTimeout{
				timeout: act,
				cancel:  func() { cancelFunc() },
			}
		default:
			panic("unsupported action")
		}
	}
}
