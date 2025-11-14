package metrics

import (
	"fmt"
	"net/http"
	"reflect"
	"runtime"
	"strings"

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

// Actor implements gen.ProcessBehavior
type Actor struct {
	gen.Process

	behavior ActorBehavior
	mailbox  gen.ProcessMailbox

	options Options

	registry *prometheus.Registry

	// Node metrics
	nodeUptime          prometheus.Gauge
	processesTotal      prometheus.Gauge
	processesRunning    prometheus.Gauge
	processesZombie     prometheus.Gauge
	memoryUsed          prometheus.Gauge
	memoryAlloc         prometheus.Gauge
	userTime            prometheus.Gauge
	systemTime          prometheus.Gauge
	applicationsTotal   prometheus.Gauge
	applicationsRunning prometheus.Gauge
	registeredNames     prometheus.Gauge
	registeredAliases   prometheus.Gauge
	registeredEvents    prometheus.Gauge

	// Network metrics
	connectedNodes    prometheus.Gauge
	remoteNodeUptime  *prometheus.GaugeVec
	remoteMessagesIn  *prometheus.GaugeVec
	remoteMessagesOut *prometheus.GaugeVec
	remoteBytesIn     *prometheus.GaugeVec
	remoteBytesOut    *prometheus.GaugeVec
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

	a.registry = prometheus.NewRegistry()

	// Initialize base Prometheus metrics
	if err := a.initializeMetrics(); err != nil {
		return err
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
	if a.options.CollectInterval < 1 {
		a.options.CollectInterval = DefaultCollectInterval
	}

	// Start HTTP server for /metrics endpoint
	if err := a.startHTTPServer(); err != nil {
		return err
	}

	a.Send(a.PID(), messageCollectMetrics{})

	return nil
}

func (a *Actor) initializeMetrics() error {
	// Node metrics
	a.nodeUptime = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ergo_node_uptime_seconds",
		Help: "Node uptime in seconds",
	})
	a.processesTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ergo_processes_total",
		Help: "Total number of processes",
	})
	a.processesRunning = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ergo_processes_running",
		Help: "Number of running processes",
	})
	a.processesZombie = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ergo_processes_zombie",
		Help: "Number of zombie processes",
	})
	a.memoryUsed = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ergo_memory_used_bytes",
		Help: "Memory used in bytes",
	})
	a.memoryAlloc = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ergo_memory_alloc_bytes",
		Help: "Memory allocated in bytes",
	})
	a.userTime = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ergo_cpu_user_seconds",
		Help: "User CPU time in seconds",
	})
	a.systemTime = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ergo_cpu_system_seconds",
		Help: "System CPU time in seconds",
	})
	a.applicationsTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ergo_applications_total",
		Help: "Total number of applications",
	})
	a.applicationsRunning = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ergo_applications_running",
		Help: "Number of running applications",
	})
	a.registeredNames = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ergo_registered_names_total",
		Help: "Total number of registered names",
	})
	a.registeredAliases = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ergo_registered_aliases_total",
		Help: "Total number of registered aliases",
	})
	a.registeredEvents = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ergo_registered_events_total",
		Help: "Total number of registered events",
	})

	// Network metrics
	a.connectedNodes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ergo_connected_nodes_total",
		Help: "Total number of connected nodes",
	})
	a.remoteNodeUptime = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ergo_remote_node_uptime_seconds",
			Help: "Remote node uptime in seconds",
		},
		[]string{"node"},
	)
	a.remoteMessagesIn = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ergo_remote_messages_in_total",
			Help: "Total number of messages received from remote node",
		},
		[]string{"node"},
	)
	a.remoteMessagesOut = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ergo_remote_messages_out_total",
			Help: "Total number of messages sent to remote node",
		},
		[]string{"node"},
	)
	a.remoteBytesIn = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ergo_remote_bytes_in_total",
			Help: "Total number of bytes received from remote node",
		},
		[]string{"node"},
	)
	a.remoteBytesOut = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ergo_remote_bytes_out_total",
			Help: "Total number of bytes sent to remote node",
		},
		[]string{"node"},
	)

	// Register all base metrics
	a.registry.MustRegister(
		a.nodeUptime,
		a.processesTotal,
		a.processesRunning,
		a.processesZombie,
		a.memoryUsed,
		a.memoryAlloc,
		a.userTime,
		a.systemTime,
		a.applicationsTotal,
		a.applicationsRunning,
		a.registeredNames,
		a.registeredAliases,
		a.registeredEvents,
		a.connectedNodes,
		a.remoteNodeUptime,
		a.remoteMessagesIn,
		a.remoteMessagesOut,
		a.remoteBytesIn,
		a.remoteBytesOut,
	)

	return nil
}

