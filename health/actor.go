package health

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/lib"
	"ergo.services/ergo/meta"
)

// Factory returns a new health Actor as gen.ProcessBehavior.
func Factory() gen.ProcessBehavior {
	return &Actor{}
}

// ActorBehavior defines the interface that health actor implementations must satisfy.
type ActorBehavior interface {
	gen.ProcessBehavior

	Init(args ...any) (Options, error)
	HandleMessage(from gen.PID, message any) error
	HandleCall(from gen.PID, ref gen.Ref, message any) (any, error)
	HandleInspect(from gen.PID, item ...string) map[string]string
	HandleSignalDown(signal gen.Atom) error
	HandleSignalUp(signal gen.Atom) error
	Terminate(reason error)
}

// Actor implements gen.ProcessBehavior for health checking.
type Actor struct {
	gen.Process

	behavior ActorBehavior
	mailbox  gen.ProcessMailbox

	options Options

	signals map[gen.Atom]*signalState

	liveStatus    atomic.Int32
	readyStatus   atomic.Int32
	startupStatus atomic.Int32

	liveResponse    atomic.Value
	readyResponse   atomic.Value
	startupResponse atomic.Value

	checkTimer gen.CancelFunc
}

// response types for JSON marshaling
type probeResponse struct {
	Status  string           `json:"status"`
	Signals []signalResponse `json:"signals,omitempty"`
}

type signalResponse struct {
	Signal  string `json:"signal"`
	Status  string `json:"status"`
	Timeout string `json:"timeout,omitempty"`
}

//
// ProcessBehavior implementation
//

// ProcessInit initializes the health actor.
func (a *Actor) ProcessInit(process gen.Process, args ...any) (rr error) {
	var ok bool

	if a.behavior, ok = process.Behavior().(ActorBehavior); ok == false {
		unknown := strings.TrimPrefix(reflect.TypeOf(process.Behavior()).String(), "*")
		return fmt.Errorf("ProcessInit: not a health ActorBehavior %s", unknown)
	}
	a.Process = process
	a.mailbox = process.Mailbox()

	if lib.Recover() {
		defer func() {
			if r := recover(); r != nil {
				pc, fn, line, _ := runtime.Caller(2)
				a.Log().Panic("Health initialization failed. Panic reason: %#v at %s[%s:%d]",
					r, runtime.FuncForPC(pc).Name(), fn, line)
				rr = gen.TerminateReasonPanic
			}
		}()
	}

	options, err := a.behavior.Init(args...)
	if err != nil {
		return err
	}

	a.options = options
	if a.options.Port < 1 {
		a.options.Port = DefaultPort
	}
	if a.options.Host == "" {
		a.options.Host = DefaultHost
	}
	if a.options.Path == "" {
		a.options.Path = DefaultPath
	}
	if a.options.CheckInterval < 1 {
		a.options.CheckInterval = DefaultCheckInterval
	}

	a.signals = make(map[gen.Atom]*signalState)

	// build initial (healthy, no signals) responses
	a.rebuildResponses()

	// start HTTP
	if err := a.startHTTP(); err != nil {
		return err
	}

	// schedule first heartbeat check
	a.scheduleCheck()

	return nil
}

// ProcessRun is the main message loop.
func (a *Actor) ProcessRun() (rr error) {
	var message *gen.MailboxMessage

	if lib.Recover() {
		defer func() {
			if r := recover(); r != nil {
				pc, fn, line, _ := runtime.Caller(2)
				a.Log().Panic("Health terminated. Panic reason: %#v at %s[%s:%d]",
					r, runtime.FuncForPC(pc).Name(), fn, line)
				rr = gen.TerminateReasonPanic
			}
		}()
	}

	for {
		if a.State() != gen.ProcessStateRunning {
			return gen.TerminateReasonKill
		}

		if message != nil {
			gen.ReleaseMailboxMessage(message)
			message = nil
		}

		for {
			msg, ok := a.mailbox.Urgent.Pop()
			if ok {
				message = msg.(*gen.MailboxMessage)
				break
			}

			msg, ok = a.mailbox.System.Pop()
			if ok {
				message = msg.(*gen.MailboxMessage)
				break
			}

			msg, ok = a.mailbox.Main.Pop()
			if ok {
				message = msg.(*gen.MailboxMessage)
				break
			}

			msg, ok = a.mailbox.Log.Pop()
			if ok {
				_ = msg
				panic("Health process can not be a logger")
			}

			return nil
		}

		switch message.Type {
		case gen.MailboxMessageTypeRegular:
			if reason := a.handleMessage(message.From, message.Message); reason != nil {
				return reason
			}

		case gen.MailboxMessageTypeRequest:
			var reason error
			var result any

			result, reason = a.handleCall(message.From, message.Ref, message.Message)

			if reason != nil {
				if reason == gen.TerminateReasonNormal && result != nil {
					a.SendResponse(message.From, message.Ref, result)
				}
				return reason
			}

			if result == nil {
				continue
			}

			a.SendResponse(message.From, message.Ref, result)

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
			result := a.inspect(message.From, message.Message.([]string)...)
			a.SendResponse(message.From, message.Ref, result)
		}
	}
}

