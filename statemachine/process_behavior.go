package statemachine

import (
	"fmt"
	"reflect"
	"runtime"
	"strings"

	"ergo.services/actor/statemachine/cb"
	"ergo.services/ergo/gen"
	"ergo.services/ergo/lib"
)

type StateMachine[D any] struct {
	gen.Process

	behavior StateMachineBehavior[D]
	mailbox  gen.ProcessMailbox

	// The specification for the StateMachine
	spec StateMachineSpec[D]

	// The state the StateMachine is currently in
	currentState gen.Atom

	// The data associated with the StateMachine
	data D

	// stateMessageHandlers maps states to the (asynchronous) handlers for the state.
	// Key: State (gen.Atom) - The state for which the handler is registered.
	// Value: Map of message type to the handler for that message.
	//   Key: The type of the message received (String).
	//   Value: The message handler (any). There is a compile-time guarantee
	//          that the handler is of type StateMessageHandler[D, M].
	stateMessageHandlers map[gen.Atom]map[string]any

	// stateCallHandlers maps states to the (synchronous) handlers for the state.
	// Key: State (gen.Atom) - The state for which the handler is registered.
	// Value: Map of message type to the handler for that message.
	//   Key: The type of the message received (String).
	//   Value: The message handler (any). There is a compile-time guarantee
	//          that the handler is of type StateCallHandler[D, M, R].
	stateCallHandlers map[gen.Atom]map[string]any

	// eventHandlers maps events to the handler for the event.
	// Key: Event name (gen.Atom) - The name of the event
	// Value: The event handler (any). There is a compile-time guarantee that
	//        the handler is of type EventHandler[D, E]
	eventHandlers map[gen.Event]any

	// Callback that is invoked immediately after every state change. If no
	// callback is registered stateEnterCallback is nil.
	stateEnterCallback cb.StateEnterCallback[D]

	// Pointer to the most recently configured state timeout.
	stateTimeout *activeStateTimeout

	// Pointer to the most recently configured message timeout.
	messageTimeout *activeMessageTimeout

	// genericTimeouts maps the name of a generic timeout to the timeout
	genericTimeouts map[gen.Atom]*activeGenericTimeout
}

//
// ProcessBehavior implementation
//

func (s *StateMachine[D]) ProcessInit(process gen.Process, args ...any) (rr error) {
	var ok bool

	if s.behavior, ok = process.Behavior().(StateMachineBehavior[D]); ok == false {
		unknown := strings.TrimPrefix(reflect.TypeOf(process.Behavior()).String(), "*")
		return fmt.Errorf("ProcessInit: not a StateMachineBehavior %s", unknown)
	}

	s.Process = process
	s.mailbox = process.Mailbox()

	if lib.Recover() {
		defer func() {
			if r := recover(); r != nil {
				pc, fn, line, _ := runtime.Caller(2)
				s.Log().Panic("StateMachine initialization failed. Panic reason: %#v at %s[%s:%d]",
					r, runtime.FuncForPC(pc).Name(), fn, line)
				rr = gen.TerminateReasonPanic
			}
		}()
	}

	spec, err := s.behavior.Init(args...)
	if err != nil {
		return err
	}

	s.currentState = spec.initialState
	s.data = spec.data
	s.stateMessageHandlers = spec.stateMessageHandlers
	s.stateCallHandlers = spec.stateCallHandlers
	s.eventHandlers = spec.eventHandlers
	s.stateEnterCallback = spec.stateEnterCallback
	s.genericTimeouts = make(map[gen.Atom]*activeGenericTimeout)

	// Send a message to ourselves to start monitoring events if there are
	// event handlers registerd.
	if len(s.eventHandlers) > 0 {
		s.Send(s.PID(), startMonitoringEvents{})
	}
	s.Log().Debug("StateMachine: started in state %s", s.currentState)

	return nil
}

