package statemachine

import (
	"fmt"
	"reflect"

	"ergo.services/ergo/gen"
)

func (s *StateMachine[D]) hasActiveStateTimeout() bool {
	return s.stateTimeout != nil && !s.stateTimeout.cancelled
}

func (s *StateMachine[D]) hasActiveMessageTimeout() bool {
	return s.messageTimeout != nil && !s.messageTimeout.cancelled
}

func (s *StateMachine[D]) hasActiveGenericTimeout(name gen.Atom) bool {
	if timeout, exists := s.genericTimeouts[name]; exists {
		return !timeout.cancelled
	}
	return false
}

func (s *StateMachine[D]) lookupMessageHandler(messageType string) (any, bool) {
	if stateMessageHandlers, exists := s.stateMessageHandlers[s.currentState]; exists == true {
		if callback, exists := stateMessageHandlers[messageType]; exists == true {
			return callback, true
		}
	}
	return nil, false
}

func (s *StateMachine[D]) invokeMessageHandler(handler any, message *gen.MailboxMessage) error {
	stateMachineValue := reflect.ValueOf(s)
	callbackValue := reflect.ValueOf(handler)
	fromValue := reflect.ValueOf(message.From)
	stateValue := reflect.ValueOf(s.currentState)
	dataValue := reflect.ValueOf(s.Data())
	msgValue := reflect.ValueOf(message.Message)
	messageType := reflect.TypeOf(message).String()

	results := callbackValue.Call([]reflect.Value{fromValue, stateValue, dataValue, msgValue, stateMachineValue})

	validateResultSize(results, 4, messageType)
	if isError, err := resultIsError(results); isError == true {
		return err
	}

	return updateStateMachineWithResults(stateMachineValue, results)
}

func (s *StateMachine[D]) lookupCallHandler(messageType string) (any, bool) {
	if stateCallHandlers, exists := s.stateCallHandlers[s.currentState]; exists == true {
		if callback, exists := stateCallHandlers[messageType]; exists == true {
			return callback, true
		}
	}
	return nil, false
}

func (s *StateMachine[D]) invokeCallHandler(handler any, message *gen.MailboxMessage) (any, error) {
	stateMachineValue := reflect.ValueOf(s)
	callbackValue := reflect.ValueOf(handler)
	fromValue := reflect.ValueOf(message.From)
	stateValue := reflect.ValueOf(s.currentState)
	dataValue := reflect.ValueOf(s.Data())
	msgValue := reflect.ValueOf(message.Message)
	messageType := reflect.TypeOf(message).String()

	results := callbackValue.Call([]reflect.Value{fromValue, stateValue, dataValue, msgValue, stateMachineValue})

	validateResultSize(results, 5, messageType)
	if isError, err := resultIsError(results); isError == true {
		return nil, err
	}
	err := updateStateMachineWithResults(stateMachineValue, results)
	if err != nil {
		return nil, err
	}
	result := results[2].Interface()
	return result, nil
}

func (s *StateMachine[D]) invokeEventHandler(handler any, message *gen.MessageEvent) error {
	stateMachineValue := reflect.ValueOf(s)
	callbackValue := reflect.ValueOf(handler)
	stateValue := reflect.ValueOf(s.currentState)
	dataValue := reflect.ValueOf(s.Data())
	msgValue := reflect.ValueOf(message.Message)
	messageType := reflect.TypeOf(message).String()

	results := callbackValue.Call([]reflect.Value{stateValue, dataValue, msgValue, stateMachineValue})

	validateResultSize(results, 4, messageType)
	if isError, err := resultIsError(results); isError == true {
		return err
	}
	updateStateMachineWithResults(stateMachineValue, results)

	return nil
}

func validateResultSize(results []reflect.Value, expectedSize int, messageType string) {
	if len(results) != expectedSize {
		panic(fmt.Sprintf("StateMachine terminated. Panic reason: unexpected "+
			"error when invoking call handler for %s", messageType))
	}
}

func resultIsError(results []reflect.Value) (bool, error) {
	errIndex := len(results) - 1
	if !results[errIndex].IsNil() {
		err := results[errIndex].Interface().(error)
		return true, err
	}
	return false, nil
}

func updateStateMachineWithResults(s reflect.Value, results []reflect.Value) error {
	// Check if any actions were returned. MessageHandler and EventHandler have
	// the result tuple (gen.Atom, D, []Action, error) with the actions at index
	// 2. CallHandler has the result typle (gen.Atom, D, R, []Action, error)
	// with the actions at index 3.
	var actionsIndex int
	hasResult := len(results) == 5
	if hasResult {
		actionsIndex = 3
	} else {
		actionsIndex = 2
	}
	if !isSliceNilOrEmpty(results[actionsIndex]) {
		processActionsMethod := s.MethodByName("ProcessActions")
		if processActionsMethod.IsNil() {
		}
		processActionsMethod.Call([]reflect.Value{results[actionsIndex], results[0]})
	}

	// Update the data
	setDataMethod := s.MethodByName("SetData")
	setDataMethod.Call([]reflect.Value{results[1]})

	// It is important that we set the state last as this can potentially trigger
	// a state enter callback. By design state enter callbacks are triggered
	// after setting up state timeouts as state timeouts are tied to te state
	// they are defined for. A state enter callback could transition to another
	// state which then will cancel the state timeout.
	setCurrentStateMethod := s.MethodByName("SetCurrentState")
	err := setCurrentStateMethod.Call([]reflect.Value{results[0]})[0]
	if !err.IsNil() {
		return err.Interface().(error)
	}

	return nil
}

func isSliceNilOrEmpty(resultValue reflect.Value) bool {
	if resultValue.IsNil() {
		return true
	}

	if resultValue.Len() == 0 {
		return true
	}

	return false
}