func (a *Actor) inspect(from gen.PID, item ...string) map[string]string {
	result := a.HandleInspect(from, item...)
	for k, v := range a.behavior.HandleInspect(from, item...) {
		result[k] = v
	}
	return result
}

// ProcessTerminate cleans up the health actor.
func (a *Actor) ProcessTerminate(reason error) {
	if a.checkTimer != nil {
		a.checkTimer()
		a.checkTimer = nil
	}
	a.behavior.Terminate(reason)
}

//
// message dispatch
//

func (a *Actor) handleCall(from gen.PID, ref gen.Ref, message any) (any, error) {
	switch msg := message.(type) {
	case RegisterRequest:
		return a.handleRegister(from, msg)
	case UnregisterRequest:
		return a.handleUnregister(from, msg)
	default:
		return a.behavior.HandleCall(from, ref, message)
	}
}

func (a *Actor) handleMessage(from gen.PID, message any) error {
	switch msg := message.(type) {
	case MessageHeartbeat:
		return a.handleHeartbeat(from, msg)

	case MessageSignalUp:
		return a.handleSignalUp(msg)

	case MessageSignalDown:
		return a.handleSignalDown(msg)

	case messageCheckTimeouts:
		a.checkHeartbeatTimeouts()
		a.scheduleCheck()
		return nil

	case gen.MessageDownPID:
		found, err := a.handleProcessDown(msg)
		if err != nil {
			return err
		}
		if found == false {
			return a.behavior.HandleMessage(from, message)
		}
		return nil

	default:
		return a.behavior.HandleMessage(from, message)
	}
}

//
// signal management
//

func (a *Actor) handleRegister(from gen.PID, msg RegisterRequest) (RegisterResponse, error) {
	if msg.Signal == "" {
		return RegisterResponse{Error: "empty signal name"}, nil
	}

	probe := msg.Probe
	if probe == 0 {
		probe = ProbeLiveness
	}

	a.signals[msg.Signal] = &signalState{
		signal:       msg.Signal,
		probe:        probe,
		up:           true,
		timeout:      msg.Timeout,
		lastBeat:     time.Now(),
		registeredBy: from,
	}

	a.Monitor(from)
	a.rebuildResponses()

	a.Log().Debug("signal registered: %s (probe=%d, timeout=%s) by %s",
		msg.Signal, probe, msg.Timeout, from)

	return RegisterResponse{}, nil
}

func (a *Actor) handleUnregister(from gen.PID, msg UnregisterRequest) (UnregisterResponse, error) {
	state, exist := a.signals[msg.Signal]
	if exist == false {
		return UnregisterResponse{Error: "unknown signal"}, nil
	}

	a.Demonitor(state.registeredBy)
	delete(a.signals, msg.Signal)
	a.rebuildResponses()

	a.Log().Debug("signal unregistered: %s by %s", msg.Signal, from)

	return UnregisterResponse{}, nil
}

func (a *Actor) handleHeartbeat(from gen.PID, msg MessageHeartbeat) error {
	state, exist := a.signals[msg.Signal]
	if exist == false {
		return nil
	}

	state.lastBeat = time.Now()

	if state.up == false {
		state.up = true
		a.rebuildResponses()
		a.Log().Info("signal recovered via heartbeat: %s", msg.Signal)
		return a.behavior.HandleSignalUp(msg.Signal)
	}

	return nil
}

func (a *Actor) handleSignalUp(msg MessageSignalUp) error {
	state, exist := a.signals[msg.Signal]
	if exist == false {
		return nil
	}

	if state.up == true {
		return nil
	}

	state.up = true
	state.lastBeat = time.Now()
	a.rebuildResponses()
	a.Log().Info("signal up: %s", msg.Signal)
	return a.behavior.HandleSignalUp(msg.Signal)
}

func (a *Actor) handleSignalDown(msg MessageSignalDown) error {
	state, exist := a.signals[msg.Signal]
	if exist == false {
		return nil
	}

	if state.up == false {
		return nil
	}

	state.up = false
	a.rebuildResponses()
	a.Log().Warning("signal down: %s", msg.Signal)
	return a.behavior.HandleSignalDown(msg.Signal)
}

func (a *Actor) handleProcessDown(msg gen.MessageDownPID) (bool, error) {
	found := false
	changed := false
	for name, state := range a.signals {
		if state.registeredBy != msg.PID {
			continue
		}

		found = true
		if state.up == true {
			state.up = false
			changed = true
			a.Log().Warning("signal down (process terminated): %s", name)
			if err := a.behavior.HandleSignalDown(name); err != nil {
				return found, err
			}
		}
	}

	if changed {
		a.rebuildResponses()
	}

	return found, nil
}

func (a *Actor) checkHeartbeatTimeouts() {
	now := time.Now()
	changed := false

	for name, state := range a.signals {
		if state.timeout < 1 {
			continue
		}

		if state.up == false {
			continue
		}

		if now.Sub(state.lastBeat) > state.timeout {
			state.up = false
			changed = true
			a.Log().Warning("signal down (heartbeat timeout): %s", name)
			a.behavior.HandleSignalDown(name)
		}
	}

	if changed {
		a.rebuildResponses()
	}
}

