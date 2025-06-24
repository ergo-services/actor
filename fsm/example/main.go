package main

import (
	"fmt"
	"time"

	"ergo.services/actor/fsm"
	"ergo.services/ergo"
	"ergo.services/ergo/act"
	"ergo.services/ergo/gen"
	"ergo.services/logger/colored"
)

// Demo message types
type StartMsg struct{}
type StopMsg struct{}
type PauseMsg struct{}
type ResumeMsg struct{}
type TickMsg struct{}
type ShutdownMsg struct{}

type ConfigureMsg struct {
	TickInterval time.Duration
	MaxTicks     int
	TaskName     string
}

type ProcessTaskMsg struct {
	ID       string
	Data     []byte
	Priority int
	Callback gen.PID
}

type StatusRequest struct {
	RequestID string
	Details   bool
}

type ErrorMsg struct {
	Code    int
	Message string
	Source  string
}

type HealthCheckMsg struct {
	RequestID string
}

// Demo states for a task processor FSM
const (
	StateIdle        gen.Atom = "idle"
	StateRunning     gen.Atom = "running"
	StatePaused      gen.Atom = "paused"
	StateStopped     gen.Atom = "stopped"
	StateError       gen.Atom = "error"
	StateMaintenance gen.Atom = "maintenance"
)

// TaskProcessorFSM demonstrates a real-world FSM for task processing
type TaskProcessorFSM struct {
	fsm.Actor

	// Internal state
	data         map[string]any
	tickCancel   gen.CancelFunc
	tasksHandled int
	startTime    time.Time
}

// Init initializes the task processor FSM
func (tp *TaskProcessorFSM) Init(args ...any) (gen.Atom, error) {
	tp.data = make(map[string]any)
	tp.startTime = time.Now()
	tp.tasksHandled = 0

	// Set default configuration
	tp.data["tick_interval"] = 2 * time.Second
	tp.data["max_ticks"] = 20
	tp.data["task_name"] = "demo-processor"

	// Parse initialization arguments
	if len(args) > 0 {
		if name, ok := args[0].(string); ok {
			tp.data["task_name"] = name
		}
	}

	tp.Log().Info("Initializing TaskProcessor '%s'", tp.data["task_name"])

	// Add states with transitions
	if err := tp.AddState(StateIdle, tp.HandleIdle, StateRunning, StateError, StateMaintenance); err != nil {
		return "", err
	}
	if err := tp.AddState(StateRunning, tp.HandleRunning, StatePaused, StateStopped, StateError, StateMaintenance); err != nil {
		return "", err
	}
	if err := tp.AddState(StatePaused, tp.HandlePaused, StateRunning, StateStopped, StateError); err != nil {
		return "", err
	}
	if err := tp.AddState(StateStopped, tp.HandleStopped, StateRunning, StateIdle); err != nil {
		return "", err
	}
	if err := tp.AddState(StateError, tp.HandleError, StateIdle, StateMaintenance); err != nil {
		return "", err
	}

	return StateIdle, nil
}

// HandleIdle processes messages in idle state
func (tp *TaskProcessorFSM) HandleIdle(from gen.PID, message any) (gen.Atom, error) {
	switch msg := message.(type) {
	case StartMsg:
		tp.Log().Info("Starting task processor")
		tp.data["tick_count"] = 0
		tp.scheduleTick()
		return StateRunning, nil

	case ConfigureMsg:
		tp.Log().Info("Configuring processor: interval=%v, max_ticks=%d, name=%s",
			msg.TickInterval, msg.MaxTicks, msg.TaskName)
		tp.data["tick_interval"] = msg.TickInterval
		tp.data["max_ticks"] = msg.MaxTicks
		tp.data["task_name"] = msg.TaskName
		return StateIdle, nil

	case StatusRequest:
		tp.sendStatusResponse(from, msg)
		return StateIdle, nil

	case ErrorMsg:
		tp.Log().Error("Error in idle state: %s", msg.Message)
		tp.data["error"] = msg
		return StateError, nil

	default:
		tp.Log().Warning("Unhandled message in idle state: %T", message)
		return StateIdle, nil
	}
}

