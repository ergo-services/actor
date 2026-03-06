package metrics

import (
	"fmt"
	"time"

	"ergo.services/ergo/act"
	"ergo.services/ergo/gen"

	"github.com/prometheus/client_golang/prometheus"
)

// TopNActorOptions holds configuration for a top-N metric actor.
type TopNActorOptions struct {
	Name     string
	Help     string
	Labels   []string
	TopN     int
	Order    TopNOrder
	Shared   *Shared
	Interval time.Duration
	Owner    gen.PID
}

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
	shared   *Shared
	interval time.Duration
	owner    gen.PID
}

type messageFlush struct{}

func (a *topNActor) Init(args ...any) error {
	if len(args) < 1 {
		return fmt.Errorf("topn actor: expected TopNActorOptions arg")
	}
	opts, ok := args[0].(TopNActorOptions)
	if ok == false {
		return fmt.Errorf("topn actor: expected TopNActorOptions, got %T", args[0])
	}

	a.metricName = opts.Name
	a.help = opts.Help
	a.labels = opts.Labels
	a.topN = opts.TopN
	a.order = opts.Order
	a.shared = opts.Shared
	a.interval = opts.Interval
	a.owner = opts.Owner

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

	if err := a.shared.registry.Register(gv); err != nil {
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
		a.shared.registry.Unregister(a.gaugeVec)
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
		a.shared.registry.Unregister(a.gaugeVec)
	}
}
