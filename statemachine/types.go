package statemachine

import (
	"ergo.services/actor/statemachine/action"
	"ergo.services/ergo/gen"
)

type activeStateTimeout struct {
	state     gen.Atom
	timeout   action.StateTimeout
	cancel    func()
	cancelled bool
}

type activeGenericTimeout struct {
	timeout   action.GenericTimeout
	cancel    func()
	cancelled bool
}

type activeMessageTimeout struct {
	timeout   action.MessageTimeout
	cancel    func()
	cancelled bool
}

// message to self to start events monitoring
type startMonitoringEvents struct{}
