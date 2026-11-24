package api

// PermsEdit is what an owner sets on a node's record (`PUT /perms`). A node with no record of
// its own gets one — the place is taken by its owner, holding out what the default above says
// — and then: `child-grants` sets what a node created beneath holds out (`{}` nothing, null
// inherits again), `grants` replaces what the node itself holds out (`[]` nothing), `grant`
// adds one line at the node, beneath it when `below` is set, and `revoke` takes the named
// grantees off the named levels of every line at the node, leaving the rest standing.
//
// AnswerFor is POST /resolvers/{name}/grant and /revoke: whom the resolver answers for. For is
// the place, as a kinded path; absent, it is the caller, at `viff.for.<caller>`.
//
//tony:schemagen=viff-answer-for,notag
type AnswerFor struct {
	For string `tony:"field=for,omitzero"`
}
