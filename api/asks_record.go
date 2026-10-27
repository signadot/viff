package api

import (
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/task"
)

// AskRecord is an ask as verse stores it at `verse.ask.<id>`: what a row of `GET /asks` (AskRow,
// DocketRow) holds. Every question anybody is put is one of these, whoever raised it, so the record
// holds five kinds (Kind): a person's ask, an onward ask raised to serve another, words said to a
// task, a task's question, and a firing's gate question. Each says its parties the same way: who
// raised it (From), whom it is put to (To), and who settled it (By). Every other key is optional:
// a record carries the ones its kind and its history wrote.
//
// A listing answers the keys this type names, and only those: a key a writer adds and the type
// leaves out would drop from GET /asks. A check in versed's tests holds every ask they leave to the
// type, and versed logs a key it drops reading a record, once per record and set of keys.
//
// `about` is the first line of what the ask's subject says of itself, copied when the question is
// posed; the subject's address is `subject`. (api.Ask.About, the request, is the address.)
//
//tony:schemagen=verse-ask-record,notag
type AskRecord struct {
	// ID is the ask's id, the last segment of its address. A person's ask not yet posed has
	// none stored; the listing fills it from the address.
	ID string `tony:"field=id,omitzero"`

	// From is who raised it, and To whom it is put to, where it names anybody.
	From *AskFrom `tony:"field=from,omitzero"`
	To   *AskTo   `tony:"field=to,omitzero"`

	// What a person's ask, an onward ask or words to a task say: its name, the question, and its
	// edit (1, and one more for each change to its wording).
	Name     string `tony:"field=name,omitzero"`
	Question string `tony:"field=question,omitzero"`
	Edit     int    `tony:"field=edit,omitzero"`
	// Due is when a person's ask settles if nobody answers, and OnSilence what it settles on.
	Due       string `tony:"field=due,omitzero"`
	OnSilence string `tony:"field=onSilence,omitzero"`
	// Then and Else are what follows a yes and a no, on the asker's authority.
	Then *AskFollow `tony:"field=then,omitzero"`
	Else *AskFollow `tony:"field=else,omitzero"`
	// Keep is how long the record stands once settled, a Go duration.
	Keep string `tony:"field=keep,omitzero"`
	// Serves is the ask an onward ask was raised to serve.
	Serves *AskServes `tony:"field=serves,omitzero"`

	// What the question is, as posed: the gate or row it was asked at, its status (`pending`,
	// `answered`, `orphaned`) and when it was posed.
	Gate    string `tony:"field=gate,omitzero"`
	Status  string `tony:"field=status,omitzero"`
	PosedAt string `tony:"field=posedAt,omitzero"`
	// Pose is the question as put to a person; Reason why it is asked.
	Pose   string `tony:"field=pose,omitzero"`
	Reason string `tony:"field=reason,omitzero"`
	// Subject is the address the question is about, and About the first line of what the subject
	// says of itself.
	Subject string `tony:"field=subject,omitzero"`
	About   string `tony:"field=about,omitzero"`
	// Spawns is what a firing's gate would start.
	Spawns string `tony:"field=spawns,omitzero"`
	// Tool, Action, Step and Fields are the step a task's question is about: the tool it called,
	// the system and verb, and the fields of the step.
	Tool   string            `tony:"field=tool,omitzero"`
	Action string            `tony:"field=action,omitzero"`
	Step   string            `tony:"field=step,omitzero"`
	Fields map[string]string `tony:"field=fields,omitzero"`
	// Labels are what the ask says about itself for whoever watches asks.
	Labels map[string]string `tony:"field=labels,omitzero"`
	// Options are the answers it takes, each with the votes it needs; Deadline when it settles
	// and on what if nobody answers.
	Options  []task.Option  `tony:"field=options,omitzero"`
	Deadline *task.Deadline `tony:"field=deadline,omitzero"`
	// WaitingOn is who may answer it now, groups expanded; AskedAgain says it was posed again
	// over a settled record.
	WaitingOn  []string `tony:"field=waitingOn,omitzero"`
	AskedAgain bool     `tony:"field=askedAgain,omitzero"`
	// Suspended says a suspension withdrew it; Expires when a settled record goes.
	Suspended bool   `tony:"field=suspended,omitzero"`
	Expires   string `tony:"field=expires,omitzero"`

	// How it was answered: the ballots cast toward a threshold, then the verdict (an option, or
	// `heard`), when, by whom, through which resolver or io, why, and the words said with it.
	Ballots    map[string]AskBallot `tony:"field=ballots,omitzero"`
	Verdict    string               `tony:"field=verdict,omitzero"`
	AnsweredAt string               `tony:"field=answeredAt,omitzero"`
	By         string               `tony:"field=by,omitzero"`
	Via        string               `tony:"field=via,omitzero"`
	Why        string               `tony:"field=why,omitzero"`
	Say        []string             `tony:"field=say,omitzero"`
	// Answer is a person's ask's answer as its asker reads it: `yes` or `no`, or `heard` for
	// words said to a task.
	Answer string `tony:"field=answer,omitzero"`
	// Said are the words said ON it, answering nothing, by when they were said.
	Said map[string]AskSaid `tony:"field=said,omitzero"`
	// Stale are answers that arrived after it had settled, by who gave them.
	Stale map[string]AskStale `tony:"field=stale,omitzero"`
	// OrphanedAt is when its task ended with it unanswered.
	OrphanedAt string `tony:"field=orphanedAt,omitzero"`

	// Turn is the question words said to a task answered.
	Turn string `tony:"field=turn,omitzero"`
	// Carried is how an onward ask's answer was carried back to the ask it serves.
	Carried *AskCarried `tony:"field=carried,omitzero"`

	// Followed is the claim that settled the question, the resolution and which of Then and Else
	// it ran: a question settles once, by its first claim, so an answer that lost the race to
	// claim it runs neither and is not delivered.
	Followed *AskFollowed `tony:"field=followed,omitzero"`
	// What became of Then and Else: the task they started, a failure, and the answer or ask they
	// made.
	ThenTask     string             `tony:"field=then-task,omitzero"`
	ElseTask     string             `tony:"field=else-task,omitzero"`
	ThenFailed   *AskFollowFailed   `tony:"field=then-failed,omitzero"`
	ElseFailed   *AskFollowFailed   `tony:"field=else-failed,omitzero"`
	ThenAnswered *AskFollowAnswered `tony:"field=then-answered,omitzero"`
	ElseAnswered *AskFollowAnswered `tony:"field=else-answered,omitzero"`
	ThenAsked    *AskFollowAsked    `tony:"field=then-asked,omitzero"`
	ElseAsked    *AskFollowAsked    `tony:"field=else-asked,omitzero"`
}

