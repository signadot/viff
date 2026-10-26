package api

import (
	"fmt"

	"github.com/signadot/verse/entity"
)

// GateSay says something ABOUT a question without answering it: "checking with ops", "this is the
// third time this week", "I would, but not before the release".
//
// A separate act from Resolve, and a separate route, because an answer carries a choice and this
// one carries none — `/asks/{id}/answer` refuses an answer that names no option, and rightly, since a
// comment that settled a gate by accident would be the worst possible failure of this model. What
// it leaves is a durable, attributed line on the question, where everyone who reads the question
// reads it, rather than in whatever chat window it was typed in.
//
//tony:schemagen=verse-gate-say,notag
type GateSay struct {
	// ID is the question said on. POST /asks/{id}/say takes it from its path, and a body naming
	// another is refused.
	ID  string `tony:"field=id"`
	By  string `tony:"field=by"`
	Say string `tony:"field=say"`
}

// Resolve answers ONE pending gate, by its id, wherever it lives — an engine gate the
// trigger registry raised, or a task gate raised inside a running agent. The routing
// is the server's problem (a task gate's id carries its owner ahead of
// task.GateSep); a caller just names the gate and says yes or no.
//
// This is the type that unified two spellings. `id`/`approve` won over
// `decisionId`/`proceed` because they are the words the CLI, `gate list`, and the docs
// already use, and because "decision id" named the same thing twice.
//
//tony:schemagen=verse-resolve,notag
type Resolve struct {
	// ID is the gate answered. POST /asks/{id}/answer takes it from its path, and a body naming
	// another is refused; POST /tasks/{id}/resolve takes it here.
	ID string `tony:"field=id"`
	// Choice is the option chosen, by id: `proceed` or `deny` on the question every gate
	// poses unless it says otherwise, or one of the record's own options where it names
	// them. A ballot naming an option the record does not carry is refused, with the
	// options listed. It was a boolean, `approve`, which could not name a third option.
	Choice string `tony:"field=choice"`
	// By is who answered. It rides the wire the same way FlowSelect.By and a say's By do,
	// and for the same reason: a resolution is an act, and an act has an actor.
	//
	// REQUIRED, and never defaulted: a default publishes a name belonging to nobody, and an
	// answer is a durable entry on the question record, so an unattributable one would be
	// permanent. Same rule as a ballot's --as, and for the same
	// reason — a decision has no meaning without whoever made it.
	By string `tony:"field=by"`
	// Say rides along with the answer as steering — "yes, but do it this way". It is a
	// list because task.Resolution.Steer is: an answer may carry several instructions,
	// and collapsing them at the boundary would lose an author's paragraphing.
	Say []string `tony:"field=say,omitzero"`
	// For is the party a resolver answers for, where the request is made as the resolver
	// (ResolverHeader): a principal, or a place's address, as GET /resolvers/{name}/asks names
	// it. It may be left out where the resolver serves the ask for one party only.
	For string `tony:"field=for,omitzero"`
	// Edit is the edit of a person's ask this answers, as the answerer was shown it. An answer
	// to an earlier edit is refused: it consented to words no longer asked. Absent is the edit
	// standing now.
	Edit int `tony:"field=edit,omitzero"`
	// Force answers as yourself an ask a resolver you granted serves for you. Without it that
	// answer is refused, naming the resolver: the party chose to have it answer.
	Force bool `tony:"field=force,omitzero"`
}

