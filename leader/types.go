package leader

// NetworkTypes returns the wire types this actor exchanges with its peers.
//
// Registering them is the caller's job, not the library's, and it must happen before
// the node carries any traffic. Type registration is node-scoped, so it cannot be done
// from a package init(); doing it in the actor's own Init would be too late, because a
// node whose leader process starts after a connection is already established would be
// unable to decode a peer's message and could not fix that retroactively.
//
// Either declare them on the application that hosts the actor, which is processed
// during ApplicationLoad before any of its processes are spawned:
//
//	gen.ApplicationSpec{
//	    Network: gen.ApplicationNetwork{
//	        RegisterTypes:  leader.NetworkTypes(),
//	        RegisterErrors: leader.ErrorTypes(),
//	    },
//	}
//
// or register them on the node directly before it starts serving:
//
//	node.Network().RegisterTypes(leader.NetworkTypes())
//	node.Network().RegisterErrors(leader.ErrorTypes())
//
// Init refuses to start if the node does not know these types: without them every vote
// fails to encode, so the alternative is a healthy-looking node that never joins an
// election.
func NetworkTypes() []any {
	return []any{
		msgVote{},
		msgVoteReply{},
		msgHeartbeat{},
	}
}

// ErrorTypes returns the sentinel errors this actor sends over the wire. There are none
// today; the function exists so consumer setup can stay uniform and keep working if
// that changes.
func ErrorTypes() []error {
	return nil
}