// HandleRunning processes messages in running state
func (tp *TaskProcessorFSM) HandleRunning(from gen.PID, message any) (gen.Atom, error) {
	switch msg := message.(type) {
	case TickMsg:
		count := tp.data["tick_count"].(int)
		count++
		tp.data["tick_count"] = count

		tp.Log().Info("Processing tick #%d", count)

		maxTicks := tp.data["max_ticks"].(int)
		if count >= maxTicks {
			tp.Log().Info("Reached maximum ticks (%d), stopping", maxTicks)
			tp.cancelTick()
			return StateStopped, nil
		}

		tp.scheduleTick()
		return StateRunning, nil

	case ProcessTaskMsg:
		tp.handleTask(msg)
		return StateRunning, nil

	case PauseMsg:
		tp.Log().Info("Pausing task processor")
		tp.cancelTick()
		return StatePaused, nil

	case StopMsg:
		tp.Log().Info("Stopping task processor")
		tp.cancelTick()
		return StateStopped, nil

	case StatusRequest:
		tp.sendStatusResponse(from, msg)
		return StateRunning, nil

	case ErrorMsg:
		tp.Log().Error("Error in running state: %s", msg.Message)
		tp.cancelTick()
		tp.data["error"] = msg
		return StateError, nil

	default:
		tp.Log().Warning("Unhandled message in running state: %T", message)
		return StateRunning, nil
	}
}

// HandlePaused processes messages in paused state
func (tp *TaskProcessorFSM) HandlePaused(from gen.PID, message any) (gen.Atom, error) {
	switch msg := message.(type) {
	case ResumeMsg:
		tp.Log().Info("Resuming task processor")
		tp.scheduleTick()
		return StateRunning, nil

	case StopMsg:
		tp.Log().Info("Stopping from paused state")
		return StateStopped, nil

	case ProcessTaskMsg:
		// Handle urgent tasks while paused
		if msg.Priority > 8 {
			tp.Log().Info("Processing urgent task while paused: %s", msg.ID)
			tp.handleTask(msg)
		}
		return StatePaused, nil

	case StatusRequest:
		tp.sendStatusResponse(from, msg)
		return StatePaused, nil

	case ErrorMsg:
		tp.Log().Error("Error in paused state: %s", msg.Message)
		tp.data["error"] = msg
		return StateError, nil

	default:
		return StatePaused, nil
	}
}

// HandleStopped processes messages in stopped state
func (tp *TaskProcessorFSM) HandleStopped(from gen.PID, message any) (gen.Atom, error) {
	switch msg := message.(type) {
	case StartMsg:
		tp.Log().Info("Restarting task processor")
		tp.data["tick_count"] = 0
		tp.scheduleTick()
		return StateRunning, nil

	case ConfigureMsg:
		tp.Log().Info("Reconfiguring stopped processor")
		tp.data["tick_interval"] = msg.TickInterval
		tp.data["max_ticks"] = msg.MaxTicks
		tp.data["task_name"] = msg.TaskName
		return StateIdle, nil

	case StatusRequest:
		tp.sendStatusResponse(from, msg)
		return StateStopped, nil

	default:
		return StateStopped, nil
	}
}

// HandleError processes messages in error state
func (tp *TaskProcessorFSM) HandleError(from gen.PID, message any) (gen.Atom, error) {
	switch msg := message.(type) {
	case StartMsg:
		tp.Log().Info("Recovering from error state")
		delete(tp.data, "error")
		return StateIdle, nil

	case StatusRequest:
		tp.sendStatusResponse(from, msg)
		return StateError, nil

	default:
		return StateError, nil
	}
}

// HandleMaintenance - demonstrates dynamic state addition capability
func (tp *TaskProcessorFSM) HandleMaintenance(from gen.PID, message any) (gen.Atom, error) {
	switch msg := message.(type) {
	case StartMsg:
		tp.Log().Info("Exiting maintenance mode")
		return StateIdle, nil

	case StatusRequest:
		tp.sendStatusResponse(from, msg)
		return StateMaintenance, nil

	case ErrorMsg:
		tp.Log().Error("Error in maintenance: %s", msg.Message)
		tp.data["error"] = msg
		return StateError, nil

	default:
		tp.Log().Info("Maintenance mode - ignoring message: %T", message)
		return StateMaintenance, nil
	}
}