// Ask is a person asking somebody something (`verse ask`, POST /asks): what is asked, of whom, by
// when, and what follows a yes. The ask is written at `verse.ask.<id>` as its asker's record, with
// the id derived from Name, and the verb poses it (docs/asking.md).
//
// Then is one key naming what follows a yes, on the asker's authority: `{spawn: {…}}`, a task
// written as a charter's spawn is (its prompt or script, agent, environment and autonomy) and
// meaning only what it says; `{answer: {ask, choice, say?, force?}}`, an answer to another ask as
// the asker; or `{ask: {…}}`, another ask the asker raises, whose `due` may be a duration from
// the yes. Absent, the ask is a vote and the answer is all it leaves.
//
//tony:schemagen=verse-ask,notag
type Ask struct {
	Name     string `tony:"field=name"`
	Question string `tony:"field=question"`
	// To is whom it is put to, in the grant grammar; absent, whoever may answer what it is about,
	// or anyone.
	To  []string `tony:"field=to,omitzero"`
	Due string   `tony:"field=due,omitzero"`
	// OnSilence is what the ask settles on when Due passes and nobody has answered: `no`, the
	// default, or `yes`, which runs its `then` as an answer would. It needs a Due. An onward ask
	// takes the served ask's.
	OnSilence string         `tony:"field=onSilence,omitzero"`
	Then      entity.Payload `tony:"field=then,omitzero"`
	// Else is what follows a no, as Then is what follows a yes: a no answered, or silence settling
	// on no at Due. It takes what Then does.
	Else  entity.Payload `tony:"field=else,omitzero"`
	About string         `tony:"field=about,omitzero"`
	// Keep is how long the ask stands once settled, as a duration (168h); absent, a week.
	Keep string `tony:"field=keep,omitzero"`
	// Labels are what the ask says about itself for whoever watches asks (`team: payments`).
	Labels map[string]string `tony:"field=labels,omitzero"`
	// Serves makes this an ONWARD ask (9ce3bva0, stage 4): one a resolver raises, made as the
	// resolver, about an ask it serves — the ask's id — for the party named in For, or the only
	// one it serves it for. Its options are the served ask's, its clock fits inside the served
	// ask's, and it takes no `then`: what follows it is the answer it carries back.
	Serves string `tony:"field=serves,omitzero"`
	For    string `tony:"field=for,omitzero"`
}

// Asked is what an ask answers with: the id it was written under, and the edit it stands at.
//
//tony:schemagen=verse-asked,notag
type Asked struct {
	ID   string `tony:"field=id"`
	Edit int    `tony:"field=edit,omitzero"`
}

// ServedAsk is one waiting ask a resolver serves (GET /resolvers/{name}/asks): what it asks, and
// the parties the resolver answers for on it — each principal it answers for, or the address of
// the place.
//
//tony:schemagen=verse-served-ask,notag
type ServedAsk struct {
	ID     string            `tony:"field=id"`
	Edit   int               `tony:"field=edit,omitzero"`
	Pose   string            `tony:"field=pose,omitzero"`
	About  string            `tony:"field=about,omitzero"`
	Labels map[string]string `tony:"field=labels,omitzero"`
	For    []string          `tony:"field=for"`
}

// AskServed is GET /asks/{id}/served: the resolver serving the ask for the caller, empty where
// none does.
//
//tony:schemagen=verse-ask-served,notag
type AskServed struct {
	Resolver string `tony:"field=resolver,omitzero"`
}

// Recorded is what `POST /asks/{id}/answer` and `POST /tasks/{id}/resolve` answer with 202 when an
// answer is recorded and the question stays open: it takes Need answers for one choice, and
// Count have chosen Choice so far. The answer that reaches Need settles the question, and that
// answers 204.
//
//tony:schemagen=verse-recorded,notag
type Recorded struct {
	Count  int    `tony:"field=count"`
	Need   int    `tony:"field=need"`
	Choice string `tony:"field=choice"`
}

// String is the tally as a person reads it.
func (r Recorded) String() string {
	return fmt.Sprintf("recorded: %d of %d for %s", r.Count, r.Need, r.Choice)
}

// AskChange is a change to a waiting ask by its asker (`verse ask <id> --due …`, PATCH
// /asks/{id}). A new Due moves its clock and asks nothing again. A new Question, To, Then
// or Else changes what it asks, so it is asked again, as the next edit, and the ballots cast before
// are dropped. Edit is the edit the asker changed, where they name it; a change to an ask that has
// moved on since is refused.
//
//tony:schemagen=verse-ask-change,notag
type AskChange struct {
	Edit     int            `tony:"field=edit,omitzero"`
	Due      string         `tony:"field=due,omitzero"`
	Question string         `tony:"field=question,omitzero"`
	To       []string       `tony:"field=to,omitzero"`
	Then     entity.Payload `tony:"field=then,omitzero"`
	Else     entity.Payload `tony:"field=else,omitzero"`
}
