package action

import (
	"time"

	"ergo.services/ergo/gen"
)

// Timeout action interface helps to distinguish all Timeout actions.
// As of now, there are three timeout actions: [StateTimeout], [MessageTimeout] and
// [GenericTimeout].
type Timeout interface {
	TimerName() gen.Atom
}

// [StateTimeout] defines how long to wait in the current state.
// It is cancelled if statemachine change it's state before Duraion, otherwise Message
// will be sent to itself.
//
// This action is modelled after [state_timeout/0].
//
// [state_timeout/0]: https://www.erlang.org/doc/apps/stdlib/gen_statem.html#t:state_timeout/0
type StateTimeout struct {
	Duration time.Duration
	Message  any
}

func (StateTimeout) TimerName() gen.Atom { return "state" }
func (StateTimeout) isAction()           {}

// [MessageTimeout] defines how long to wait for event.
// It is cancelled if any event arrives before Duraion, otherwise Message
// will be sent to the statemachine.
//
// This action is modelled after [event_timeout/0].
//
// [event_timeout/0]: https://www.erlang.org/doc/apps/stdlib/gen_statem.html#t:event_timeout/0
type MessageTimeout struct {
	Duration time.Duration
	Message  any
}

func (MessageTimeout) TimerName() gen.Atom { return "message" }
func (MessageTimeout) isAction()           {}

// [GenericTimeout] defines how  long to wait for a named time-out event.
// When timer expires,  Message will be sent to the statemachine.
//
// This action is modelled after [generic_timeout/0].
//
// [generic_timeout/0]: https://www.erlang.org/doc/apps/stdlib/gen_statem.html#t:generic_timeout/0
type GenericTimeout struct {
	Name     gen.Atom
	Duration time.Duration
	Message  any
}

func (gt GenericTimeout) TimerName() gen.Atom { return gt.Name }
func (GenericTimeout) isAction()              {}
