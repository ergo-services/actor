package leader

import (
	"ergo.services/ergo/gen"
	"ergo.services/ergo/net/edf"
)

func init() {
	types := []any{
		msgVote{},
		msgVoteReply{},
		msgHeartbeat{},
	}

	for _, t := range types {
		err := edf.RegisterTypeOf(t)
		if err == nil || err == gen.ErrTaken {
			continue
		}
		panic(err)
	}
}
