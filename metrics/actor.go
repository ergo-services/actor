package metrics

import (
	"fmt"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/lib"
	"ergo.services/ergo/meta"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func Factory() gen.ProcessBehavior {
	return &Actor{}
}

type ActorBehavior interface {
	gen.ProcessBehavior

	Init(args ...any) (Options, error)

	HandleMessage(from gen.PID, message any) error
	HandleCall(from gen.PID, ref gen.Ref, message any) (any, error)
	HandleEvent(event gen.MessageEvent) error
	HandleInspect(from gen.PID, item ...string) map[string]string

	CollectMetrics() error
	Terminate(reason error)
}

type messageCollectMetrics struct{}

// Registry returns the prometheus registry for registering custom metrics
func (a *Actor) Registry() *prometheus.Registry {
	return a.registry
}

// customMetrics returns the sync.Map used for custom metric storage.
// When Shared is set, returns the shared map; otherwise returns the local map.
func (a *Actor) customMetrics() *sync.Map {
	if a.options.Shared != nil {
		return &a.options.Shared.custom
	}
	return &a.custom
}

// Actor implements gen.ProcessBehavior
type Actor struct {
	gen.Process

	behavior ActorBehavior
	mailbox  gen.ProcessMailbox

	options Options

	registry *prometheus.Registry

	// Sub-metric collectors (computation state only, prometheus objects in customMetrics map)
	latency     latencyMetrics
	depth       depthMetrics
	utilization utilizationMetrics
	throughput  throughputMetrics
	inittime    initTimeMetrics
	wakeups     wakeupsMetrics
	event       eventMetrics

	// Custom metrics storage (standalone mode; shared mode uses Shared.custom)
	custom sync.Map // string -> *registeredMetric
}

//
// internal metric registration and lookup helpers
//

func registerInternalGauge(cm *sync.Map, registry *prometheus.Registry, name, help string, constLabels prometheus.Labels) {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        name,
		Help:        help,
		ConstLabels: constLabels,
	})
	registry.MustRegister(g)
	cm.Store(name, &registeredMetric{
		name:       name,
		metricType: MetricGauge,
		collector:  g,
		gauge:      g,
		internal:   true,
	})
}

func registerInternalGaugeVec(cm *sync.Map, registry *prometheus.Registry, name, help string, constLabels prometheus.Labels, labels []string) {
	gv := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name:        name,
		Help:        help,
		ConstLabels: constLabels,
	}, labels)
	registry.MustRegister(gv)
	cm.Store(name, &registeredMetric{
		name:       name,
		metricType: MetricGauge,
		labelNames: labels,
		collector:  gv,
		gaugeVec:   gv,
		internal:   true,
	})
}

func gaugeFromMap(cm *sync.Map, name string) prometheus.Gauge {
	v, _ := cm.Load(name)
	return v.(*registeredMetric).gauge
}

func gaugeVecFromMap(cm *sync.Map, name string) *prometheus.GaugeVec {
	v, _ := cm.Load(name)
	return v.(*registeredMetric).gaugeVec
}

//
// ProcessBehavior implementation
//

