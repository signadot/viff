package api

import (
	"fmt"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/task"
)

// AskRecord is an ask as verse stores it at `verse.ask.<id>`: what a row of `GET /asks` (AskRow,
// DocketRow) holds. Every question anybody is put is one of these, whoever raised it, so the record
// holds five kinds (Kind), told apart by which keys they carry: a person's ask, an onward ask
// raised to serve another, words said to a task, a task's question, and a firing's gate question.
// Every key is optional: a record carries the ones its kind and its history wrote.
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

	// What a person's ask, an onward ask or words to a task say: its name, who asked, the
	// question, and its edit (1, and one more for each change to its wording).
	Name     string `tony:"field=name,omitzero"`
	Asker    string `tony:"field=asker,omitzero"`
	Question string `tony:"field=question,omitzero"`
	Edit     int    `tony:"field=edit,omitzero"`
	// Answerers are whom it is put to, in the grant grammar.
	Answerers Names `tony:"field=answerers,omitzero"`
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

	// What the question is, as posed: the rule or task that raised it (Origin), the gate or
	// row it was asked at, its status (`pending`, `answered`, `orphaned`) and when it was posed.
	// Origin is kept as stored, an empty one included: a person's ask holds `origin: ""`, and so
	// does a task's question relayed under an id verse did not mint, while words to a task hold
	// none. A deployed charter (gate-ask.tony) tells a person's ask by its empty origin. ENG-1300
	// is about saying who raised a question in one key.
	Origin  *string `tony:"field=origin,omitzero"`
	Gate    string  `tony:"field=gate,omitzero"`
	Status  string  `tony:"field=status,omitzero"`
	PosedAt string  `tony:"field=posedAt,omitzero"`
	// Pose is the question as put to a person; Reason why it is asked.
	Pose   string `tony:"field=pose,omitzero"`
	Reason string `tony:"field=reason,omitzero"`
	// Subject is the address the question is about, and About the first line of what the subject
	// says of itself.
	Subject string `tony:"field=subject,omitzero"`
	About   string `tony:"field=about,omitzero"`
	// Spawns is what a firing's gate would start; At the charter address its rule is installed
	// at.
	Spawns string `tony:"field=spawns,omitzero"`
	At     string `tony:"field=at,omitzero"`
	// Tool, Action, Task, Step and Fields are the step a task's question is about: the task, the
	// tool it called, the system and verb, and the fields of the step.
	Tool   string            `tony:"field=tool,omitzero"`
	Action string            `tony:"field=action,omitzero"`
	Task   string            `tony:"field=task,omitzero"`
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

	// SaidTo is the task words were said to, and Turn the question they answered.
	SaidTo string `tony:"field=saidTo,omitzero"`
	Turn   string `tony:"field=turn,omitzero"`
	// Carried is how an onward ask's answer was carried back to the ask it serves.
	Carried *AskCarried `tony:"field=carried,omitzero"`

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

// Kind is which of the five kinds the record is, from the keys it carries, since the record
// stores none: words to a task carry `saidTo`; an onward ask `serves`; a person's ask an `asker`
// (a task's own `ask`-tool question is asked at the gate `ask` too, and has no asker); a task's
// question a `task`; and a firing's gate question none of these.
func (r AskRecord) Kind() string {
	switch {
	case r.SaidTo != "":
		return AskKindSay
	case r.Serves != nil:
		return AskKindOnward
	case r.Asker != "":
		return AskKindPerson
	case r.Task != "":
		return AskKindTask
	default:
		return AskKindGate
	}
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
// A record carried before the note existed holds that sentence in `status`; it is read as the
// note.
//
//tony:schemagen=verse-ask-carried,notag,codec=custom
type AskCarried struct {
	At      string `tony:"field=at"`
	Status  int    `tony:"field=status,omitzero"`
	Refused string `tony:"field=refused,omitzero"`
	Note    string `tony:"field=note,omitzero"`
}

// ToTonyIR writes the carry as its record.
func (c *AskCarried) ToTonyIR(_ ...gomap.MapOption) (*ir.Node, error) {
	if c == nil {
		return ir.Null(), nil
	}
	m := map[string]*ir.Node{"at": ir.FromString(c.At)}
	if c.Status != 0 {
		m["status"] = ir.FromInt(int64(c.Status))
	}
	if c.Refused != "" {
		m["refused"] = ir.FromString(c.Refused)
	}
	if c.Note != "" {
		m["note"] = ir.FromString(c.Note)
	}
	return ir.FromMap(m), nil
}

// FromTonyIR reads a carry, and the sentence an older one kept in `status` as its note.
func (c *AskCarried) FromTonyIR(node *ir.Node, _ ...gomap.UnmapOption) error {
	node = ir.Uncomment(node)
	if node == nil || node.Type == ir.NullType {
		return nil
	}
	if node.Type != ir.ObjectType {
		return fmt.Errorf("carried: expected a record, got %s", node.Type)
	}
	for k, v := range ir.ToMap(node) {
		v = ir.Uncomment(v)
		if v == nil {
			continue
		}
		switch {
		case k == "status" && v.Type == ir.NumberType && v.Int64 != nil:
			c.Status = int(*v.Int64)
		case k == "status" && v.Type == ir.StringType:
			c.Note = v.String
		case v.Type != ir.StringType:
			continue
		case k == "at":
			c.At = v.String
		case k == "refused":
			c.Refused = v.String
		case k == "note":
			c.Note = v.String
		}
	}
	return nil
}

// Names are principals or grantees, as a list. An ask written before its answerers were always a
// list holds one name as a string, which is read as a list of one.
//
//tony:schemagen=verse-names,notag,codec=custom
type Names []string

// ToTonyIR writes the names as a list.
func (n *Names) ToTonyIR(_ ...gomap.MapOption) (*ir.Node, error) {
	if n == nil || *n == nil {
		return ir.Null(), nil
	}
	out := make([]*ir.Node, len(*n))
	for i, s := range *n {
		out[i] = ir.FromString(s)
	}
	return ir.FromSlice(out), nil
}

// FromTonyIR reads a list of names, or one name.
func (n *Names) FromTonyIR(node *ir.Node, _ ...gomap.UnmapOption) error {
	node = ir.Uncomment(node)
	switch {
	case node == nil || node.Type == ir.NullType:
		*n = nil
	case node.Type == ir.StringType:
		*n = Names{node.String}
	case node.Type == ir.ArrayType:
		out := make(Names, 0, len(node.Values))
		for _, v := range node.Values {
			if v = ir.Uncomment(v); v != nil && v.Type == ir.StringType {
				out = append(out, v.String)
			}
		}
		*n = out
	default:
		return fmt.Errorf("names: expected a list of names, got %s", node.Type)
	}
	return nil
}
