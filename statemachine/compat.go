package statemachine

import (
	"ergo.services/actor/statemachine/action"
	"ergo.services/actor/statemachine/cb"
)

// Compatibility layer to aid migration after code reorg.
// TODO: remove after consumers complete migration

// Deprecated: backwards compatibility alias. Use action.Action instead
type Action = action.Action

// Deprecated: backwards compatibility alias. Use action.StateTimeout instead
type StateTimeout = action.StateTimeout

// Deprecated: backwards compatibility alias. Use action.MessageTimeout instead
type MessageTimeout = action.MessageTimeout

// Deprecated: backwards compatibility alias. Use action.GenericTimeout instead
type GenericTimeout = action.GenericTimeout

// Deprecated: backwards compatibility alias. Use cb.StateMessageHandler instead
type StateMessageHandler[D any, M any] = cb.StateMessageHandler[D, M]

// Deprecated: backwards compatibility alias. Use cb.StateCallHandler instead
type StateCallHandler[D any, M any, R any] = cb.StateCallHandler[D, M, R]

// Deprecated: backwards compatibility alias. Use cb.EventHandler instead
type EventHandler[D any, E any] = cb.EventHandler[D, E]

// Deprecated: backwards compatibility alias. Use cb.StateEnterCallback instead
type StateEnterCallback[D any] = cb.StateEnterCallback[D]