// High priority message handler (bypasses FSM state logic)
func (tp *TaskProcessorFSM) HandleMessage(from gen.PID, message any) error {
	switch msg := message.(type) {
	case HealthCheckMsg:
		tp.Log().Info("High priority health check: %s", msg.RequestID)
		response := map[string]any{
			"request_id": msg.RequestID,
			"state":      string(tp.CurrentState()),
			"uptime":     time.Since(tp.startTime).String(),
			"tasks":      tp.tasksHandled,
			"health":     "ok",
			"urgent":     true,
		}
		tp.Send(from, response)
		return nil

	case ShutdownMsg:
		tp.Log().Warning("High priority shutdown requested")
		tp.cancelTick()
		return gen.TerminateReasonShutdown

	default:
		tp.Log().Info("High priority message: %T", message)
		return nil
	}
}

// High priority call handler (bypasses FSM state logic)
func (tp *TaskProcessorFSM) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	switch req := request.(type) {
	case StatusRequest:
		return map[string]any{
			"request_id":        req.RequestID,
			"state":             string(tp.CurrentState()),
			"uptime":            time.Since(tp.startTime).String(),
			"tasks_handled":     tp.tasksHandled,
			"total_states":      len(tp.ListStates()),
			"valid_transitions": tp.ListTransitions(),
			"high_priority":     true,
		}, nil

	default:
		return map[string]any{
			"error":         "unknown_request",
			"state":         string(tp.CurrentState()),
			"high_priority": true,
		}, nil
	}
}

// Cleanup on termination
func (tp *TaskProcessorFSM) Terminate(reason error) {
	tp.Log().Info("TaskProcessor terminating: %v", reason)
	tp.cancelTick()
	tp.data = nil
}

// Helper methods

func (tp *TaskProcessorFSM) scheduleTick() {
	interval := tp.data["tick_interval"].(time.Duration)
	cancel, err := tp.SendAfter(tp.PID(), TickMsg{}, interval)
	if err != nil {
		tp.Log().Error("Failed to schedule tick: %v", err)
		return
	}
	tp.cancelTick()
	tp.tickCancel = cancel
}

func (tp *TaskProcessorFSM) cancelTick() {
	if tp.tickCancel != nil {
		tp.tickCancel()
		tp.tickCancel = nil
	}
}

func (tp *TaskProcessorFSM) handleTask(task ProcessTaskMsg) {
	tp.tasksHandled++
	tp.Log().Info("Processing task %s (priority: %d, size: %d bytes)",
		task.ID, task.Priority, len(task.Data))

	// Simulate task processing
	result := map[string]any{
		"task_id":   task.ID,
		"processed": true,
		"timestamp": time.Now(),
		"size":      len(task.Data),
		"priority":  task.Priority,
		"handler":   tp.data["task_name"],
	}

	if task.Callback != (gen.PID{}) {
		tp.Send(task.Callback, result)
	}
}

func (tp *TaskProcessorFSM) sendStatusResponse(from gen.PID, req StatusRequest) {
	response := map[string]any{
		"request_id":    req.RequestID,
		"state":         string(tp.CurrentState()),
		"uptime":        time.Since(tp.startTime).String(),
		"tasks_handled": tp.tasksHandled,
	}

	if req.Details {
		response["tick_interval"] = tp.data["tick_interval"]
		response["max_ticks"] = tp.data["max_ticks"]
		response["task_name"] = tp.data["task_name"]
		response["tick_count"] = tp.data["tick_count"]
		response["total_states"] = len(tp.ListStates())
		response["valid_transitions"] = tp.ListTransitions()
	}

	if errorInfo, exists := tp.data["error"]; exists {
		response["error"] = errorInfo
	}

	tp.Send(from, response)
}

// Demo coordinator that orchestrates the demonstration
type DemoCoordinator struct {
	act.Actor
	processor gen.PID
	demoStep  int
}

func (dc *DemoCoordinator) Init(args ...any) error {
	if len(args) > 0 {
		if pid, ok := args[0].(gen.PID); ok {
			dc.processor = pid
		}
	}
	dc.demoStep = 0

	// Start the demo
	dc.SendAfter(dc.PID(), "start_demo", 500*time.Millisecond)
	return nil
}

