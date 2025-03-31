package cqrs

import (
	"ergo.services/ergo/gen"
	"ergo.services/ergo/lib"
	"fmt"
	"reflect"
	"runtime"
	"strings"
)

type Actor struct {
	gen.Process
	behavior Behavior
	mailbox  gen.ProcessMailbox

	condition Condition
	handlers  map[gen.Atom]*handler
}

type Condition func(from gen.PID, mtype gen.MailboxMessageType, message any) gen.Atom

type Handler struct {
	Factory gen.ProcessFactory
	Options gen.ProcessOptions
	Args    []any
}

type handler struct {
	Handler
	sbeavior  string
	pid       gen.PID
	forwarded uint64
	restarts  uint64
}

type Options struct {
	Condition Condition
	Handlers  map[gen.Atom]Handler
}

type Behavior interface {
	gen.ProcessBehavior
	// Init invoked on a spawn CQRS for the initializing.
	Init(args ...any) (Options, error)

	// HandleMessage invoked if cqrs.Actor received a message sent with gen.Process.Send(...).
	// Non-nil value of the returning error will cause termination of this process.
	// To stop this process normally, return gen.TerminateReasonNormal
	// or any other for abnormal termination.
	HandleMessage(from gen.PID, message any) error

	// HandleCall invoked if cqrs.Actor got a synchronous request made with gen.Process.Call(...).
	// Return nil as a result to handle this request asynchronously and
	// to provide the result later using the gen.Process.SendResponse(...) method.
	HandleCall(from gen.PID, ref gen.Ref, request any) (any, error)

	// Terminate invoked on a termination process
	Terminate(reason error)

	// HandleEvent invoked on an event message if this process got subscribed on
	// this event using gen.Process.LinkEvent or gen.Process.MonitorEvent
	HandleEvent(message gen.MessageEvent) error

	// HandleInspect invoked on the request made with gen.Process.Inspect(...)
	HandleInspect(from gen.PID, item ...string) map[string]string
}

func (a *Actor) ProcessInit(process gen.Process, args ...any) (rr error) {
	var ok bool

	if a.behavior, ok = process.Behavior().(Behavior); ok == false {
		unknown := strings.TrimPrefix(reflect.TypeOf(process.Behavior()).String(), "*")
		return fmt.Errorf("ProcessInit: not a cqrs.Behavior %s", unknown)
	}
	a.Process = process
	a.mailbox = process.Mailbox()

	if lib.Recover() {
		defer func() {
			if r := recover(); r != nil {
				pc, fn, line, _ := runtime.Caller(2)
				a.Log().Panic("cqrs.Actor initialization failed. Panic reason: %#v at %s[%s:%d]",
					r, runtime.FuncForPC(pc).Name(), fn, line)
				rr = gen.TerminateReasonPanic
			}
		}()
	}

	options, err := a.behavior.Init(args...)
	if err != nil {
		return err
	}

	if options.Condition == nil {
		return fmt.Errorf("condition must be defined")
	}

	if len(options.Handlers) == 0 {
		return fmt.Errorf("no handlers defined")
	}

	a.condition = options.Condition
	a.handlers = make(map[gen.Atom]*handler)

	for name, h := range options.Handlers {
		if h.Factory == nil {
			return fmt.Errorf("factory is not defined for %s", name)
		}
		if _, exist := a.handlers[name]; exist == true {
			return fmt.Errorf("handler name %s is already exist", name)
		}
		h.Options.LinkParent = true

		pid, err := a.Spawn(h.Factory, h.Options, h.Args...)
		if err != nil {
			return err
		}
		pi, _ := a.Node().ProcessInfo(pid)

		a.handlers[name] = &handler{
			Handler:  h,
			pid:      pid,
			sbeavior: pi.Behavior,
		}
	}

	return nil
}

