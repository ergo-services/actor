package metrics

// Every collector is begin / observe / flush over per-process stats, and observe is where
// the arithmetic and the skip rules live. Those rules are what decide whether a panel shows
// a process at all, so each one is pinned here: a filter that stops skipping turns idle
// processes into rows, and a derivation that drifts moves every value on the chart.
//
// The collect cycle reaches these only on a node with processes, which the mock does not
// invent - so they are driven directly, in-package.

import (
	"sync"
	"testing"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/testing/check"

	"github.com/prometheus/client_golang/prometheus"
)

// collectorFixture wires one collector the way initializeErgoMetrics does.
func collectorFixture() (*sync.Map, *prometheus.Registry, prometheus.Labels) {
	return &sync.Map{}, prometheus.NewRegistry(), prometheus.Labels{"node": "test@host"}
}

// Throughput ranks by message counts, and a process that moved nothing is not a data point:
// counting it would push real traffic out of a top-N.
func TestThroughput_ObservesOnlyMovedMessages(t *testing.T) {
	cm, reg, labels := collectorFixture()
	tm := &throughputMetrics{}
	tm.init(cm, reg, labels)

	tm.begin()
	tm.observe(gen.ProcessShortInfo{PID: gen.PID{ID: 1}, MessagesIn: 5, MessagesOut: 0}, 10)
	tm.observe(gen.ProcessShortInfo{PID: gen.PID{ID: 2}, MessagesIn: 0, MessagesOut: 3}, 10)
	tm.observe(gen.ProcessShortInfo{PID: gen.PID{ID: 3}}, 10) // silent: neither direction

	check.Equal(t, 1, tm.heapIn.Len())
	check.Equal(t, 1, tm.heapOut.Len())
	tm.flush()
}

// Utilization is RunningTime over Uptime, and the clamp matters: a process reporting more
// running time than uptime must read as fully busy rather than as an impossible 1.4.
func TestUtilization_DerivesClampsAndSkips(t *testing.T) {
	cm, reg, labels := collectorFixture()
	um := &utilizationMetrics{}
	um.init(cm, reg, labels)

	um.begin()
	// Uptime is seconds, RunningTime nanoseconds: half a second of work in one second.
	um.observe(gen.ProcessShortInfo{PID: gen.PID{ID: 1}, Uptime: 1, RunningTime: 5e8}, 10)
	check.Equal(t, 0.5, um.max)

	// Over-reported running time clamps to 1.0 instead of exceeding it.
	um.observe(gen.ProcessShortInfo{PID: gen.PID{ID: 2}, Uptime: 1, RunningTime: 14e8}, 10)
	check.Equal(t, 1.0, um.max)

	// No uptime yet, or no work done: not a data point.
	um.observe(gen.ProcessShortInfo{PID: gen.PID{ID: 3}, Uptime: 0, RunningTime: 1e9}, 10)
	um.observe(gen.ProcessShortInfo{PID: gen.PID{ID: 4}, Uptime: 10, RunningTime: 0}, 10)
	check.Equal(t, 2, um.heap.Len())
	um.flush()
}

// The drain ratio is messages per wakeup - the signal for a process woken far more often
// than it has work for - so it is only meaningful once both numbers exist.
func TestWakeups_RanksWakeupsAndDrainRatio(t *testing.T) {
	cm, reg, labels := collectorFixture()
	wm := &wakeupsMetrics{}
	wm.init(cm, reg, labels)

	wm.begin()
	wm.observe(gen.ProcessShortInfo{PID: gen.PID{ID: 1}, Wakeups: 10, MessagesIn: 100}, 10)
	// Woken with nothing to show for it: ranked by wakeups, but no ratio.
	wm.observe(gen.ProcessShortInfo{PID: gen.PID{ID: 2}, Wakeups: 10, MessagesIn: 0}, 10)
	// Never woken: not a data point at all.
	wm.observe(gen.ProcessShortInfo{PID: gen.PID{ID: 3}, Wakeups: 0, MessagesIn: 5}, 10)

	check.Equal(t, 2, wm.heapWakeups.Len())
	check.Equal(t, 1, wm.heapDrains.Len())
	wm.flush()
}