// ProcessInit
func (a *Actor) ProcessInit(process gen.Process, args ...any) (rr error) {
	var ok bool

	if a.behavior, ok = process.Behavior().(ActorBehavior); ok == false {
		unknown := strings.TrimPrefix(reflect.TypeOf(process.Behavior()).String(), "*")
		return fmt.Errorf("ProcessInit: not a metrics ActorBehavior %s", unknown)
	}
	a.Process = process
	a.mailbox = process.Mailbox()

	if lib.Recover() {
		defer func() {
			if r := recover(); r != nil {
				pc, fn, line, _ := runtime.Caller(2)
				a.Log().Panic("Metrics initialization failed. Panic reason: %#v at %s[%s:%d]",
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

	// Determine role based on Shared/Port/Mux configuration
	isPrimary := a.options.Shared != nil && (a.options.Port > 0 || a.options.Mux != nil)
	isStandalone := a.options.Shared == nil

	// Registry: shared or own
	a.registry = prometheus.NewRegistry()
	if a.options.Shared != nil {
		a.registry = a.options.Shared.Registry()
	}

	// Defaults: only apply Port/Host defaults in standalone mode
	if isStandalone {
		if a.options.Port < 1 {
			a.options.Port = DefaultPort
		}
		if a.options.Host == "" {
			a.options.Host = DefaultHost
		}
	}
	if a.options.Path == "" {
		a.options.Path = DefaultPath
	}
	if a.options.CollectInterval < 1 {
		a.options.CollectInterval = DefaultCollectInterval
	}
	if a.options.TopN < 1 {
		a.options.TopN = DefaultTopN
	}

	// Primary/standalone: base metrics + HTTP + collection timer.
	// Workers: only handle custom metric messages, nothing to init.
	if isStandalone || isPrimary {
		if err := a.initializeErgoMetrics(); err != nil {
			return err
		}
		if err := a.startHTTPServer(); err != nil {
			return err
		}
		a.Send(a.PID(), messageCollectMetrics{})
	}

	return nil
}

func (a *Actor) initializeErgoMetrics() error {
	cm := a.customMetrics()
	nodeName := string(a.Node().Name())
	nodeLabels := prometheus.Labels{"node": nodeName}

	// Node metrics
	registerInternalGauge(cm, a.registry, "ergo_node_uptime_seconds", "Node uptime in seconds", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_processes_total", "Total number of processes", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_processes_running", "Number of running processes", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_processes_zombie", "Number of zombie processes", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_processes_spawned_total", "Cumulative number of successfully spawned processes", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_processes_spawn_failed_total", "Cumulative number of failed spawn attempts", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_processes_terminated_total", "Cumulative number of terminated processes", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_memory_used_bytes", "Memory used in bytes", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_memory_alloc_bytes", "Memory allocated in bytes", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_cpu_user_seconds", "User CPU time in seconds", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_cpu_system_seconds", "System CPU time in seconds", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_cpu_cores", "Number of CPU cores available", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_applications_total", "Total number of applications", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_applications_running", "Number of running applications", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_registered_names_total", "Total number of registered names", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_registered_aliases_total", "Total number of registered aliases", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_registered_events_total", "Total number of registered events", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_events_published_total", "Cumulative number of events published by local producers", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_events_received_total", "Cumulative number of events received from remote nodes", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_events_local_sent_total", "Cumulative number of event messages sent to local subscribers", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_events_remote_sent_total", "Cumulative number of event messages sent to remote subscribers", nodeLabels)

	// Network metrics
	registerInternalGauge(cm, a.registry, "ergo_connected_nodes_total", "Total number of connected nodes", nodeLabels)
	registerInternalGaugeVec(cm, a.registry, "ergo_remote_node_uptime_seconds", "Remote node uptime in seconds", nodeLabels, []string{"remote_node"})
	registerInternalGaugeVec(cm, a.registry, "ergo_remote_messages_in_total", "Total number of messages received from remote node", nodeLabels, []string{"remote_node"})
	registerInternalGaugeVec(cm, a.registry, "ergo_remote_messages_out_total", "Total number of messages sent to remote node", nodeLabels, []string{"remote_node"})
	registerInternalGaugeVec(cm, a.registry, "ergo_remote_bytes_in_total", "Total number of bytes received from remote node", nodeLabels, []string{"remote_node"})
	registerInternalGaugeVec(cm, a.registry, "ergo_remote_bytes_out_total", "Total number of bytes sent to remote node", nodeLabels, []string{"remote_node"})

	// Initialize sub-metric collectors
	a.latency.init(cm, a.registry, nodeLabels)
	a.depth.init(cm, a.registry, nodeLabels)
	a.utilization.init(cm, a.registry, nodeLabels)
	a.throughput.init(cm, a.registry, nodeLabels)
	a.inittime.init(cm, a.registry, nodeLabels)
	a.wakeups.init(cm, a.registry, nodeLabels)
	a.event.init(cm, a.registry, nodeLabels)

	// Process aggregate metrics
	registerInternalGauge(cm, a.registry, "ergo_process_messages_in", "Sum of messages received by all processes on this node", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_process_messages_out", "Sum of messages sent by all processes on this node", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_process_running_time_seconds", "Sum of callback running time across all processes on this node", nodeLabels)
	registerInternalGauge(cm, a.registry, "ergo_process_wakeups", "Sum of wakeups (Sleep to Running transitions) across all processes on this node", nodeLabels)

	return nil
}

func (a *Actor) startHTTPServer() error {
	mux := a.options.Mux
	if mux == nil {
		mux = http.NewServeMux()
	}

	mux.Handle(a.options.Path, promhttp.HandlerFor(a.registry, promhttp.HandlerOpts{}))

	if a.options.Mux != nil {
		// external mux provided, skip starting own server
		a.Log().Debug("metrics handler registered on external mux at %s", a.options.Path)
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

	a.Log().Debug("metrics HTTP server started at http://%s:%d%s", a.options.Host, a.options.Port, a.options.Path)
	return nil
}

func (a *Actor) collectBaseMetrics() error {
	cm := a.customMetrics()

	// Get node information
	nodeInfo, err := a.Node().Info()
	if err != nil {
		return fmt.Errorf("failed to get node info: %w", err)
	}

	// Update node metrics
	gaugeFromMap(cm, "ergo_node_uptime_seconds").Set(float64(nodeInfo.Uptime))
	gaugeFromMap(cm, "ergo_processes_total").Set(float64(nodeInfo.ProcessesTotal))
	gaugeFromMap(cm, "ergo_processes_running").Set(float64(nodeInfo.ProcessesRunning))
	gaugeFromMap(cm, "ergo_processes_zombie").Set(float64(nodeInfo.ProcessesZombee))
	gaugeFromMap(cm, "ergo_processes_spawned_total").Set(float64(nodeInfo.ProcessesSpawned))
	gaugeFromMap(cm, "ergo_processes_spawn_failed_total").Set(float64(nodeInfo.ProcessesSpawnFailed))
	gaugeFromMap(cm, "ergo_processes_terminated_total").Set(float64(nodeInfo.ProcessesTerminated))
	gaugeFromMap(cm, "ergo_memory_used_bytes").Set(float64(nodeInfo.MemoryUsed))
	gaugeFromMap(cm, "ergo_memory_alloc_bytes").Set(float64(nodeInfo.MemoryAlloc))
	gaugeFromMap(cm, "ergo_cpu_user_seconds").Set(float64(nodeInfo.UserTime) / 1e9)
	gaugeFromMap(cm, "ergo_cpu_system_seconds").Set(float64(nodeInfo.SystemTime) / 1e9)
	gaugeFromMap(cm, "ergo_cpu_cores").Set(float64(runtime.NumCPU()))
	gaugeFromMap(cm, "ergo_applications_total").Set(float64(nodeInfo.ApplicationsTotal))
	gaugeFromMap(cm, "ergo_applications_running").Set(float64(nodeInfo.ApplicationsRunning))
	gaugeFromMap(cm, "ergo_registered_names_total").Set(float64(nodeInfo.RegisteredNames))
	gaugeFromMap(cm, "ergo_registered_aliases_total").Set(float64(nodeInfo.RegisteredAliases))
	gaugeFromMap(cm, "ergo_registered_events_total").Set(float64(nodeInfo.RegisteredEvents))
	gaugeFromMap(cm, "ergo_events_published_total").Set(float64(nodeInfo.EventsPublished))
	gaugeFromMap(cm, "ergo_events_received_total").Set(float64(nodeInfo.EventsReceived))
	gaugeFromMap(cm, "ergo_events_local_sent_total").Set(float64(nodeInfo.EventsLocalSent))
	gaugeFromMap(cm, "ergo_events_remote_sent_total").Set(float64(nodeInfo.EventsRemoteSent))

	// Get network information
	network := a.Node().Network()
	connectedNodes := network.Nodes()

	// Update network metrics
	gaugeFromMap(cm, "ergo_connected_nodes_total").Set(float64(len(connectedNodes)))

	remoteUptime := gaugeVecFromMap(cm, "ergo_remote_node_uptime_seconds")
	remoteMsgIn := gaugeVecFromMap(cm, "ergo_remote_messages_in_total")
	remoteMsgOut := gaugeVecFromMap(cm, "ergo_remote_messages_out_total")
	remoteBytIn := gaugeVecFromMap(cm, "ergo_remote_bytes_in_total")
	remoteBytOut := gaugeVecFromMap(cm, "ergo_remote_bytes_out_total")

	// Reset remote node metrics before updating
	remoteUptime.Reset()
	remoteMsgIn.Reset()
	remoteMsgOut.Reset()
	remoteBytIn.Reset()
	remoteBytOut.Reset()

	// Update per-node metrics
	for _, nodeName := range connectedNodes {
		remoteNode, err := network.Node(nodeName)
		if err != nil {
			a.Log().Warning("failed to get remote node %s: %s", nodeName, err)
			continue
		}

		remoteInfo := remoteNode.Info()
		nodeNameStr := string(nodeName)
		remoteUptime.WithLabelValues(nodeNameStr).Set(float64(remoteInfo.Uptime))
		remoteMsgIn.WithLabelValues(nodeNameStr).Set(float64(remoteInfo.MessagesIn))
		remoteMsgOut.WithLabelValues(nodeNameStr).Set(float64(remoteInfo.MessagesOut))
		remoteBytIn.WithLabelValues(nodeNameStr).Set(float64(remoteInfo.BytesIn))
		remoteBytOut.WithLabelValues(nodeNameStr).Set(float64(remoteInfo.BytesOut))
	}

	// Collect per-process metrics in a single pass
	a.latency.begin()
	a.depth.begin()
	a.utilization.begin()
	a.throughput.begin()
	a.inittime.begin()
	a.wakeups.begin()

	var totalMessagesIn uint64
	var totalMessagesOut uint64
	var totalRunningTime uint64
	var totalWakeups uint64

	topN := a.options.TopN

	a.Node().ProcessRangeShortInfo(func(info gen.ProcessShortInfo) bool {
		// Aggregate counters
		totalMessagesIn += info.MessagesIn
		totalMessagesOut += info.MessagesOut
		totalRunningTime += info.RunningTime
		totalWakeups += info.Wakeups

		// Mailbox depth metrics
		a.depth.observe(info, topN)

		// Process utilization metrics
		a.utilization.observe(info, topN)

		// Latency metrics (no-op without -tags=latency)
		a.latency.observe(info, topN)

		// Process throughput metrics
		a.throughput.observe(info, topN)

		// Process init time metrics
		a.inittime.observe(info, topN)

		// Process wakeups metrics
		a.wakeups.observe(info, topN)

		return true
	})

	a.depth.flush()
	a.utilization.flush()
	a.latency.flush()
	a.throughput.flush()
	a.inittime.flush()
	a.wakeups.flush()

	// Collect event metrics
	a.event.begin()
	a.Node().EventRangeInfo(func(info gen.EventInfo) bool {
		a.event.observe(info, topN)
		return true
	})
	a.event.flush()

	gaugeFromMap(cm, "ergo_process_messages_in").Set(float64(totalMessagesIn))
	gaugeFromMap(cm, "ergo_process_messages_out").Set(float64(totalMessagesOut))
	gaugeFromMap(cm, "ergo_process_running_time_seconds").Set(float64(totalRunningTime) / 1e9)
	gaugeFromMap(cm, "ergo_process_wakeups").Set(float64(totalWakeups))

	return nil
}

func (a *Actor) ProcessRun() (rr error) {
	var message *gen.MailboxMessage

	if lib.Recover() {
		defer func() {
			if r := recover(); r != nil {
				pc, fn, line, _ := runtime.Caller(2)
				a.Log().Panic("Metrics terminated. Panic reason: %#v at %s[%s:%d]",
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
			msg, ok := a.mailbox.Urgent.Pop()
			if ok {
				// got new urgent message. handle it
				message = msg.(*gen.MailboxMessage)
				break
			}

			msg, ok = a.mailbox.System.Pop()
			if ok {
				// got new system message. handle it
				message = msg.(*gen.MailboxMessage)
				break
			}

			msg, ok = a.mailbox.Main.Pop()
			if ok {
				// got new regular message. handle it
				message = msg.(*gen.MailboxMessage)
				break
			}

			msg, ok = a.mailbox.Log.Pop()
			if ok {
				panic("Metrics process can not be a logger")
			}

			// no messages in the mailbox
			return nil
		}

		switch message.Type {
		case gen.MailboxMessageTypeRegular:
			switch msg := message.Message.(type) {
			case messageCollectMetrics:
				// Collect base metrics (NodeInfo, NetworkInfo)
				if err := a.collectBaseMetrics(); err != nil {
					a.Log().Error("failed to collect base metrics: %s", err)
				}

				// Call user-defined CollectMetrics to extend with custom metrics
				if err := a.behavior.CollectMetrics(); err != nil {
					return err
				}

				// Schedule next collection
				a.SendAfter(a.PID(), messageCollectMetrics{}, a.options.CollectInterval)

			case MessageUnregister:
				a.handleUnregister(msg)
			case MessageGaugeSet:
				a.handleGaugeSet(msg)
			case MessageGaugeAdd:
				a.handleGaugeAdd(msg)
			case MessageCounterAdd:
				a.handleCounterAdd(msg)
			case MessageHistogramObserve:
				a.handleHistogramObserve(msg)
			case gen.MessageDownPID:
				if a.handleProcessDown(msg) == false {
					if err := a.behavior.HandleMessage(message.From, message.Message); err != nil {
						return err
					}
				}
			default:
				if err := a.behavior.HandleMessage(message.From, message.Message); err != nil {
					return err
				}
			}

		case gen.MailboxMessageTypeRequest:
			var reason error
			var result any

			switch msg := message.Message.(type) {
			case RegisterRequest:
				result = a.handleRegister(message.From, msg)
			default:
				result, reason = a.behavior.HandleCall(message.From, message.Ref, message.Message)
			}

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
			result := a.inspect(message.From, message.Message.([]string)...)
			a.SendResponse(message.From, message.Ref, result)
		}

	}
}

//
// custom metric handlers
//

func (a *Actor) handleRegister(from gen.PID, msg RegisterRequest) RegisterResponse {
	cm := a.customMetrics()

	// Check for existing metric with the same name
	if existing, loaded := cm.Load(msg.Name); loaded {
		m := existing.(*registeredMetric)
		if m.internal {
			return RegisterResponse{
				Error: fmt.Sprintf("metric %q is reserved for internal use", msg.Name),
			}
		}
		// Idempotent: same type + same labels = success
		if m.metricType == msg.Type && equalStringSlices(m.labelNames, msg.Labels) {
			return RegisterResponse{}
		}
		return RegisterResponse{
			Error: fmt.Sprintf("metric %q already registered with different type or labels", msg.Name),
		}
	}

	nodeName := string(a.Node().Name())
	constLabels := prometheus.Labels{"node": nodeName}

	rm := &registeredMetric{
		name:         msg.Name,
		metricType:   msg.Type,
		labelNames:   msg.Labels,
		registeredBy: from,
	}

	hasLabels := len(msg.Labels) > 0

	switch msg.Type {
	case MetricGauge:
		if hasLabels {
			vec := prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name:        msg.Name,
				Help:        msg.Help,
				ConstLabels: constLabels,
			}, msg.Labels)
			rm.gaugeVec = vec
			rm.collector = vec
			break
		}
		g := prometheus.NewGauge(prometheus.GaugeOpts{
			Name:        msg.Name,
			Help:        msg.Help,
			ConstLabels: constLabels,
		})
		rm.gauge = g
		rm.collector = g

	case MetricCounter:
		if hasLabels {
			vec := prometheus.NewCounterVec(prometheus.CounterOpts{
				Name:        msg.Name,
				Help:        msg.Help,
				ConstLabels: constLabels,
			}, msg.Labels)
			rm.counterVec = vec
			rm.collector = vec
			break
		}
		c := prometheus.NewCounter(prometheus.CounterOpts{
			Name:        msg.Name,
			Help:        msg.Help,
			ConstLabels: constLabels,
		})
		rm.counter = c
		rm.collector = c

	case MetricHistogram:
		buckets := msg.Buckets
		if buckets == nil {
			buckets = prometheus.DefBuckets
		}
		if hasLabels {
			vec := prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Name:        msg.Name,
				Help:        msg.Help,
				ConstLabels: constLabels,
				Buckets:     buckets,
			}, msg.Labels)
			rm.histogramVec = vec
			rm.collector = vec
			break
		}
		h := prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:        msg.Name,
			Help:        msg.Help,
			ConstLabels: constLabels,
			Buckets:     buckets,
		})
		rm.histogram = h
		rm.collector = h

	default:
		return RegisterResponse{
			Error: fmt.Sprintf("unknown metric type: %d", msg.Type),
		}
	}

	if err := a.registry.Register(rm.collector); err != nil {
		return RegisterResponse{Error: err.Error()}
	}

	cm.Store(msg.Name, rm)
	a.MonitorPID(from)

	return RegisterResponse{}
}

func (a *Actor) handleUnregister(msg MessageUnregister) {
	cm := a.customMetrics()
	if v, loaded := cm.Load(msg.Name); loaded {
		m := v.(*registeredMetric)
		if m.internal {
			return
		}
		cm.Delete(msg.Name)
		a.registry.Unregister(m.collector)
	}
}

func (a *Actor) handleGaugeSet(msg MessageGaugeSet) {
	cm := a.customMetrics()
	v, ok := cm.Load(msg.Name)
	if ok == false {
		a.Log().Warning("metrics: gauge %q not found", msg.Name)
		return
	}
	m := v.(*registeredMetric)
	if m.metricType != MetricGauge {
		a.Log().Warning("metrics: %q is not a gauge", msg.Name)
		return
	}
	if m.gaugeVec != nil {
		m.gaugeVec.WithLabelValues(msg.Labels...).Set(msg.Value)
		return
	}
	if m.gauge != nil {
		m.gauge.Set(msg.Value)
	}
}

func (a *Actor) handleGaugeAdd(msg MessageGaugeAdd) {
	cm := a.customMetrics()
	v, ok := cm.Load(msg.Name)
	if ok == false {
		a.Log().Warning("metrics: gauge %q not found", msg.Name)
		return
	}
	m := v.(*registeredMetric)
	if m.metricType != MetricGauge {
		a.Log().Warning("metrics: %q is not a gauge", msg.Name)
		return
	}
	if m.gaugeVec != nil {
		m.gaugeVec.WithLabelValues(msg.Labels...).Add(msg.Value)
		return
	}
	if m.gauge != nil {
		m.gauge.Add(msg.Value)
	}
}

func (a *Actor) handleCounterAdd(msg MessageCounterAdd) {
	cm := a.customMetrics()
	v, ok := cm.Load(msg.Name)
	if ok == false {
		a.Log().Warning("metrics: counter %q not found", msg.Name)
		return
	}
	m := v.(*registeredMetric)
	if m.metricType != MetricCounter {
		a.Log().Warning("metrics: %q is not a counter", msg.Name)
		return
	}
	if m.counterVec != nil {
		m.counterVec.WithLabelValues(msg.Labels...).Add(msg.Value)
		return
	}
	if m.counter != nil {
		m.counter.Add(msg.Value)
	}
}

func (a *Actor) handleHistogramObserve(msg MessageHistogramObserve) {
	cm := a.customMetrics()
	v, ok := cm.Load(msg.Name)
	if ok == false {
		a.Log().Warning("metrics: histogram %q not found", msg.Name)
		return
	}
	m := v.(*registeredMetric)
	if m.metricType != MetricHistogram {
		a.Log().Warning("metrics: %q is not a histogram", msg.Name)
		return
	}
	if m.histogramVec != nil {
		m.histogramVec.WithLabelValues(msg.Labels...).Observe(msg.Value)
		return
	}
	if m.histogram != nil {
		m.histogram.Observe(msg.Value)
	}
}

func (a *Actor) handleProcessDown(msg gen.MessageDownPID) bool {
	found := false
	cm := a.customMetrics()
	cm.Range(func(key, value any) bool {
		m := value.(*registeredMetric)
		if m.internal {
			return true
		}
		if m.registeredBy == msg.PID {
			found = true
			cm.Delete(key)
			a.registry.Unregister(m.collector)
		}
		return true
	})
	return found
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (a *Actor) inspect(from gen.PID, item ...string) map[string]string {
	// our base inspect data
	result := a.HandleInspect(from, item...)
	// user's inspect data on top
	for k, v := range a.behavior.HandleInspect(from, item...) {
		result[k] = v
	}
	return result
}

func (a *Actor) ProcessTerminate(reason error) {
	a.behavior.Terminate(reason)
}

//
// default callbacks for ActorBehavior interface
//

// Init
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

// HandleMessage
func (a *Actor) HandleMessage(from gen.PID, message any) error {
	return nil
}

// HandleCall
func (a *Actor) HandleCall(from gen.PID, ref gen.Ref, message any) (any, error) {
	return nil, nil
}

// HandleEvent
func (a *Actor) HandleEvent(event gen.MessageEvent) error {
	return nil
}

// HandleInspect
func (a *Actor) HandleInspect(from gen.PID, item ...string) map[string]string {
	result := make(map[string]string)

	// Gather all metric families from the registry
	metricFamilies, err := a.registry.Gather()
	if err != nil {
		result["error"] = err.Error()
		return result
	}

	// Add general information
	result["total_metrics"] = fmt.Sprintf("%d", len(metricFamilies))
	if a.options.Mux != nil {
		result["http_path"] = a.options.Path
	} else if a.options.Port > 0 {
		result["http_endpoint"] = fmt.Sprintf("http://%s:%d%s", a.options.Host, a.options.Port, a.options.Path)
	}
	result["collect_interval"] = a.options.CollectInterval.String()

	// Count custom metrics (non-internal)
	customCount := 0
	a.customMetrics().Range(func(_, v any) bool {
		m := v.(*registeredMetric)
		if m.internal == false {
			customCount++
		}
		return true
	})
	result["custom_metrics"] = fmt.Sprintf("%d", customCount)

	// Add each metric with its current values
	for _, mf := range metricFamilies {
		var values []string
		for _, m := range mf.GetMetric() {
			switch mf.GetType() {
			case 0: // COUNTER
				if m.Counter != nil {
					values = append(values, fmt.Sprintf("%.2f", m.Counter.GetValue()))
				}
			case 1: // GAUGE
				if m.Gauge != nil {
					values = append(values, fmt.Sprintf("%.2f", m.Gauge.GetValue()))
				}
			case 2: // SUMMARY
				if m.Summary != nil {
					values = append(
						values,
						fmt.Sprintf("count=%d sum=%.2f", m.Summary.GetSampleCount(), m.Summary.GetSampleSum()),
					)
				}
			case 4: // HISTOGRAM
				if m.Histogram != nil {
					values = append(
						values,
						fmt.Sprintf("count=%d sum=%.2f", m.Histogram.GetSampleCount(), m.Histogram.GetSampleSum()),
					)
				}
			}
		}
		result[mf.GetName()] = strings.Join(values, ", ")
	}

	return result
}

// CollectMetrics
func (a *Actor) CollectMetrics() error {
	return nil
}

// Terminate
func (a *Actor) Terminate(reason error) {}
