package leader

// NetworkTypes returns the wire types this actor exchanges with its peers. Registering
// them is the caller's job and must happen before the node carries any traffic - the
// actor cannot do it later without the types missing the wire. Init reports the
// omission; see the package documentation for where to declare them.
func NetworkTypes() []any {
	return []any{
		msgVote{},
		msgVoteReply{},
		msgHeartbeat{},
	}
}

// ErrorTypes returns the sentinel errors this actor sends over the wire. None today.
func ErrorTypes() []error {
	return nil
}