func (a *Actor) startHTTPServer() error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(a.registry, promhttp.HandlerOpts{}))

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

	a.Log().Debug("metrics HTTP server started at http://%s:%d/metrics", a.options.Host, a.options.Port)
	return nil
}

func (a *Actor) collectBaseMetrics() error {
	// Get node information
	nodeInfo, err := a.Node().Info()
	if err != nil {
		return fmt.Errorf("failed to get node info: %w", err)
	}

	// Update node metrics
	a.nodeUptime.Set(float64(nodeInfo.Uptime))
	a.processesTotal.Set(float64(nodeInfo.ProcessesTotal))
	a.processesRunning.Set(float64(nodeInfo.ProcessesRunning))
	a.processesZombie.Set(float64(nodeInfo.ProcessesZombee))
	a.memoryUsed.Set(float64(nodeInfo.MemoryUsed))
	a.memoryAlloc.Set(float64(nodeInfo.MemoryAlloc))
	a.userTime.Set(float64(nodeInfo.UserTime) / 1e9)
	a.systemTime.Set(float64(nodeInfo.SystemTime) / 1e9)
	a.applicationsTotal.Set(float64(nodeInfo.ApplicationsTotal))
	a.applicationsRunning.Set(float64(nodeInfo.ApplicationsRunning))
	a.registeredNames.Set(float64(nodeInfo.RegisteredNames))
	a.registeredAliases.Set(float64(nodeInfo.RegisteredAliases))
	a.registeredEvents.Set(float64(nodeInfo.RegisteredEvents))

	// Get network information
	network := a.Node().Network()
	connectedNodes := network.Nodes()

	// Update network metrics
	a.connectedNodes.Set(float64(len(connectedNodes)))

	// Reset remote node metrics before updating
	a.remoteNodeUptime.Reset()
	a.remoteMessagesIn.Reset()
	a.remoteMessagesOut.Reset()
	a.remoteBytesIn.Reset()
	a.remoteBytesOut.Reset()

	// Update per-node metrics
	for _, nodeName := range connectedNodes {
		remoteNode, err := network.Node(nodeName)
		if err != nil {
			a.Log().Warning("failed to get remote node %s: %s", nodeName, err)
			continue
		}

		remoteInfo := remoteNode.Info()
		nodeNameStr := string(nodeName)
		a.remoteNodeUptime.WithLabelValues(nodeNameStr).Set(float64(remoteInfo.Uptime))
		a.remoteMessagesIn.WithLabelValues(nodeNameStr).Set(float64(remoteInfo.MessagesIn))
		a.remoteMessagesOut.WithLabelValues(nodeNameStr).Set(float64(remoteInfo.MessagesOut))
		a.remoteBytesIn.WithLabelValues(nodeNameStr).Set(float64(remoteInfo.BytesIn))
		a.remoteBytesOut.WithLabelValues(nodeNameStr).Set(float64(remoteInfo.BytesOut))
	}

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
			_, isCollectMetrics := message.Message.(messageCollectMetrics)
			if isCollectMetrics {
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
				continue
			}

			if err := a.behavior.HandleMessage(message.From, message.Message); err != nil {
				return err
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

func (a *Actor) ProcessTerminate(reason error) {
	a.behavior.Terminate(reason)
}

//
// default callbacks for ActorBehavior interface
//

// Init
func (a *Actor) Init(args ...any) (Options, error) {
	return Options{}, nil
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
	result["http_endpoint"] = fmt.Sprintf("http://%s:%d/metrics", a.options.Host, a.options.Port)
	result["collect_interval"] = a.options.CollectInterval.String()

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

// Registry returns the prometheus registry for registering custom metrics
func (a *Actor) Registry() *prometheus.Registry {
	return a.registry
}