func (a *Actor) scheduleCheck() {
	if a.checkTimer != nil {
		a.checkTimer()
	}
	a.checkTimer, _ = a.SendAfter(a.PID(), messageCheckTimeouts{}, a.options.CheckInterval)
}

//
// HTTP
//

func (a *Actor) startHTTP() error {
	mux := a.options.Mux
	if mux == nil {
		mux = http.NewServeMux()
	}

	mux.HandleFunc(a.options.Path+"/live", a.handleLive)
	mux.HandleFunc(a.options.Path+"/ready", a.handleReady)
	mux.HandleFunc(a.options.Path+"/startup", a.handleStartup)

	if a.options.Mux != nil {
		// external mux, no need to start own server
		a.Log().Debug("health handlers registered on external mux at %s/*", a.options.Path)
		return nil
	}

	serverOptions := meta.WebServerOptions{
		Port:    a.options.Port,
		Host:    a.options.Host,
		Handler: mux,
	}

	webserver, err := meta.CreateWebServer(serverOptions)
	if err != nil {
		return err
	}

	_, err = a.SpawnMeta(webserver, gen.MetaOptions{})
	if err != nil {
		webserver.Terminate(err)
		return err
	}

	a.Log().Debug("health HTTP server started at http://%s:%d%s/*", a.options.Host, a.options.Port, a.options.Path)
	return nil
}

func (a *Actor) handleLive(w http.ResponseWriter, r *http.Request) {
	status := int(a.liveStatus.Load())
	body := a.liveResponse.Load().([]byte)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(body)
}

func (a *Actor) handleReady(w http.ResponseWriter, r *http.Request) {
	status := int(a.readyStatus.Load())
	body := a.readyResponse.Load().([]byte)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(body)
}

func (a *Actor) handleStartup(w http.ResponseWriter, r *http.Request) {
	status := int(a.startupStatus.Load())
	body := a.startupResponse.Load().([]byte)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(body)
}

//
// response building
//

func (a *Actor) rebuildResponses() {
	a.rebuildProbeResponse(ProbeLiveness, &a.liveStatus, &a.liveResponse)
	a.rebuildProbeResponse(ProbeReadiness, &a.readyStatus, &a.readyResponse)
	a.rebuildProbeResponse(ProbeStartup, &a.startupStatus, &a.startupResponse)
}

func (a *Actor) rebuildProbeResponse(probe Probe, statusCode *atomic.Int32, body *atomic.Value) {
	healthy := true
	var signals []signalResponse

	for _, state := range a.signals {
		if state.probe&probe == 0 {
			continue
		}

		sr := signalResponse{
			Signal: string(state.signal),
		}
		if state.up {
			sr.Status = "up"
		} else {
			sr.Status = "down"
			healthy = false
		}
		if state.timeout > 0 {
			sr.Timeout = state.timeout.String()
		}
		signals = append(signals, sr)
	}

	resp := probeResponse{Signals: signals}
	if healthy {
		resp.Status = "healthy"
		statusCode.Store(http.StatusOK)
	} else {
		resp.Status = "unhealthy"
		statusCode.Store(http.StatusServiceUnavailable)
	}

	data, _ := json.Marshal(resp)
	body.Store(data)
}

//
// default callbacks for ActorBehavior interface
//

// Init default implementation.
func (a *Actor) Init(args ...any) (Options, error) {
	if len(args) == 0 {
		return Options{}, nil
	}

	options, ok := args[0].(Options)
	if ok == false {
		err := fmt.Errorf("%w: use %T", gen.ErrIncorrect, Options{})
		return Options{}, err
	}

	return options, nil
}

// HandleMessage default implementation.
func (a *Actor) HandleMessage(from gen.PID, message any) error {
	return nil
}

// HandleCall default implementation.
func (a *Actor) HandleCall(from gen.PID, ref gen.Ref, message any) (any, error) {
	return nil, nil
}

// HandleInspect default implementation.
func (a *Actor) HandleInspect(from gen.PID, item ...string) map[string]string {
	result := make(map[string]string)

	if a.options.Mux != nil {
		result["path"] = a.options.Path + "/*"
	} else {
		result["endpoint"] = fmt.Sprintf("http://%s:%d%s/*", a.options.Host, a.options.Port, a.options.Path)
	}
	result["signals"] = fmt.Sprintf("%d", len(a.signals))
	result["check_interval"] = a.options.CheckInterval.String()

	for name, state := range a.signals {
		status := "up"
		if state.up == false {
			status = "down"
		}
		result[string(name)] = status
	}

	return result
}

// HandleSignalDown default implementation.
func (a *Actor) HandleSignalDown(signal gen.Atom) error {
	return nil
}

// HandleSignalUp default implementation.
func (a *Actor) HandleSignalUp(signal gen.Atom) error {
	return nil
}

// Terminate default implementation.
func (a *Actor) Terminate(reason error) {}