// AskRow is one ask as `GET /asks` lists it: the record as stored, and, for a record that does
// not read as an AskRecord, why (Unreadable), beside only its id, so it can be seen.
//
//tony:schemagen=verse-ask-row,notag
type AskRow struct {
	AskRecord
	Unreadable string `tony:"field=unreadable,omitzero"`
}

// DocketRow is one row of the docket (`GET /asks?waiting=me`, `?all=true`, `?served=true`): a
// pending question as stored, nothing taken out, with whom a resolver serves it for (ServedBy,
// with `?served` or `?all`) and which plane raised it (Where: `task` for a task's question,
// `engine` for the rest). Words said to a task ask nobody and are never on it.
//
//tony:schemagen=verse-docket-row,notag
type DocketRow struct {
	AskRow
	ServedBy string `tony:"field=servedBy,omitzero"`
	Where    string `tony:"field=where"`
}

// The kinds of ask record, as Kind tells them apart.
const (
	// AskKindPerson is a person's ask (`POST /asks`).
	AskKindPerson = "ask"
	// AskKindOnward is a person's ask raised to serve another ask (`serves`).
	AskKindOnward = "onward"
	// AskKindSay is words said to a task (`POST /tasks/{id}/say`), which asks no person.
	AskKindSay = "say"
	// AskKindTask is a question a task raised.
	AskKindTask = "task"
	// AskKindGate is a question a firing's gate raised.
	AskKindGate = "gate"
)

// Kind is which of the five kinds the record is, from whom it is from and to, since the record
// stores none: words to a task are to a task; an onward ask `serves` another; a person's ask is
// from a principal (a task's own `ask`-tool question is asked at the gate `ask` too, and is from
// the task); a task's question is from a task; and a firing's gate question from a rule.
func (r AskRecord) Kind() string {
	switch {
	case r.To != nil && r.To.Task != "":
		return AskKindSay
	case r.Serves != nil:
		return AskKindOnward
	case r.From != nil && r.From.Principal != "":
		return AskKindPerson
	case r.From != nil && r.From.Task != "":
		return AskKindTask
	default:
		return AskKindGate
	}
}

// AskFrom is who raised a question: exactly one of a rule (with the charter it is installed at),
// a task, or a principal, verbatim (`user:…`, `agent:…`, `via:…`). The principal carries
// authority: an ask's `then` runs as it, and an onward ask carries back as it.
//
//tony:schemagen=verse-ask-from,notag
type AskFrom struct {
	Rule      string `tony:"field=rule,omitzero"`
	At        string `tony:"field=at,omitzero"`
	Task      string `tony:"field=task,omitzero"`
	Principal string `tony:"field=principal,omitzero"`
}

// AskTo is whom a question is put to: grantees in the grant grammar, or the task words were said
// to.
//
//tony:schemagen=verse-ask-to,notag
type AskTo struct {
	Grantees []string `tony:"field=grantees,omitzero"`
	Task     string   `tony:"field=task,omitzero"`
}