// Init time is reported in seconds while the runtime counts nanoseconds; a process that
// never recorded one is skipped rather than charted as instantaneous.
func TestInitTime_ConvertsAndSkipsUnrecorded(t *testing.T) {
	cm, reg, labels := collectorFixture()
	im := &initTimeMetrics{}
	im.init(cm, reg, labels)

	im.begin()
	im.observe(gen.ProcessShortInfo{PID: gen.PID{ID: 1}, InitTime: 25e8}, 10) // 2.5s
	im.observe(gen.ProcessShortInfo{PID: gen.PID{ID: 2}, InitTime: 0}, 10)

	check.Equal(t, uint64(25e8), im.max)
	check.Equal(t, 1, im.heap.Len())
	im.flush()
}

// Liveness only means something for a process that has been up a while, has work, and has a
// measured mailbox latency - and never for a zombie. Each filter is a row that would
// otherwise be noise at the top of the panel.
func TestLiveness_SkipsEverythingItCannotJudge(t *testing.T) {
	cm, reg, labels := collectorFixture()
	lm := &livenessMetrics{}
	lm.init(cm, reg, labels)

	healthy := gen.ProcessShortInfo{
		PID: gen.PID{ID: 1}, Uptime: 120, MessagesIn: 10,
		MailboxLatency: 1000, RunningTime: 6e9, State: gen.ProcessStateRunning,
	}

	lm.begin()
	lm.observe(healthy, 10)
	check.Equal(t, 1, lm.heap.Len())

	for _, skipped := range []gen.ProcessShortInfo{
		{PID: gen.PID{ID: 2}, Uptime: 59, MessagesIn: 10, MailboxLatency: 1000, RunningTime: 6e9},
		{PID: gen.PID{ID: 3}, Uptime: 120, MessagesIn: 0, MailboxLatency: 1000, RunningTime: 6e9},
		{PID: gen.PID{ID: 4}, Uptime: 120, MessagesIn: 10, MailboxLatency: 0, RunningTime: 6e9},
		{
			PID: gen.PID{ID: 5}, Uptime: 120, MessagesIn: 10, MailboxLatency: 1000,
			RunningTime: 6e9, State: gen.ProcessStateZombee,
		},
	} {
		lm.observe(skipped, 10)
	}
	check.Equal(t, 1, lm.heap.Len())
	lm.flush()
}

// An event's state is a classification, and the five buckets are mutually exclusive: a
// misclassified event is one an operator stops looking for.
func TestEvent_ClassifiesUtilizationStates(t *testing.T) {
	cm, reg, labels := collectorFixture()
	em := &eventMetrics{}
	em.init(cm, reg, labels)

	em.begin()
	for _, tc := range []struct {
		name  string
		info  gen.EventInfo
		index int
	}{
		{"active", gen.EventInfo{MessagesPublished: 5, Subscribers: 2}, 0},
		{"on_demand", gen.EventInfo{Notify: true}, 1},
		{"idle", gen.EventInfo{}, 2},
		{"no_subscribers", gen.EventInfo{MessagesPublished: 5}, 3},
		{"no_publishing", gen.EventInfo{Subscribers: 3}, 4},
	} {
		before := em.utilCounts[tc.index]
		em.observe(tc.info, 10)
		if em.utilCounts[tc.index] != before+1 {
			t.Fatalf("%s was not counted in bucket %d: %v", tc.name, tc.index, em.utilCounts)
		}
	}

	// Subscribers is a high-water mark across the cycle.
	check.Equal(t, int64(3), em.max)
	// Only the events that actually published or delivered are ranked.
	check.Equal(t, 2, em.heapPublished.Len())
	em.flush()
}
