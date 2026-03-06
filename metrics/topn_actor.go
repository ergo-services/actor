package metrics

import (
	"fmt"
	"time"

	"ergo.services/ergo/act"
	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

// TopNActorFactory returns a new topN actor instance.
func TopNActorFactory() gen.ProcessBehavior {
	return &topNActor{}
}

type topNActor struct {
	act.Actor

	metricName string
	help       string
	labels     []string
	topN       int
	order      TopNOrder

	heap     *topNHeap
	gaugeVec *prometheus.GaugeVec
	registry *prometheus.Registry
	interval time.Duration
	owner    gen.PID
}

type messageFlush struct{}

func (a *topNActor) Init(args ...any) error {
	// args: name, help, labels, topN, order, registry, interval, owner
	if len(args) < 8 {
		return fmt.Errorf("topn actor: expected 8 args, got %d", len(args))
	}

	a.metricName = args[0].(string)
	a.help = args[1].(string)
	a.labels = args[2].([]string)
	a.topN = args[3].(int)
	a.order = args[4].(TopNOrder)
	a.registry = args[5].(*prometheus.Registry)
	a.interval = args[6].(time.Duration)
	a.owner = args[7].(gen.PID)

	// register process name for direct addressing
	if err := a.RegisterName(gen.Atom("radar_topn_" + a.metricName)); err != nil {
		return fmt.Errorf("topn actor: register name: %w", err)
	}

	// create GaugeVec
	nodeName := string(a.Node().Name())
	constLabels := prometheus.Labels{"node": nodeName}

	gv := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name:        a.metricName,
		Help:        a.help,
		ConstLabels: constLabels,
	}, a.labels)

	if err := a.registry.Register(gv); err != nil {
		// handle restart: reuse already-registered collector
		existing, ok := err.(prometheus.AlreadyRegisteredError)
		if ok == false {
			return fmt.Errorf("topn actor: register gauge: %w", err)
		}
		gv, ok = existing.ExistingCollector.(*prometheus.GaugeVec)
		if ok == false {
			return fmt.Errorf("topn actor: existing collector is not *GaugeVec")
		}
	}
	a.gaugeVec = gv

	a.heap = newTopNHeap(a.order)

	// monitor the owner process
	a.MonitorPID(a.owner)

	// schedule first flush
	a.SendAfter(a.PID(), messageFlush{}, a.interval)

	return nil
}

func (a *topNActor) HandleMessage(from gen.PID, message any) error {
	switch msg := message.(type) {
	case MessageTopNObserve:
		a.heap.Observe(msg.Value, msg.Labels, a.topN)

	case messageFlush:
		a.heap.Flush(a.gaugeVec)
		a.SendAfter(a.PID(), messageFlush{}, a.interval)

	case gen.MessageDownPID:
		// owner terminated, clean up
		a.registry.Unregister(a.gaugeVec)
		return gen.TerminateReasonNormal
	}

	return nil
}

func (a *topNActor) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	return nil, nil
}

func (a *topNActor) Terminate(reason error) {
	if reason == gen.TerminateReasonNormal || reason == gen.TerminateReasonShutdown {
		// already unregistered in HandleMessage for owner-down,
		// or shutting down gracefully -- leave gauge for restart reuse
		return
	}
	// abnormal termination: unregister to allow clean re-registration
	if a.gaugeVec != nil {
		a.registry.Unregister(a.gaugeVec)
	}
}
