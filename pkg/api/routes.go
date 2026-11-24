package api

// Route is one entry of the API's own route table (`GET /api`): a method and path, the
// signature names of what it accepts and returns, and how many of them come back.
//
// An empty Request is a route with no body (a GET, or a control POST that takes none). An
// empty Response means the response shape is NOT YET a named type — most handlers still
// assemble map literals. That absence is deliberate and load-bearing: it makes the remaining
// naming work visible in the published artifact rather than hiding it behind a claim like
// "returns an open value", which would be true of anything and useful about nothing.
//
// Response names ONE document; a name is all it can carry. Returns says how many arrive and by
// what means, because the name alone could not: `GET /triggers` published `response:
// viff-trigger` while its body is an ARRAY of them, and `GET /watch` published the same claim
// for a thing that is not a document at all but an SSE stream of them. Both read as "returns a
// viff-trigger" / "returns a viff-event", which is wrong in the way a published signature
// must never be wrong — confidently, in the artifact that exists to be trusted about the wire.
//
// Returns is also what makes a route COMPARABLE to the schema. The root (internal/schema/root.tony)
// types each top-level path — `state: .[viff-state]` — and is written by hand,
// beholden to no route table; a route's response and returns together say the same thing in the
// same terms, which is what lets a test hold the two accounts of one surface equal. See
// TestTheRootIsTheServedSurface.
//
//tony:schemagen=viff-route,notag
type Route struct {
	Method  string `tony:"field=method"`
	Path    string `tony:"field=path"`
	Request string `tony:"field=request,omitzero"`
	// Response is a signature name, or a tony base type where that is the whole truth:
	// a route that answers with plain strings says `string`, and there is nothing further to name.
	Response string `tony:"field=response,omitzero"`
	Returns  string `tony:"field=returns,omitzero"`
}

// The values of Route.Returns: how many Response documents come back, and by what means.
//
// Empty is the fourth case and means nothing worth naming came back — a route that answers
// with no body, or one whose shape is not named yet. Every route with a Response has one of
// these, and a route without one has neither (TestEveryRouteSaysWhatItReturns).
const (
	// ReturnsOne is one document, alone, as the body.
	ReturnsOne = "one"
	// ReturnsList is an array of them.
	ReturnsList = "list"
	// ReturnsStream is a sequence of them over server-sent events — frames, not a document,
	// so the body has no tony type even though each frame's payload does.
	ReturnsStream = "stream"
)

// Routes is the whole table, as served.
//
//tony:schemagen=viff-routes,notag
type Routes struct {
	Routes []Route `tony:"field=routes"`
}