func (dc *DemoCoordinator) HandleMessage(from gen.PID, message any) error {
	switch msg := message.(type) {
	case string:
		switch msg {
		case "start_demo":
			dc.Log().Info("🚀 Starting FSM Demo...")
			dc.demoStep = 1
			dc.SendAfter(dc.PID(), "next_step", 100*time.Millisecond)
		case "next_step":
			switch dc.demoStep {
			case 1:
				dc.demonstrateBasicTransitions()
				dc.demoStep = 2
				dc.SendAfter(dc.PID(), "next_step", 2*time.Second)
			case 2:
				dc.demonstrateDynamicStates()
				dc.demoStep = 3
				dc.SendAfter(dc.PID(), "next_step", 2*time.Second)
			case 3:
				dc.demonstrateHighPriority()
				dc.demoStep = 4
				dc.SendAfter(dc.PID(), "next_step", 2*time.Second)
			case 4:
				dc.demonstrateErrorHandling()
				dc.demoStep = 5
				dc.SendAfter(dc.PID(), "next_step", 2*time.Second)
			case 5:
				dc.demonstrateShutdown()
				dc.Log().Info("✅ Demo completed successfully!")

				// starting a graceful shutdown
				go dc.Node().Stop()
			}
		}
	}
	return nil
}

func (dc *DemoCoordinator) demonstrateBasicTransitions() {
	dc.Log().Info("📋 1. Basic State Transitions Demo")

	// Configure the processor
	dc.Send(dc.processor, ConfigureMsg{
		TickInterval: 1 * time.Second,
		MaxTicks:     5,
		TaskName:     "basic-demo",
	})

	// Start processing
	dc.Log().Info("   ▶️  Starting processor...")
	dc.Send(dc.processor, StartMsg{})
	time.Sleep(3 * time.Second)

	// Pause
	dc.Log().Info("   ⏸️  Pausing processor...")
	dc.Send(dc.processor, PauseMsg{})
	time.Sleep(2 * time.Second)

	// Resume
	dc.Log().Info("   ▶️  Resuming processor...")
	dc.Send(dc.processor, ResumeMsg{})
	time.Sleep(3 * time.Second)

	// Stop
	dc.Log().Info("   ⏹️  Stopping processor...")
	dc.Send(dc.processor, StopMsg{})
	time.Sleep(1 * time.Second)
}

func (dc *DemoCoordinator) demonstrateDynamicStates() {
	dc.Log().Info("🔧 2. Dynamic State Management Demo")

	// Get current state info via call
	result, err := dc.Call(dc.processor, StatusRequest{RequestID: "dynamic-1", Details: true})
	if err != nil {
		dc.Log().Error("   ❌ Call failed: %v", err)
		return
	}

	if status, ok := result.(map[string]any); ok {
		dc.Log().Info("   📊 Current states: %v", status["total_states"])
		dc.Log().Info("   🔄 Valid transitions: %v", status["valid_transitions"])
	}

	// Note: In real usage, you would add the maintenance state dynamically
	// For this demo, we'll transition to it after adding it to the FSM
	dc.Log().Info("   🔧 Dynamic state management is available via AddState/RemoveState")

	// Send tasks while in various states
	dc.Log().Info("   📦 Sending processing tasks...")
	for i := 0; i < 3; i++ {
		dc.Node().Send(dc.processor, ProcessTaskMsg{
			ID:       fmt.Sprintf("task-%d", i+1),
			Data:     []byte(fmt.Sprintf("demo data %d", i+1)),
			Priority: 5 + i,
			Callback: gen.PID{}, // No callback for demo
		})
		time.Sleep(500 * time.Millisecond)
	}
}