func (s *StateMachine[D]) ProcessRun() (rr error) {
	var message *gen.MailboxMessage

	if lib.Recover() {
		defer func() {
			if r := recover(); r != nil {
				pc, fn, line, _ := runtime.Caller(2)
				s.Log().Panic("StateMachine terminated. Panic reason: %#v at %s[%s:%d]",
					r, runtime.FuncForPC(pc).Name(), fn, line)
				rr = gen.TerminateReasonPanic
			}
		}()
	}

	for {
		if s.State() != gen.ProcessStateRunning {
			// process was killed by the node.
			return gen.TerminateReasonKill
		}

		if message != nil {
			gen.ReleaseMailboxMessage(message)
			message = nil
		}

		for {
			// check queues
			msg, ok := s.mailbox.Urgent.Pop()
			if ok {
				// got new urgent message. handle it
				message = msg.(*gen.MailboxMessage)
				break
			}

			msg, ok = s.mailbox.System.Pop()
			if ok {
				// got new system message. handle it
				message = msg.(*gen.MailboxMessage)
				break
			}

			msg, ok = s.mailbox.Main.Pop()
			if ok {
				// got new regular message. handle it
				message = msg.(*gen.MailboxMessage)
				break
			}

			if _, ok := s.mailbox.Log.Pop(); ok {
				panic("statemachne process can not be a logger")
			}

			// no messages in the mailbox
			return nil
		}

		// Any message should cancel the active message timeout
		if s.hasActiveMessageTimeout() {
			s.messageTimeout.cancel()
			s.messageTimeout.cancelled = true
		}

		switch message.Type {
		case gen.MailboxMessageTypeRegular:
			switch message.Message.(type) {
			case startMonitoringEvents:
				// start monitoring
				for event := range s.eventHandlers {
					if _, err := s.MonitorEvent(event); err != nil {
						panic(fmt.Sprintf("Error monitoring event: %v.", err))
					}
				}
				s.Log().Debug("StateMachine: monitoring events")
				return nil

			default:
				// check if there is a handler for the message in the current state
				messageType := reflect.TypeOf(message.Message).String()
				handler, ok := s.lookupMessageHandler(messageType)
				if ok == false {
					return fmt.Errorf("No handler for message %s in state %s", messageType, s.currentState)
				}
				return s.invokeMessageHandler(handler, message)
			}

		case gen.MailboxMessageTypeRequest:
			var reason error
			var result any

			// check if there is a handler for the call in the current state
			messageType := reflect.TypeOf(message.Message).String()
			handler, ok := s.lookupCallHandler(messageType)
			if ok == false {
				return fmt.Errorf("No handler for message %s in state %s", messageType, s.currentState)
			}
			result, reason = s.invokeCallHandler(handler, message)

			if reason != nil {
				// if reason is "normal" and we got response - send it before termination
				if reason == gen.TerminateReasonNormal {
					s.SendResponse(message.From, message.Ref, result)
				}
				return reason
			}
			// Note: we do not support async handling of sync request at the moment
			s.SendResponse(message.From, message.Ref, result)

		case gen.MailboxMessageTypeEvent:
			event := message.Message.(gen.MessageEvent)
			handler, exists := s.eventHandlers[event.Event]
			if exists == false {
				return fmt.Errorf("No handler for event %v", event)
			}
			return s.invokeEventHandler(handler, &event)

		case gen.MailboxMessageTypeExit:
			switch exit := message.Message.(type) {
			case gen.MessageExitPID:
				return fmt.Errorf("%s: %w", exit.PID, exit.Reason)

			case gen.MessageExitProcessID:
				return fmt.Errorf("%s: %w", exit.ProcessID, exit.Reason)

			case gen.MessageExitAlias:
				return fmt.Errorf("%s: %w", exit.Alias, exit.Reason)

			case gen.MessageExitEvent:
				return fmt.Errorf("%s: %w", exit.Event, exit.Reason)

			case gen.MessageExitNode:
				return fmt.Errorf("%s: %w", exit.Name, gen.ErrNoConnection)

			default:
				panic(fmt.Sprintf("unknown exit message: %#v", exit))
			}

		case gen.MailboxMessageTypeInspect:
			result := s.behavior.HandleInspect(message.From, message.Message.([]string)...)
			s.SendResponse(message.From, message.Ref, result)
		}
	}
}

func (s *StateMachine[D]) ProcessTerminate(reason error) {
	s.behavior.Terminate(reason)
}
