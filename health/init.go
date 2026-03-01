package health

import (
	"time"

	"ergo.services/ergo/gen"
	"ergo.services/ergo/net/edf"
)

func init() {
	types := []any{
		Probe(0),
		time.Duration(0),
		RegisterRequest{},
		RegisterResponse{},
		UnregisterRequest{},
		UnregisterResponse{},
		MessageHeartbeat{},
		MessageSignalUp{},
		MessageSignalDown{},
	}

	for _, t := range types {
		err := edf.RegisterTypeOf(t)
		if err == nil || err == gen.ErrTaken {
			continue
		}
		panic(err)
	}
}