func (dc *DemoCoordinator) demonstrateHighPriority() {
	dc.Log().Info("🚨 3. High Priority Messages Demo")

	// Send high priority health check
	dc.Log().Info("   💓 Sending high priority health check...")
	dc.SendWithPriority(dc.processor, HealthCheckMsg{RequestID: "health-1"}, gen.MessagePriorityHigh)
	time.Sleep(500 * time.Millisecond)

	// Send high priority call
	dc.Log().Info("   📞 Making high priority status call...")
	result, err := dc.CallWithPriority(
		dc.processor,
		StatusRequest{RequestID: "priority-call", Details: true},
		gen.MessagePriorityHigh,
	)
	if err != nil {
		dc.Log().Error("   ❌ High priority call failed: %v", err)
	} else {
		if status, ok := result.(map[string]any); ok {
			dc.Log().Info("   ✅ High priority response: state=%s, priority=%v",
				status["state"], status["high_priority"])
		}
	}

	// Send urgent task while paused
	dc.Send(dc.processor, PauseMsg{})
	time.Sleep(500 * time.Millisecond)

	dc.Log().Info("   🚀 Sending urgent task to paused processor...")
	dc.Send(dc.processor, ProcessTaskMsg{
		ID:       "urgent-task",
		Data:     []byte("urgent data"),
		Priority: 9, // High priority task
		Callback: gen.PID{},
	})

	time.Sleep(1 * time.Second)
	dc.Send(dc.processor, ResumeMsg{})
}

func (dc *DemoCoordinator) demonstrateErrorHandling() {
	dc.Log().Info("❌ 4. Error Handling Demo")

	// Trigger an error
	dc.Log().Info("   💥 Triggering error condition...")
	dc.Send(dc.processor, ErrorMsg{
		Code:    500,
		Message: "Simulated error for demo",
		Source:  "demo-coordinator",
	})
	time.Sleep(1 * time.Second)

	// Check status in error state
	result, err := dc.Call(dc.processor, StatusRequest{RequestID: "error-status", Details: true})
	if err != nil {
		dc.Log().Error("   ❌ Status call failed: %v", err)
	} else {
		if status, ok := result.(map[string]any); ok {
			dc.Log().Info("   📊 Error state status: %s", status["state"])
		}
	}

	// Recover from error
	dc.Log().Info("   🔄 Recovering from error...")
	dc.Send(dc.processor, StartMsg{})
	time.Sleep(1 * time.Second)
}

func (dc *DemoCoordinator) demonstrateShutdown() {
	dc.Log().Info("🏁 5. Final Status and Shutdown")

	// Get final status
	result, err := dc.Call(dc.processor, StatusRequest{RequestID: "final-status", Details: true})
	if err != nil {
		dc.Log().Error("   ❌ Final status call failed: %v", err)
	} else {
		if status, ok := result.(map[string]any); ok {
			dc.Log().Info("   📊 Final status: state=%s, uptime=%s, tasks=%v",
				status["state"], status["uptime"], status["tasks_handled"])
		}
	}

	// Graceful shutdown via high priority message
	dc.Log().Info("   🛑 Sending graceful shutdown...")
	dc.SendWithPriority(dc.processor, ShutdownMsg{}, gen.MessagePriorityHigh)
}

func main() {
	// Create node options
	var options gen.NodeOptions

	// Disable default logger to get rid of multiple logging to the os.Stdout
	options.Log.DefaultLogger.Disable = true

	// Add colored logger
	loggerColored, err := colored.CreateLogger(colored.Options{
		TimeFormat: time.DateTime,
	})
	if err != nil {
		fmt.Printf("Failed to create colored logger: %v\n", err)
		return
	}
	options.Log.Loggers = append(options.Log.Loggers, gen.Logger{
		Name:   "colored",
		Logger: loggerColored,
	})

	// Start the node
	node, err := ergo.StartNode("fsm@localhost", options)
	if err != nil {
		fmt.Printf("Failed to start node: %v\n", err)
		return
	}
	defer node.Stop()

	node.Log().Info("🌟 Started node: %s", node.Name())

	// Spawn the task processor FSM
	processorPID, err := node.Spawn(func() gen.ProcessBehavior {
		return &TaskProcessorFSM{}
	}, gen.ProcessOptions{}, "task-processor-demo")

	if err != nil {
		node.Log().Error("Failed to spawn FSM actor: %v", err)
		return
	}

	node.Log().Info("🤖 Spawned TaskProcessor FSM: %s", processorPID)

	// Spawn the demo coordinator process
	_, err = node.Spawn(func() gen.ProcessBehavior {
		return &DemoCoordinator{}
	}, gen.ProcessOptions{}, processorPID)

	if err != nil {
		node.Log().Error("Failed to spawn coordinator: %v", err)
		return
	}

	// Wait for node to stop
	node.Wait()
	fmt.Println("👋 Goodbye!")
}
