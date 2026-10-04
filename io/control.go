package io

import (
	"context"

	"github.com/signadot/verse/client"
)

// The third direction, and the one with the sharp edge.
//
// A reflection reads the external system and writes the tree. An out half reads the tree and
// writes the external system. A CONTROLLER does neither: it turns what people do over there —
// press, reply, mention, mail — into a control request here, made AS THE PERSON WHO DID IT.
//
// It is not a store write and must never become one. "An intervention is never a store write" is
// the rule the whole control surface rests on (docs/control.md): an answer settles a gate through
// the route that checks who may settle it, and a program that instead wrote the record would be a
// program that answers gates on everybody's behalf. So a controller is handed a way to ACT as
// somebody, and nothing that writes.
//
// THREE INVARIANTS, and a connector that breaks any of them is broken rather than clever:
//
//   - WHICH request is decided without reading prose. The Slack io knows which question a reply
//     answers because it posted the message that reply is under, not because the words say so.
//   - WHAT it may ask for is bounded by what the record already offers — the options on a
//     question, and nothing invented. A press, a word or a sentence chooses among them or is
//     refused.
//   - WHO it is made as is the person, established by the external system (a Slack id on a
//     socket the app token opened, an authenticated sender), never the io's own principal. An
//     answer recorded as `agent:slack-io` would be an answer nobody gave.
//
// What makes the person's name more than an assertion is the connector's own credential beside
// it: a verse that verifies people admits the request through the person's account binding, from
// a connector the deployment trusts for that provider, and records which connector it came
// through. A connector with no credential asserts the person, as a person typing `--as` does.
type Controller interface {
	// Control runs until ctx is done. `as` answers a client acting as one person, named with the
	// account the external system knows them by (`<provider>:<id>`), which is how a connector
	// makes a request on somebody's behalf without holding their credentials or being able to
	// forge a write.
	Control(ctx context.Context, as func(principal, account string) *client.Client) error
}

// As is the client factory a controller is given: one verse, the connector's own bearer, and any
// person with their account.
//
// A FUNCTION RATHER THAN A CLIENT, because the person is the request's and not the program's.
// Handed a client, a connector would have one identity and would have to choose between acting as
// itself (wrong) and holding credentials for everybody (worse). Handed this, it acts as whoever
// the external system says did the thing, one request at a time, and says which account that
// was. With a bearer the verse verifies the connector and admits the person through the
// account's binding (docs/reference/perms.md, "a person relayed by a trusted io"); without one
// the person is asserted, as the CLI's `--as` is.
func As(addr string, bearer client.BearerFunc) func(principal, account string) *client.Client {
	return func(principal, account string) *client.Client {
		return client.NewRelay(addr, principal, account, bearer)
	}
}
