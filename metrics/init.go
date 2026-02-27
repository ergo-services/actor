package metrics

import (
	"ergo.services/ergo/gen"
	"ergo.services/ergo/net/edf"
)

func init() {
	types := []any{
		MetricType(0),
		RegisterRequest{},
		RegisterResponse{},
		MessageUnregister{},
		MessageGaugeSet{},
		MessageGaugeAdd{},
		MessageCounterAdd{},
		MessageHistogramObserve{},
	}

	for _, t := range types {
		err := edf.RegisterTypeOf(t)
		if err == nil || err == gen.ErrTaken {
			continue
		}
		panic(err)
	}
}
