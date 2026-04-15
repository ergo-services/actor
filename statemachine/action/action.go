/*
Actions for a state transitions are modelled after [action/0].

These transition actions can be invoked by returning them from the handlers:
  - [ergo.services/actor/statemachine/cb.StateMessageHandler]
  - [ergo.services/actor/statemachine/cb.StateCallHandler]
  - [ergo.services/actor/statemachine/cb.EventHandler]
  - [ergo.services/actor/statemachine/cb.StateEnterCallback]

[action/0]: https://www.erlang.org/doc/apps/stdlib/gen_statem.html#t:action/0
*/
package action

// Represents Erlang's action returned from state handler function.
type Action interface {
	isAction()
}
