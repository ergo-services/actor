package metrics

import (
	"fmt"

	"ergo.services/ergo/gen"
)

// RegisterGauge registers a gauge metric on the target metrics actor (sync Call).
func RegisterGauge(process gen.Process, to any, name, help string, labels []string) error {
	return registerMetric(process, to, RegisterRequest{
		Name:   name,
		Help:   help,
		Type:   MetricGauge,
		Labels: labels,
	})
}

// RegisterCounter registers a counter metric on the target metrics actor (sync Call).
func RegisterCounter(process gen.Process, to any, name, help string, labels []string) error {
	return registerMetric(process, to, RegisterRequest{
		Name:   name,
		Help:   help,
		Type:   MetricCounter,
		Labels: labels,
	})
}

// RegisterHistogram registers a histogram metric on the target metrics actor (sync Call).
// Pass nil for buckets to use prometheus.DefBuckets.
func RegisterHistogram(process gen.Process, to any, name, help string, labels []string, buckets []float64) error {
	return registerMetric(process, to, RegisterRequest{
		Name:    name,
		Help:    help,
		Type:    MetricHistogram,
		Labels:  labels,
		Buckets: buckets,
	})
}

func registerMetric(process gen.Process, to any, req RegisterRequest) error {
	result, err := process.Call(to, req)
	if err != nil {
		return err
	}
	resp, ok := result.(RegisterResponse)
	if ok == false {
		return fmt.Errorf("unexpected response type: %T", result)
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// Unregister removes a previously registered custom metric (async Send).
func Unregister(process gen.Process, to any, name string) error {
	return process.Send(to, MessageUnregister{Name: name})
}

// GaugeSet sets the value of a registered gauge metric (async Send).
func GaugeSet(process gen.Process, to any, name string, value float64, labels []string) error {
	return process.Send(to, MessageGaugeSet{Name: name, Value: value, Labels: labels})
}

// GaugeAdd adds the value to a registered gauge metric (async Send).
func GaugeAdd(process gen.Process, to any, name string, value float64, labels []string) error {
	return process.Send(to, MessageGaugeAdd{Name: name, Value: value, Labels: labels})
}

// CounterAdd adds the value to a registered counter metric (async Send).
func CounterAdd(process gen.Process, to any, name string, value float64, labels []string) error {
	return process.Send(to, MessageCounterAdd{Name: name, Value: value, Labels: labels})
}

// HistogramObserve observes a value on a registered histogram metric (async Send).
func HistogramObserve(process gen.Process, to any, name string, value float64, labels []string) error {
	return process.Send(to, MessageHistogramObserve{Name: name, Value: value, Labels: labels})
}

// RegisterTopN registers a top-N metric on the target supervisor (sync Call).
// The supervisor spawns a dedicated actor to manage the metric.
func RegisterTopN(process gen.Process, to any, name, help string, topN int, order TopNOrder, labels []string) error {
	result, err := process.Call(to, RegisterTopNRequest{
		Name:   name,
		Help:   help,
		TopN:   topN,
		Order:  order,
		Labels: labels,
	})
	if err != nil {
		return err
	}
	resp, ok := result.(RegisterResponse)
	if ok == false {
		return fmt.Errorf("unexpected response type: %T", result)
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// TopNObserve sends a value observation to a top-N metric actor (async Send).
func TopNObserve(process gen.Process, to any, value float64, labels []string) error {
	return process.Send(to, MessageTopNObserve{Value: value, Labels: labels})
}