// AskFollow is what follows a person's ask's yes (`then`) or no (`else`): exactly one of a spawn,
// an answer to another ask, or another ask. A spawn and an ask are stored as their asker wrote
// them, and read strictly when they run.
//
//tony:schemagen=verse-ask-follow,notag
type AskFollow struct {
	Spawn  entity.Payload   `tony:"field=spawn,omitzero"`
	Answer *AskFollowAnswer `tony:"field=answer,omitzero"`
	Ask    entity.Payload   `tony:"field=ask,omitzero"`
}

// AskFollowAnswer is an answer to another ask that follows a yes or a no, as its asker: the ask,
// the choice, words with it, whether it forces a settled ask, and the edit it answers.
//
//tony:schemagen=verse-ask-follow-answer,notag
type AskFollowAnswer struct {
	Ask    string `tony:"field=ask"`
	Choice string `tony:"field=choice"`
	Say    string `tony:"field=say,omitzero"`
	Force  bool   `tony:"field=force,omitzero"`
	Edit   int    `tony:"field=edit,omitzero"`
}

// AskServes is the ask an onward ask serves: its id and edit, and the party it was asked for.
//
//tony:schemagen=verse-ask-serves,notag
type AskServes struct {
	ID   string `tony:"field=id"`
	Edit int    `tony:"field=edit"`
	For  string `tony:"field=for,omitzero"`
}

// AskBallot is one answerer's vote toward a threshold: the choice, when, words with it, the edit
// it answered, and through which resolver or io; Authenticated is the provider that verified the
// answerer, where one did.
//
//tony:schemagen=verse-ask-ballot,notag
type AskBallot struct {
	Choice        string   `tony:"field=choice"`
	At            string   `tony:"field=at"`
	Say           []string `tony:"field=say,omitzero"`
	Edit          int      `tony:"field=edit,omitzero"`
	Via           string   `tony:"field=via,omitzero"`
	Authenticated string   `tony:"field=authenticated,omitzero"`
}

// AskSaid is words said on an ask, answering nothing: who said them, and what.
//
//tony:schemagen=verse-ask-said,notag
type AskSaid struct {
	By   string `tony:"field=by"`
	Text string `tony:"field=text"`
}

// AskStale is an answer that arrived after the ask settled: what it chose, when, and its words.
//
//tony:schemagen=verse-ask-stale,notag
type AskStale struct {
	Verdict string   `tony:"field=verdict"`
	At      string   `tony:"field=at"`
	Say     []string `tony:"field=say,omitzero"`
}

// AskFollowFailed is a then or else that could not run: when, and why.
//
//tony:schemagen=verse-ask-follow-failed,notag
type AskFollowFailed struct {
	At  string `tony:"field=at"`
	Why string `tony:"field=why"`
}

// AskFollowed is the claim that settled the question: the resolution, in the keys the answered
// record uses, when it was claimed, and which field (`then` or `else`) it ran. The claim is the
// answer: a question pending under one has its answer decided and not yet delivered, and
// Undelivered says why the last delivery failed, until the question is posed again and it is.
//
//tony:schemagen=verse-ask-followed,notag
type AskFollowed struct {
	At            string   `tony:"field=at"`
	Field         string   `tony:"field=field,omitzero"`
	Verdict       string   `tony:"field=verdict,omitzero"`
	By            string   `tony:"field=by,omitzero"`
	Via           string   `tony:"field=via,omitzero"`
	Why           string   `tony:"field=why,omitzero"`
	Say           []string `tony:"field=say,omitzero"`
	Edit          int      `tony:"field=edit,omitzero"`
	Authenticated string   `tony:"field=authenticated,omitzero"`
	Undelivered   string   `tony:"field=undelivered,omitzero"`
}

// AskFollowAnswered is an answer a then or else made: when it started, the ask it answered, the
// status that answer got, and the refusal where it was refused.
//
//tony:schemagen=verse-ask-follow-answered,notag
type AskFollowAnswered struct {
	At      string `tony:"field=at"`
	Ask     string `tony:"field=ask,omitzero"`
	Status  int    `tony:"field=status,omitzero"`
	Refused string `tony:"field=refused,omitzero"`
}

// AskFollowAsked is an ask a then or else raised: when it started, the status raising it got, the
// ask's id, and the refusal where it was refused.
//
//tony:schemagen=verse-ask-follow-asked,notag
type AskFollowAsked struct {
	At      string `tony:"field=at"`
	Status  int    `tony:"field=status,omitzero"`
	ID      string `tony:"field=id,omitzero"`
	Refused string `tony:"field=refused,omitzero"`
}

// AskCarried is how an onward ask's answer was carried back to the ask it serves: when, the
// status the served ask answered, its refusal where it refused, and a Note where nothing was
// carried (the clock settled on no option).
//
//tony:schemagen=verse-ask-carried,notag
type AskCarried struct {
	At      string `tony:"field=at"`
	Status  int    `tony:"field=status,omitzero"`
	Refused string `tony:"field=refused,omitzero"`
	Note    string `tony:"field=note,omitzero"`
}