func (a *Actor) ProcessRun() (rr error) {
	var message *gen.MailboxMessage

	if lib.Recover() {
		defer func() {
			if r := recover(); r != nil {
				pc, fn, line, _ := runtime.Caller(2)
				a.Log().Panic("cqrs.Actor terminated. Panic reason: %#v at %s[%s:%d]",
					r, runtime.FuncForPC(pc).Name(), fn, line)
				rr = gen.TerminateReasonPanic
			}
		}()
	}

	for {
		if a.State() != gen.ProcessStateRunning {
			// process was killed by the node.
			return gen.TerminateReasonKill
		}

		if message != nil {
			gen.ReleaseMailboxMessage(message)
			message = nil
		}

		for {
			// check queues
			if msg, ok := a.mailbox.Urgent.Pop(); ok {
				// got new urgent message. handle it
				message = msg.(*gen.MailboxMessage)
				break
			}

			if msg, ok := a.mailbox.System.Pop(); ok {
				// got new system message. handle it
				message = msg.(*gen.MailboxMessage)
				break
			}

			if msg, ok := a.mailbox.Main.Pop(); ok {
				// got new regular message. handle it
				message = msg.(*gen.MailboxMessage)
				if message.Type < gen.MailboxMessageTypeExit {
					// MailboxMessageTypeRegular, MailboxMessageTypeRequest, MailboxMessageTypeEvent
					if forwared := a.forward(message); forwared {
						// it shouldn't be "released" back to the pool
						message = nil
						continue
					}

					// hasnt been forwared, handle it by itself
				}

				break
			}

			if _, ok := a.mailbox.Log.Pop(); ok {
				panic("cqrs.Actor process can not be a logger")
			}

			// no messages in the mailbox
			return nil
		}

		switch message.Type {
		case gen.MailboxMessageTypeRegular:
			if reason := a.behavior.HandleMessage(message.From, message.Message); reason != nil {
				return reason
			}

		case gen.MailboxMessageTypeRequest:
			var reason error
			var result any

			result, reason = a.behavior.HandleCall(message.From, message.Ref, message.Message)

			if reason != nil {
				// if reason is "normal" and we got response - send it before termination
				if reason == gen.TerminateReasonNormal && result != nil {
					a.SendResponse(message.From, message.Ref, result)
				}
				return reason
			}

			if result == nil {
				// async handling of sync request. response could be sent
				// later, even by the other process
				continue
			}

			a.SendResponse(message.From, message.Ref, result)

		case gen.MailboxMessageTypeEvent:
			if reason := a.behavior.HandleEvent(message.Message.(gen.MessageEvent)); reason != nil {
				return reason
			}

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
			result := a.behavior.HandleInspect(message.From, message.Message.([]string)...)
			a.SendResponse(message.From, message.Ref, result)
		}

	}
}
func (a *Actor) forward(message *gen.MailboxMessage) bool {
	var err error

	name := a.condition(message.From, message.Type, message.Message)

	// checking for the handler
	h, exist := a.handlers[name]
	if exist == false {
		return false
	}

	err = a.Forward(h.pid, message, gen.MessagePriorityNormal)
	if err == nil {
		h.forwarded++
		return true
	}

	if err == gen.ErrProcessUnknown || err == gen.ErrProcessTerminated {
		// restart
		pid, err := a.Spawn(h.Factory, h.Options, h.Args...)
		if err != nil {
			a.Log().Error("unable to spawn new handler process: %s", err)
			return false
		}
		a.Forward(pid, message, gen.MessagePriorityNormal)
		h.pid = pid
		h.forwarded++
		h.restarts++
		return true
	}

	a.Log().Error("unable to forward message to handler %s: %s", name, err)
	return false
}

//
// default callbacks for Behavior interface
//

func (a *Actor) HandleMessage(from gen.PID, message any) error {
	a.Log().Warning("cqrs.Actor.HandleMessage: unhandled message from %s", from)
	return nil
}
func (a *Actor) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	a.Log().Warning("cqrs.Actor.HandleCall: unhandled request from %s", from)
	return nil, nil
}
func (a *Actor) Terminate(reason error) {}
func (a *Actor) HandleEvent(message gen.MessageEvent) error {
	a.Log().Warning("cqrs.Actor.HandleEvent: unhandled event message %#v", message)
	return nil
}
func (a *Actor) HandleInspect(from gen.PID, item ...string) map[string]string {
	inspect := make(map[string]string)

	for name, h := range a.handlers {
		k := fmt.Sprintf("%s:restarts", name)
		inspect[k] = fmt.Sprintf("%d", h.restarts)
		k = fmt.Sprintf("%s:forwarded", name)
		inspect[k] = fmt.Sprintf("%d", h.forwarded)
		k = fmt.Sprintf("%s:behavior", name)
		inspect[k] = h.sbeavior
		k = fmt.Sprintf("%s:PID", name)
		inspect[k] = h.pid.String()
	}

	return inspect
}
