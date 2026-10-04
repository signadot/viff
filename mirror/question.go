package mirror

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/task"
)

// A gate question is published as an ask of its own, `verse.ask.<id>`, rather than as a field
// on whatever raised it: every question is an ask, on the one docket asks stand on (ask.go).
//
// Two things force that. A trigger gate is posed by a FIRING, before anything spawns — there is
// no task:instance to hang it on, and if the gate is denied there never will be. And a
// call_agent child raises its own questions from its own Task, which is never mirrored at
// all. Both are the same shape: the record needs somewhere to live, and "whose it is" does not
// answer that.
//
// The id is the question's own (`<origin>~d<n>`), so the ref carries the origin and a predicate
// can select one run's questions without a second field to match on.
// QuestionRef addresses one question: the ask it is.
func QuestionRef(id string) entity.Ref { return AskRef(id) }

// RuleRef addresses the record of one rule of the charter installed at an address:
// `verse.trigger.rule.<at>:<name>`, and the bare name for baseline. It is spelled here and
// used by the trigger package, rather than the other way round, because a question is co-owned
// by the rule that posed it and the mirror is what writes that down — and mirror cannot import
// trigger.
func RuleRef(at, name string) entity.Ref {
	return entity.VerseRef("trigger", "rule", RecordID(at, name))
}

// RecordID is the id a charter's durable records carry: the name alone for baseline, and
// `<at>:<name>` for a charter installed at an address, so two packages may share a name.
func RecordID(at, name string) string {
	if at == "" {
		return name
	}
	return at + ":" + name
}

// poseRecord writes the question's own record, once: the system's, under what posed it — the task,
// or the rule of the charter that asked — and holding out what the default on
// `verse.ask` says, or the gate's own answerers when it named them, or the resolver's
// answerers when a resolver owns it. The resolver is on the record FROM THE WRITE THAT MAKES IT:
// a record that held the default out first and was narrowed to the resolver afterwards is a
// question every person may settle for as long as the second write takes, and for good when it
// fails.
//
// AN OWNER RECORD, though the question is the system's like the node above it and the elision
// would have written none: what reaches a question is decided on its record, so that a grant
// held out above — the docket's open default — stops here, and a gate held out to davido is
// held out to davido. The link under is what lets the task's owner, or the charter's, answer
// what their own work asked, with the question standing at its own address.
func poseRecord(store entity.Store, q task.GateQuestion, taskID string) error {
	var origin entity.Ref
	switch {
	case taskID != "":
		origin = Ref(taskID)
	case q.Origin != "":
		origin = RuleRef(q.At, q.Origin)
	}
	ref := QuestionRef(q.ID)
	answers := entity.Canonical(q.Answerers)
	// NOT THROWN AWAY. A question whose record did not land stands with none, and one with none
	// is answered by whoever the docket's row admits — every person — so a store refusing this
	// one write would turn a gate held out to five named people into one anybody settles, for
	// good and with nothing to show for it. Tried a few times, since a store under load refuses
	// a write now and then, and then said, to a caller that does not pose without it.
	// A PERSON'S ASK IS ITS ASKER'S RECORD, written by the verb with who reads it and who answers
	// it (versed postAsk). Posing it writes the question's fields onto that record and leaves the
	// record as the verb wrote it: re-established as the docket's, the ask would stop being its
	// asker's, and its asker could no longer withdraw it.
	if owner, err := ownerOnRecord(store, ref); err != nil {
		return err
	} else if owner != "" && !entity.IsSystem(owner) {
		return nil
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = poseRecordOnce(store, ref, origin, q.Subject, answers); err == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	return err
}

// poseRecordOnce is one try at poseRecord, every read in it included: the default it copies is
// read from the mirror, and a mirror that did not answer is the same store refusing.
//
// WHO MAY ANSWER BELONGS TO THE ASKING, not to the id. A question that stood orphaned — its
// waiter gone with nobody having decided it, at a restart or under a suspension — is asked again
// under its own id when the firing comes back to it, so where the question standing is not
// pending, this is a new asking and the record is rewritten to say who answers it
// (entity.Reestablish). Where it stands pending it is the same asking reported a second time — a
// local run posts from the registry and again from the frame — and the record is left as it is:
// a resolver it has been handed to since keeps it.
//
// AN ASK ABOUT SOMETHING IS READ AND ANSWERED AS THAT THING IS (9ce3bva0, D3): whoever may read
// its subject reads it, and where it names nobody, whoever may write its subject answers it —
// `audience:<subject>` in both — since a question says what it is about, and a docket reader who
// may not read the subject would learn it there. Where it names answerers they answer, each able
// to read the subject (CheckAudience, before it is posed). An ask about nothing is the docket's.
func poseRecordOnce(store entity.Store, ref, origin entity.Ref, subject string, answers []string) error {
	var grants []entity.Grant
	answering := AnsweringReaders(store, subject, answers)
	if audience := subjectAudience(subject); audience != nil {
		to := map[entity.Level][]string{entity.Read: append(audience, answering...), entity.ReadWrite: audience}
		if len(answers) > 0 {
			to[entity.ReadWrite] = answers
		}
		grants = []entity.Grant{{At: ref.Segs(), To: to}}
	} else if len(answers) > 0 {
		levels, err := entity.DefaultFor(store, ref)
		if err != nil {
			return err
		}
		to := map[entity.Level][]string{entity.ReadWrite: answers}
		if reads := append(slices.Clone(levels[entity.Read]), answering...); len(reads) > 0 {
			to[entity.Read] = reads
		}
		grants = []entity.Grant{{At: ref.Segs(), To: to}}
	}
	standing, ok, err := store.Get(ref)
	if err != nil {
		return err
	}
	if ok && entity.Str(standing.Payload, "status") == QuestionPending {
		return entity.Establish(store, ref, entity.SystemDocket, origin, grants)
	}
	return entity.Reestablish(store, ref, entity.SystemDocket, origin, grants)
}

// Parties is whom an ask is put to, as the nodes their `answer` grants stand on (entity.Answer):
// each principal it names at `verse.for.<principal>`, a group standing for its members as they
// are when it is asked; or, where it names nobody, what it is about. A whole kind, a place acting
// and an address's audience are nobody a resolver answers for, and name no party.
func Parties(store entity.Store, subject string, answerers []string) []entity.Ref {
	var out []entity.Ref
	for _, n := range expandGroups(store, answerers) {
		if g, err := entity.ParsePrincipal(n); err == nil && g.Kind != entity.ViaKind {
			out = append(out, entity.ForRef(g.String()))
		}
	}
	if len(answerers) == 0 && subject != "" {
		if ref, err := entity.ParseAddr(subject); err == nil {
			out = append(out, ref)
		}
	}
	return out
}

// AnsweringReaders is the `read` an ask holds out to the resolvers serving it: `answering:` each
// of its parties (9ce3bva0, S2).
func AnsweringReaders(store entity.Store, subject string, answerers []string) []string {
	var out []string
	for _, p := range Parties(store, subject, answerers) {
		out = append(out, entity.Answering(p))
	}
	return out
}

// subjectAudience is what a question is about, as a grantee: none for a question about nothing,
// or about something that is no one place.
func subjectAudience(subject string) []string {
	if subject == "" {
		return nil
	}
	ref, err := entity.ParseAddr(subject)
	if err != nil {
		return nil
	}
	return []string{entity.Audience(ref)}
}

// ErrCannotRead is an answerer who may not read what an ask is about.
var ErrCannotRead = errors.New("may not read what it asks about")

// CheckAudience says whether every answerer an ask names may read what it is about: NOBODY
// ANSWERS AN ASK ABOUT SOMETHING THEY CANNOT READ (9ce3bva0, D3). An ask about nothing, or naming
// nobody, passes — the latter is answered by its subject's own audience, who read it by holding
// write. A group is its members. A whole kind (`user:*`) passes only where what the subject holds
// out to anybody of that kind reads it, which is asked of a principal of the kind nobody is: a
// kind cannot be enumerated, and `.invalid` is reserved never to name anyone (RFC 2606).
func CheckAudience(store entity.Store, subject string, answerers []string) error {
	if subject == "" || len(answerers) == 0 {
		return nil
	}
	ref, err := entity.ParseAddr(subject)
	if err != nil {
		return nil
	}
	var cannot []string
	for _, n := range expandGroups(store, answerers) {
		g, err := entity.ParseGrantee(n)
		if err != nil {
			return fmt.Errorf("answerers: %w", err)
		}
		who := n
		switch {
		case g.Kind == entity.Everyone:
			who = entity.UserKind + ":" + nobody
		case g.ID == entity.Everyone:
			who = g.Kind + ":" + nobody
		case g.Kind == entity.AudienceKind:
			continue // an audience at an address, held to that address's check when it answers
		}
		ok, err := entity.Holds(store, who, entity.Read, ref)
		if err != nil {
			return err
		}
		if !ok {
			cannot = append(cannot, n)
		}
	}
	if len(cannot) > 0 {
		return fmt.Errorf("%s %w %s", strings.Join(cannot, ", "), ErrCannotRead, subject)
	}
	return nil
}

// nobody is the id of a principal no one is, for asking what a whole kind holds.
const nobody = "nobody@verse.invalid"

// expandGroups is names with each group replaced by its members.
func expandGroups(store entity.Store, names []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		if g, err := entity.ParseGrantee(n); err == nil && g.Kind == entity.GroupKind {
			out = append(out, entity.Members(store, g.ID, seen)...)
			continue
		}
		out = append(out, n)
	}
	return out
}

// SaidField holds what people said ABOUT a question without answering it, each entry under its own
// key — the instant it was said — so that two people talking at once cannot lose each other, the
// same reason a stale answer is written under its own key rather than appended to a list.
//
// It is not an answer and can never become one: a say carries no choice, and the record's `verdict`
// is written by one path only. What it is for is the sentence an answerer needs and the question
// does not have — why this is waiting, what somebody is checking, what they would want instead.
const SaidField = "said"

// SayOn records what somebody said about a question. The question need not be pending: a decision
// is talked about afterwards as much as before, and a record that refused the words once it
// settled would lose exactly the ones explaining it.
func SayOn(store entity.Store, id, by, said string) {
	said = strings.TrimSpace(said)
	if said == "" {
		return
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	writeAt(store, QuestionRef(id), entity.Obj{
		SaidField: entity.Obj{at: entity.Obj{"by": by, "text": said}},
	})
}

// AnswerersField is who the gate itself named to answer, on the question: what releasing gives
// the question back to. Absent where the gate named nobody.
const AnswerersField = "answerers"

// WaitingOnField is who the question waits on NOW, on the question: every principal who may
// answer it as it stands.
//
// A READER CANNOT WORK THIS OUT. `answerers` is there only where the gate named somebody, and the
// rest of who may answer — the resolver a question was handed to, the rung that took it after an
// escalation, the docket's own default — is on the question's perms record, which only the system
// may read. So a docket, a charter deriving one message per person, and `verse question` would each
// have to guess, and would each guess differently.
//
// It is the question's own `read-write` line, which is the same fact the answer route checks, with
// groups expanded to the people who act as themselves. `user:*` and the other wildcards stay as
// they are written: a question the docket holds out to every person waits on every person, and
// there is no list of them to write down.
//
// Present tense, so it goes when the question settles: what was asked of whom is `answerers`, and
// who decided is `by`.
//
// It moves when the question does — posed, bound, handed up, handed back — because those are
// writes to the question. Changing the DEFAULT above a docket of pending questions is not: it is a
// perms change, nothing writes the questions standing under it, and they go on saying the answer
// set they were posed with until each is asked again.
const WaitingOnField = "waitingOn"

// waitingOn is who may answer the question at ref as its record now holds it out, as the field
// carries it. A read that fails answers nil, which leaves whatever the record says standing: a
// question said to wait on nobody is one every reader would drop.
func waitingOn(store entity.Store, ref entity.Ref) any {
	names, err := entity.LevelAt(store, ref, entity.ReadWrite)
	if err != nil {
		return nil
	}
	if len(names) == 0 {
		// The record says nothing about who answers, so the default above it does.
		levels, err := entity.DefaultFor(store, ref)
		if err != nil {
			return nil
		}
		names = levels[entity.ReadWrite]
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		if g, err := entity.ParseGrantee(n); err == nil && g.Kind == entity.GroupKind {
			out = append(out, entity.Members(store, g.ID, seen)...)
			continue
		}
		out = append(out, n)
	}
	return answerersValue(out)
}

// about is the one line the question's subject offers about ITSELF, copied onto the question so
// that a reader who cannot read the subject can still say what is being decided.
//
// A CONVENTION rather than a schema (`06476112`): an entity that wants to be decidable carries a
// `says`, and one that carries none is read bare rather than refused. Copied HERE, where the
// question is posed, because a presenter — a docket, Slack, a resolver — may hold no grant on the
// subject while the system posing the question does, and because one read at posing serves every
// reader afterwards.
//
// A line, not a paragraph: a docket line and a chat message each show one, and a `says` that runs
// long is cut rather than allowed to bury what it is attached to.
func about(store entity.Store, subject string) any {
	if subject == "" {
		return entity.Removed()
	}
	ref, err := entity.ParseAddr(subject)
	if err != nil {
		return entity.Removed()
	}
	e, ok, err := store.Get(ref)
	if err != nil || !ok {
		return entity.Removed()
	}
	said, _, _ := strings.Cut(strings.TrimSpace(entity.Str(e.Payload, "says")), "\n")
	if said == "" {
		return entity.Removed()
	}
	if r := []rune(said); len(r) > aboutMax {
		said = strings.TrimSpace(string(r[:aboutMax])) + "…"
	}
	return said
}

// aboutMax is how much of a subject's `says` a question carries: enough for a sentence.
const aboutMax = 140

// answerersValue is the gate's named answerers as the question carries them, REMOVED where it
// named nobody so that an asking that names none does not inherit the last one's.
// LabelsField is what an ask says about itself for whoever watches asks (task.GateQuestion.Labels):
// a label's name to its value.
const LabelsField = "labels"

// labelsValue is labels as a record writes them, removed where there are none.
func labelsValue(labels map[string]string) any {
	if len(labels) == 0 {
		return entity.Removed()
	}
	o := entity.Obj{}
	for k, v := range labels {
		o[k] = v
	}
	return o
}

// LabelsOf reads an ask's labels, nil where it has none. Only verse writes them, as strings.
func LabelsOf(p entity.Payload) map[string]string {
	fs := entity.Fields(entity.Get(p, LabelsField))
	if len(fs) == 0 {
		return nil
	}
	out := make(map[string]string, len(fs))
	for k, v := range fs {
		if v = ir.Uncomment(v); v != nil && v.Type == ir.StringType {
			out[k] = v.String
		}
	}
	return out
}

func answerersValue(names []string) any {
	names = entity.Canonical(names)
	if len(names) == 0 {
		return entity.Removed()
	}
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = n
	}
	return out
}

// Question status, monotonic: a question is posed once and settles once.
const (
	QuestionPending  = "pending"
	QuestionAnswered = "answered"
	// QuestionOrphaned is a question that SETTLED WITHOUT A DECISION: nothing is waiting on
	// the answer any more, so nothing will read one. It settles rather than being deleted, for
	// the reason an answered one does — what was asked, and why it was never decided, is the
	// trail.
	//
	// The name says "the asker is gone" and that was once the only way to get here: a task
	// that died with a previous verse. It is no longer, and the difference matters to whoever
	// was going to answer. A task's loop now WITHDRAWS a standing question when an operator
	// intervenes — a say that redirects the run, a pause about to suspend it — and there the
	// asker is alive and somebody chose this. Same settling; not the same news.
	//
	// So `why` carries which it was, and a reader who needs to tell them apart reads that
	// rather than the status. A separate `withdrawn` status would be the alternative and is
	// deliberately not taken: it is a value a charter could match on, so it is a wire change,
	// and nothing yet needs to FILTER on the distinction — only to read it.
	QuestionOrphaned = "orphaned"
)

// PostQuestion publishes a posed question. `task` is the run that raised it, empty for a
// firing's gate — the origin is on the question either way, but a task id is what the docket
// and the flow tree index by.
//
// The derived effects are published as three flat lists rather than as the Effect structs.
// A charter matches on values, so `effects: {verbs: [write]}` has to be sayable without
// knowing the shape of a Go struct, and the three lists are what a gate predicate already
// speaks in (`GateMatch.Verbs`, `.Resources`, `.Paths`).
func PostQuestion(store entity.Store, p task.Pending, taskID string) (posed bool) {
	q := p.Question
	rec := entity.Obj{
		"id":      q.ID,
		"origin":  q.Origin,
		"gate":    q.Gate,
		"mode":    q.Mode.String(),
		"status":  QuestionPending,
		"posedAt": time.Now().UTC().Format(time.RFC3339),
	}
	// Which edit of a person's ask this is, where it is one: what an answer names (Tally).
	if q.Edit != 0 {
		rec["edit"] = q.Edit
	}
	// Absent rather than empty, for the reason the gate listing already compacts: a missing
	// key is visible, an empty one is camouflage. A bare `verse task run` is about nothing,
	// and a brokered gate legitimately carries neither origin nor mode.
	for k, v := range map[string]string{
		// What is being decided, in words, when the gate wrote any (trigger.Gate.Pose) — the
		// one field on the record written for a person to read rather than for a reader to
		// match on. `reason` says which rule asked, and stands for a question that has none.
		"pose":    q.Pose,
		"subject": q.Subject,
		"spawns":  q.Spawns,
		"reason":  q.Reason,
		// The charter the asking rule is in. Absent for baseline and for a task's own gate,
		// where there is no charter to name rather than an unknown one.
		"at": q.At,
		// The action a selection is holding up; absent for a gate, whose firing comes back on
		// its own. What a restart navigates by (trigger.Engine.ResumeSelections).
		"chain": q.Chain,
		"tool":  p.Tool,
		"task":  taskID,
	} {
		if v != "" {
			rec[k] = v
		}
	}
	if eff := effectLists(p.Effects); len(eff) > 0 {
		rec["effects"] = eff
	}
	rec[AnswerersField] = answerersValue(q.Answerers)
	rec[LabelsField] = labelsValue(q.Labels)
	rec["about"] = about(store, q.Subject)
	// Always written, default included, so the record says what can be chosen without a
	// reader having to know what a question with no options means. The tally reads it back.
	rec["options"] = optionList(q.OptionsOrDefault())
	if q.Deadline != nil && (q.Deadline.Within != "" || q.Deadline.At != "") {
		// `within` and `default` are the question's; `at`, the instant, is stamped by
		// whoever arms the clock (StampDeadline), once, so a re-pose finds it rather than
		// sliding it. It is not written here, and a merge leaves a standing one alone — a
		// dated question included, whose date is stamped by the arming like any other and
		// moved only by MoveDeadline.
		d := entity.Obj{}
		if q.Deadline.Within != "" {
			d["within"] = q.Deadline.Within
		}
		if q.Deadline.Default != "" {
			d["default"] = q.Deadline.Default
		}
		rec["deadline"] = d
	}
	// THE RECORD BEFORE THE QUESTION. A question with no record of its own is answered by
	// whoever the docket's row admits — every person — so written the other way round there is
	// a moment, for every question that names its answerers, in which anybody who sees it on the
	// docket answers it: a stranger's vote counted toward a gate that needs three of five named
	// people. An owner record normally FOLLOWS the write it records, because a record
	// standing for a node whose write then failed makes the next creator there not its owner;
	// that cannot happen here, where the id is this question's alone and the record is the
	// system's, and a record for a question that never landed holds out nothing to nobody.
	if err := poseRecord(store, q, taskID); err != nil {
		// Closed, and loud: nobody is asked until it is posed again, which is a gate that waits
		// rather than one that anybody settles.
		fmt.Fprintf(os.Stderr, "verse: question %s is NOT posed — who may answer it could not be "+
			"written, so nobody is asked until it is posed again: %v\n", q.ID, err)
		return false
	}
	// AFTER the record of who may answer, and read back from it: an asking that named nobody is
	// held out by the default above, and one posed again while it stands pending keeps whatever
	// resolver it has been handed to since.
	rec[WaitingOnField] = waitingOn(store, QuestionRef(q.ID))
	clearPriorAnswer(store, QuestionRef(q.ID), rec)
	writeAt(store, QuestionRef(q.ID), rec)
	return true
}

// DeadlineOf reads a record's deadline: the duration it was posed with, the option it settles
// on, and the instant it was armed against, empty until StampDeadline has run.
func DeadlineOf(p entity.Payload) (within, dflt, at string) {
	d := entity.Get(p, "deadline")
	return entity.Str(d, "within"), entity.Str(d, "default"), entity.Str(d, "at")
}

// StampDeadline fixes the instant a question's deadline fires, once. A stamp already standing
// is honoured and returned, so a re-pose after a restart arms against the instant the question
// was first given rather than a fresh one — elapsed wall time counts. The effective stamp is
// read back after the write, which is what makes two armings of one question agree.
//
// ONLY ON A QUESTION THAT STANDS. A write beneath an address brings the address into being, so a
// stamp on a question that was never posed would pose it: a node with a deadline and nothing
// else, no record saying who may answer it, on the docket for anybody.
func StampDeadline(store entity.Store, id string, at time.Time) (string, error) {
	ref := QuestionRef(id)
	want := at.UTC().Format(time.RFC3339Nano)
	_, _, err := store.Write(entity.Write{
		At:    ref.At("deadline"),
		Value: entity.Obj{"at": want}.Payload(),
		When: []entity.Require{
			{At: ref.At(), Pattern: entity.Present()},
			{At: ref.At("deadline", "at"), Absent: true},
		},
	})
	if err != nil {
		return "", err
	}
	e, ok, err := store.Get(ref)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("question %s is not posed: there is nothing to put a deadline on", id)
	}
	if _, _, got := DeadlineOf(e.Payload); got != "" {
		return got, nil
	}
	return want, nil
}

// MoveDeadline moves a PENDING question's deadline to at, and reports whether it moved. A question
// that has settled keeps the clock it settled under — what it records is what happened — so the
// move is conditional on the record still saying pending, and the store decides at the commit.
// The caller arms a timer for the new instant; one armed for the old instant finds the record
// moved and does nothing (a settle re-reads the instant before it acts).
func MoveDeadline(store entity.Store, id string, at time.Time) (bool, error) {
	ref := QuestionRef(id)
	moved, _, err := store.Write(entity.Write{
		At:    ref.At("deadline", "at"),
		Value: entity.Val(at.UTC().Format(time.RFC3339Nano)),
		When:  []entity.Require{{At: ref.At(), Pattern: entity.Obj{"status": QuestionPending}.Payload()}},
	})
	return moved, err
}

// optionList renders options as the record carries them: `[{id, need}]`, in order.
func optionList(opts []task.Option) []any {
	out := make([]any, 0, len(opts))
	for _, o := range opts {
		// THE NEED THAT COUNTS: below one reads as one (task.Need), so a producer that left it
		// empty is recorded as the one ballot that settles it, not as a need of none.
		need, _ := task.Need([]task.Option{o}, o.ID)
		om := entity.Obj{"id": o.ID, "need": need}
		if o.Says != "" {
			om["says"] = o.Says
		}
		// Written only when the option says something, so a ballot between interchangeable
		// doers stays as terse as a yes-or-no one.
		if o.Grant != "" {
			om["grant"] = o.Grant
		}
		if len(o.Tools) > 0 {
			om["tools"] = anyStrings(o.Tools)
		}
		out = append(out, om)
	}
	return out
}

// OptionsOf reads a record's options, the default two for a record that names none — which
// is every record posted before options existed, and a brokered record whose only writer was
// a frame.
func OptionsOf(p entity.Payload) []task.Option {
	f := ir.Uncomment(entity.Get(p, "options"))
	if f == nil || f.Type != ir.ArrayType {
		return task.DefaultOptions()
	}
	var out []task.Option
	for _, el := range f.Values {
		id := entity.Str(el, "id")
		if id == "" {
			continue
		}
		out = append(out, task.Option{ID: id, Need: intField(el, "need"), Says: entity.Str(el, "says")})
	}
	if len(out) == 0 {
		return task.DefaultOptions()
	}
	return out
}

// Tally counts the ballots on a record by the option they chose.
//
// ONLY THE BALLOTS CAST FOR THE ASK AS IT STANDS COUNT. A person's ask asked again drops the
// ballots of its earlier edit, and one that races the drop lands naming the edit it answered,
// which is not this one; a question nobody edits has no edit, and every ballot on it counts.
func Tally(p entity.Payload) map[string]int {
	out := map[string]int{}
	edit := intField(p, "edit")
	for _, b := range entity.Fields(entity.Get(p, "ballots")) {
		if intField(b, "edit") != edit {
			continue
		}
		if c := entity.Str(b, "choice"); c != "" {
			out[c]++
		}
	}
	return out
}

// Reword writes what a waiting ask now asks, as the next edit: the fields its asker changed, with
// the ballots cast before dropped. Conditional on the ask still waiting at the edit it was changed
// from, so a change never lands on an ask that settled or moved on in between. Whom it waits on is
// read again once its answerers are settled (WaitOnAgain).
func Reword(store entity.Store, id string, from int, rec entity.Obj) (bool, error) {
	ref := QuestionRef(id)
	rec["ballots"] = entity.Removed()
	committed, _, err := store.Write(entity.Write{
		At:    ref.At(),
		Value: rec.Payload(),
		When:  []entity.Require{{Pattern: entity.Obj{"status": QuestionPending, "edit": from}.Payload()}},
	})
	return committed, err
}

// WaitOnAgain reads whom a question waits on again from its record, after who may answer it
// changed.
func WaitOnAgain(store entity.Store, id string) {
	ref := QuestionRef(id)
	writeAt(store, ref, entity.Obj{WaitingOnField: waitingOn(store, ref)})
}

// EditOf is which edit of a person's ask a record stands at, and 0 for one nobody edits.
func EditOf(p entity.Payload) int { return intField(p, "edit") }

// intField reads an integer field, 0 when absent or not a number. The record is written by
// this package with an int, and read back through the store as a number node.
func intField(p entity.Payload, field string) int {
	f := ir.Uncomment(entity.Get(p, field))
	if f == nil || f.Type != ir.NumberType {
		return 0
	}
	if f.Int64 != nil {
		return int(*f.Int64)
	}
	if f.Float64 != nil {
		return int(*f.Float64)
	}
	n, _ := strconv.Atoi(f.Number)
	return n
}

// CastBallot records one ballot that does NOT settle the question, and reports the tally for
// its choice as read back after the write. The settling ballot is not written here: it rides
// the settle itself (AnswerQuestion), so that the ballot which reaches a threshold and the
// record saying so are one write.
//
// Read back rather than counted forward, because two ballots can land at once, each read the
// record before the other wrote, and each count one short. The write merges both; the read
// after it sees both; and whichever caller reads the threshold reached goes on to settle. A
// caller that read it too may find the settle already taken, which is the ordinary race two
// answerers have always had, and it is told so.
func CastBallot(store entity.Store, id string, res task.Resolution) (count int, err error) {
	ref := QuestionRef(id)
	_, _, err = store.Write(entity.Write{
		At:    ref.At("ballots"),
		Value: entity.Obj{res.By: ballot(res)}.Payload(),
	})
	if err != nil {
		return 0, err
	}
	e, ok, err := store.Get(ref)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	return Tally(e.Payload)[res.Chosen()], nil
}

// ballot is one answerer's entry under `ballots`: what they chose, when, and what they said.
// Keyed by who, so an answerer who changes their mind replaces their own ballot rather than
// casting two, and two answerers cannot lose each other's.
func ballot(res task.Resolution) entity.Obj {
	rec := entity.Obj{
		"choice": res.Chosen(),
		"at":     time.Now().UTC().Format(time.RFC3339),
	}
	if len(res.Steer) > 0 {
		rec["say"] = anyStrings(res.Steer)
	}
	if res.Edit != 0 {
		rec["edit"] = res.Edit
	}
	if res.Via != "" {
		rec["via"] = res.Via
	}
	return rec
}

func anyStrings(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// PostFrameQuestion publishes a question reported as a FRAME — the brokered path, where the
// worker ran the gate on its own machine and sent back what it chose to report.
//
// It writes less than PostQuestion because the frame carries less: derived effects stayed with
// the deriver that produced them, so `effects` is absent here rather than empty. That absence
// is the honest one — nobody classified this action WHERE THE RECORD IS BEING WRITTEN, which
// is different from classifying it to nothing.
//
// A local run posts twice, once from each writer. That is not a conflict: a write merges, so
// the frame's post cannot erase the effects the registry's post already recorded, and every
// field the two share they agree on.
func PostFrameQuestion(store entity.Store, ev task.Frame, taskID, subject string, answerers []string) bool {
	rec := entity.Obj{
		"id":      ev.ID,
		"gate":    ev.Gate,
		"status":  QuestionPending,
		"posedAt": time.Now().UTC().Format(time.RFC3339),
	}
	// A QUESTION THAT HAS ALREADY SETTLED IS NOT RE-POSED BY A LATE FRAME.
	//
	// A local run posts twice — once from the registry (PostQuestion above) and once from the
	// frame — and the comment above says every field the two share they agree on. They do not
	// agree on `status` once an ANSWER lands between them: this write merges `pending` over
	// `answered` and leaves a record that says both, with a verdict, a `by` and an `answeredAt`
	// sitting under `status: pending`. A person reading the docket then sees a question to
	// decide, already stamped with who decided it.
	//
	// PostQuestion guards this with clearPriorAnswer, which is the RE-ASK case: a genuinely new
	// asking clears the old answer and marks `askedAgain`. This is not that — it is the same
	// asking, reported a second time — so the answer stands and only the fields that would
	// contradict it are dropped.
	//
	// It surfaced as a flaky test (git-issue nkptp9b3h12ksx3kg9n0): under load the answer lands
	// inside the window between the two posts, which on an unloaded machine it never does.
	//
	// SO THE TWO FIELDS THAT CAN CONTRADICT ARE WRITTEN CONDITIONALLY, and the rest is not.
	// `status` and `posedAt` land only while the record does not already say it settled;
	// everything else the frame carries is information no answer contradicts and is written
	// either way. Reading the status here and branching on it is the same race with a smaller
	// window — measured, and it narrowed the flake without closing it — so the store is asked
	// instead.
	//
	// WHAT WAS MEASURED HERE WAS THE GUARD BROKEN. Until 874azhtjh12krn1xqsn0 the store kept only
	// the last of two preconditions about one place, so this write went out as "not orphaned"
	// alone and was admitted over an answer every time, serially, with no lag involved. The
	// residual that was put down to a view lagging the answer was that. Whether the two writers
	// still leave a window now that the guard holds has not been measured; ordering them, rather
	// than each checking, stays on git-issue nkptp9b3h12ksx3kg9n0.
	ref := QuestionRef(ev.ID)
	posed := entity.Obj{"status": rec["status"], "posedAt": rec["posedAt"]}
	delete(rec, "status")
	delete(rec, "posedAt")
	// The origin is READ OFF the id, and only when the id is one verse minted (`<origin>~d<n>`).
	// A worker that raises a gate under an id of its own choosing has no origin — it decided
	// nothing about who answers — and inventing one by taking the id whole would put a name
	// there that means "the question itself".
	origin, _, minted := strings.Cut(ev.ID, task.GateSep)
	if !minted {
		origin = ""
	}
	for k, v := range map[string]string{
		"origin":  origin,
		"mode":    ev.Mode,
		"reason":  ev.Text,
		"tool":    ev.Tool,
		"task":    taskID,
		"subject": subject,
	} {
		if v != "" {
			rec[k] = v
		}
	}
	rec[AnswerersField] = answerersValue(answerers)
	// What the question offers, as a posed one carries it: the frame's options, or the
	// default two when the worker named none, and its deadline when it named one.
	q := task.GateQuestion{Options: ev.Options}
	rec["options"] = optionList(q.OptionsOrDefault())
	if ev.Deadline != nil && ev.Deadline.Within != "" {
		d := entity.Obj{"within": ev.Deadline.Within}
		if ev.Deadline.Default != "" {
			d["default"] = ev.Deadline.Default
		}
		rec["deadline"] = d
	}
	// The record first, for the reason Pose writes it first: a question standing without one is
	// everybody's to answer.
	if err := poseRecord(store, task.GateQuestion{ID: ev.ID, Answerers: answerers}, taskID); err != nil {
		fmt.Fprintf(os.Stderr, "verse: question %s is NOT posed — who may answer it could not be "+
			"written, so nobody is asked until it is posed again: %v\n", ev.ID, err)
		return false
	}
	writeAt(store, ref, rec)
	// Whom it waits on, read back from the record just written, as PostQuestion does — and with
	// `status`, since a settled question waits on nobody and a late frame must not say otherwise.
	posed[WaitingOnField] = waitingOn(store, ref)
	// Still pending, or not there at all — a brokered question whose only writer is this frame.
	// `!not` over the two settled values rather than an equality on `pending`, so a status this
	// does not know about is not silently treated as re-posable.
	_, _, _ = store.Write(entity.Write{
		At:    ref.At(),
		Value: posed.Payload(),
		When:  []entity.Require{notStatus(QuestionAnswered), notStatus(QuestionOrphaned)},
	})
	return true
}

// notStatus is the precondition "this record does not say status: s".
//
// Asked of the WHOLE payload rather than of the `status` field, which is what makes it true for
// a record that has no status yet — the brokered case, where this frame is the first writer.
// A field-level `!not` would be a non-match against an absent field and would refuse the very
// write that creates it.
func notStatus(s string) entity.Require {
	return entity.Require{Pattern: entity.Obj{"status": s}.Payload().WithTag("!not")}
}

// AnswerQuestion settles a question. It records rather than deletes: an answered question is
// the evidence trail — who decided, when, and what they were deciding — and a docket that
// forgets is one nobody can audit.
//
// This is written FROM the decision, after the fact. Nothing reads a verdict here and acts on
// it; the answer travelled out-of-band and this is its record. A reaction wired to this field
// would be the command entity the steering invariant forbids.
func AnswerQuestion(store entity.Store, id string, res task.Resolution) {
	rec := answerRecord(res)
	// REMOVED, not absent: a write MERGES, so an answer that was recorded as in-flight would
	// stay in-flight forever unless this says otherwise. Delivery is the news here.
	rec["delivered"] = entity.Removed()
	writeAt(store, QuestionRef(id), rec)
}

// QueuedAnswer records an answer that has been ACCEPTED but not yet delivered — taken by a
// broker for a worker that must dial in and pull it.
//
// The gate is closed either way: nobody else may answer, because two answers to one question
// is a race with one winner however slowly the answer travels. What is not yet true is that
// the party who must act on it knows. Recording that as done is what let an answer evaporate
// with a worker that never came back, while every surface said the question was settled.
//
// `delivered: false` is present only while that is the case; it is cleared when the worker's
// acknowledgement arrives. Absent means delivered, which is what it has always meant.
//
// UNLESS THE ACKNOWLEDGEMENT IS ALREADY IN. The answer is delivered before it is recorded
// (Server.settle), so a quick worker's decision frame can be projected first, and it settles the
// question as the worker's for want of anything else to go on. Written over that, `delivered:
// false` would never clear — the acknowledgement it waits for has been and gone. The write is
// therefore conditional on the question not being answered yet, and where it is, the answer is
// recorded as what it now is: delivered, and the answerer's.
func QueuedAnswer(store entity.Store, id string, res task.Resolution) {
	rec := answerRecord(res)
	rec["delivered"] = false
	committed, _, err := store.Write(entity.Write{
		At: QuestionRef(id).At(), Value: rec.Payload(), When: []entity.Require{notStatus(QuestionAnswered)},
	})
	if err == nil && !committed {
		AnswerQuestion(store, id, res)
	}
}

func answerRecord(res task.Resolution) entity.Obj {
	rec := entity.Obj{
		"status":     QuestionAnswered,
		"verdict":    res.Chosen(),
		"answeredAt": time.Now().UTC().Format(time.RFC3339),
		// Who it waited on is a present-tense fact, and it is settled: `by` says who decided,
		// `answerers` whom the gate named, `ballots` who voted.
		WaitingOnField: entity.Removed(),
	}
	if res.By != "" {
		rec["by"] = res.By
	}
	if res.Via != "" {
		rec["via"] = res.Via
	}
	// The settling ballot rides the settle. One write says both that this answerer chose
	// this and that the choice reached its threshold, so no reader can see the one without
	// the other. A settle nobody cast — auto, cancelled, the clock — is not a ballot.
	if res.Cast() {
		rec["ballots"] = entity.Obj{res.By: ballot(res)}
	}
	if res.Note != "" {
		rec["why"] = res.Note
	}
	// What the answerer said alongside the verdict. It rides Steer rather than Note because
	// that is where an answer's prose goes — `verse question approve --say "but not on friday"`
	// is an instruction to the task, and the record of a decision that omitted it would
	// keep the yes and lose the condition on it.
	if len(res.Steer) > 0 {
		rec["say"] = anyStrings(res.Steer)
	}
	return rec
}

// OrphanQuestions settles every question a task left open, for a task that has ended.
// Nobody decided them and nobody now can: the asker is gone, and an answer would reach a run
// that is over.
//
// It is a settle, not a delete, for the reason an answered one is: what was asked, and the
// fact that it was never decided, is the trail. `orphaned` is the same word a restart uses
// for the same fact — the party who could have answered no longer exists.
func OrphanQuestions(store entity.Store, taskID string) {
	for _, ent := range entity.Below(store, func(ref entity.Ref) bool {
		return IsQuestion(ref)
	}, 1, entity.VerseRoot, AskSystem) {
		p := entity.ToObj(ent.Payload)
		if p["status"] != QuestionPending {
			continue
		}
		why := "the task that raised it ended without it being answered"
		switch {
		case p["task"] == taskID:
		case p[SaidToField] == taskID:
			why = "the task ended before it heard this"
		default:
			continue
		}
		settlePending(store, ent.Ref, entity.Obj{
			"status":     QuestionOrphaned,
			"orphanedAt": time.Now().UTC().Format(time.RFC3339),
			"why":        why,
		})
	}
}

// SaidToField marks an ask that is words said to a running task (9ce3bva0, stage 6), naming the
// task. The task is its party: it is heard when the task takes the words at its next boundary
// (HearSay), and no person answers it.
const SaidToField = "saidTo"

// Heard is what a say settles on when its task takes it.
const Heard = "heard"

// HearSay settles the say ask id as heard by taskID, where it stands waiting and is words to that
// task: the task's say frame echoes the ask it took (task.SayRequest.Ask).
func HearSay(store entity.Store, id, taskID string) {
	ref := QuestionRef(id)
	e, ok, err := store.Get(ref)
	if err != nil || !ok || entity.Str(e.Payload, SaidToField) != taskID {
		return
	}
	settlePending(store, ref, entity.Obj{
		"status": QuestionAnswered, "verdict": Heard, "answer": Heard, "by": Ref(taskID).String(),
		"answeredAt": time.Now().UTC().Format(time.RFC3339),
	})
}

// DropQuestion settles one question whose asker stopped waiting for it, with `why` saying what
// stopping meant — the task ended, or its loop withdrew the question because an operator
// intervened. Either way there is nobody left to receive an answer.
//
// Same status as a task that ended holding open gates, because it is the same fact from a
// smaller scope: nothing will act on an answer to this. `why` is what separates the two for a
// reader, and it is the reason this takes one — a docket that says only `orphaned` reads as
// "the task died" for a run that is alive and was redirected.
func DropQuestion(store entity.Store, id, why string) { dropQuestion(store, id, why, nil) }

// HoldQuestion is DropQuestion for a suspension's withdrawal: the question is withdrawn to be asked
// again when the suspension lifts, and says so (`suspended`), so it is kept meanwhile rather than
// going as a settled ask does (versed askttl.go).
func HoldQuestion(store entity.Store, id, why string) {
	dropQuestion(store, id, why, entity.Obj{SuspendedField: true})
}

// SuspendedField marks a question a suspension withdrew, to be asked again when it lifts.
const SuspendedField = "suspended"

func dropQuestion(store entity.Store, id, why string, extra entity.Obj) {
	e, ok, err := store.Get(QuestionRef(id))
	if err != nil || !ok {
		return
	}
	// Only a still-pending one. An answer and a cancellation can cross, and the answer is
	// the fact worth keeping: it says what was decided, where this says only that nobody
	// will decide it now.
	if entity.ToObj(e.Payload)["status"] != QuestionPending {
		return
	}
	rec := entity.Obj{
		"status":     QuestionOrphaned,
		"orphanedAt": time.Now().UTC().Format(time.RFC3339),
	}
	if why != "" {
		// `why` and not `reason`: reason is the gate's own account of why it MATCHED, written
		// when the question was posed, and overwriting it would lose what was being asked in
		// favour of why nobody answered. `why` is where a settling's account already goes —
		// see answerRecord, which puts an answerer's note there.
		rec["why"] = why
	}
	for k, v := range extra {
		rec[k] = v
	}
	settlePending(store, QuestionRef(id), rec)
}

// settlePending writes a settling onto a question ONLY WHILE IT IS STILL PENDING, in the write
// itself. The read the callers make ahead of it is a courtesy that says nothing about the
// moment of the write: a withdrawal and the terminal sweep race, each reads pending, and the
// second to land used to overwrite the first with its own account. The precondition is what
// makes the first settling the one that stands, whichever it is.
func settlePending(store entity.Store, ref entity.Ref, rec entity.Obj) {
	rec[WaitingOnField] = entity.Removed() // nobody: it is settled
	_, _, _ = store.Write(entity.Write{
		At:    ref.At(),
		Value: rec.Payload(),
		When:  []entity.Require{notStatus(QuestionAnswered), notStatus(QuestionOrphaned)},
	})
}

// SettledBy reports who answered a question, and whether it is settled at all. It is the
// discriminator between "somebody beat you to it" and "there is no such question" — two facts
// that were one 404 before the record existed to tell them apart.
//
// A question nobody has answered reports false, as does one that was never posed: from the
// answerer's side those are the same, since neither is a decision they lost.
// The READ's failure is reported rather than folded into "nobody answered": this decides whether
// an answerer is told they lost a race, and an unreadable store saying "you did not" is the one
// wrong answer that cannot be retried into.
func SettledBy(store entity.Store, id string) (string, bool, error) {
	e, ok, err := store.Get(QuestionRef(id))
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	p := entity.ToObj(e.Payload)
	if p["status"] != QuestionAnswered {
		return "", false, nil
	}
	by, _ := p["by"].(string)
	if by == "" {
		by = "an earlier answer"
	}
	return by, true, nil
}

// Counted reports whether an answer is on a settled record as one of the ballots behind its
// outcome: the record settled on what this answerer chose, and their ballot is there. It is
// the difference between losing a race and being part of the decision that won it.
func Counted(store entity.Store, id string, res task.Resolution) bool {
	e, ok, err := store.Get(QuestionRef(id))
	if err != nil || !ok {
		return false
	}
	p := e.Payload
	if entity.Str(p, "status") != QuestionAnswered || entity.Str(p, "verdict") != res.Chosen() {
		return false
	}
	return entity.Str(entity.Get(entity.Get(p, "ballots"), res.By), "choice") == res.Chosen()
}

// StaleAnswer records an answer that arrived after the question was settled. Losing a race is
// an outcome, not an error: the answerer decided something, and a docket that keeps only the
// winner cannot show that a question was contested.
//
// Each loser is written UNDER ITS OWN key rather than appended to a list, so two of them
// racing cannot lose each other — a merge of distinct keys needs no read-modify-write, and
// there is nothing here to serialize. One answerer who answers late twice overwrites their own
// entry, which is right: it is their answer, and the later one is what they think.
func StaleAnswer(store entity.Store, id string, res task.Resolution) {
	by := res.By
	if by == "" {
		by = "unattributed"
	}
	rec := entity.Obj{
		"verdict": res.Chosen(),
		"at":      time.Now().UTC().Format(time.RFC3339),
	}
	if len(res.Steer) > 0 {
		rec["say"] = anyStrings(res.Steer)
	}
	// WRITTEN AT `stale`, NOT AT `stale.<by>`, because a write does not create the path it
	// descends through: nothing else writes this field, so the first stale answer to a question
	// would be descending through a key that does not stand yet. One step shallower reaches the
	// same document — the record keyed by who gave it, accumulating across answerers — and its
	// own parent is the question's payload, which stands.
	_, _, _ = store.Write(entity.Write{
		At:    QuestionRef(id).At("stale"),
		Value: entity.Obj{by: rec}.Payload(),
	})
}

// effectLists flattens derived effects into the three lists a gate predicate speaks in.
// Empty when an action classified to nothing, which is a real state: a question whose tool
// derived no effects still publishes its id, gate and subject, because a tool nothing could
// classify must not vanish from the docket.
func effectLists(effs []task.Effect) entity.Obj {
	var verbs, resources, paths []any
	seen := map[string]bool{}
	for _, e := range effs {
		if v := string(e.Verb); v != "" && !seen["v:"+v] {
			seen["v:"+v] = true
			verbs = append(verbs, v)
		}
		if r := e.Resource.Ref; r != "" {
			resources = append(resources, entity.Obj{"kind": string(e.Resource.Kind), "ref": r})
			if e.Resource.Kind == task.KindFile && !seen["p:"+r] {
				seen["p:"+r] = true
				paths = append(paths, r)
			}
		}
	}
	out := entity.Obj{}
	for k, v := range map[string][]any{"verbs": verbs, "resources": resources, "paths": paths} {
		if len(v) > 0 {
			out[k] = v
		}
	}
	return out
}

// clearPriorAnswer prepares a record that is being posed AGAIN under an id that already
// settled. A firing's question comes back under its own id when it stood orphaned — a restart's
// replay, a suspension lifted — and never over an answer: a firing after an answer asks a new
// question under an id of its own (trigger.Engine.askingID). A poser that does post over an
// answer is still read as asking again rather than refused, for the reason below.
//
// Without it the write merges and the record says two things at once: pending, and answered by
// somebody with a verdict and a time. What a person then reads on the docket is a question to
// decide, already stamped with who decided it and what they said. Nil rather than absent,
// because a write merges and saying "that is gone" is the writer's job.
//
// It does NOT refuse the post. The record is a projection of a registry that is genuinely
// waiting for an answer, so a post that declined to write would leave a live question invisible
// on every surface — worse than the thing it fixed. The earlier answer is not lost either: it
// is at the commit that made it, which is what the store is for.
//
// `askedAgain` marks it, because "you have seen this before" is the fact a reader wants and the
// cleared fields can no longer say.
func clearPriorAnswer(store entity.Store, ref entity.Ref, rec entity.Obj) {
	e, ok, err := store.Get(ref)
	if err != nil || !ok {
		return
	}
	// AN ASK ASKED AGAIN IS NOT GOING. Its expiry was the settled round's, and a question that
	// waits again stands until it settles again (versed askttl.go).
	if entity.Get(e.Payload, "expires") != nil {
		rec["expires"] = entity.Removed()
	}
	// Nor is it held any more: the suspension that withdrew it has lifted.
	if entity.Get(e.Payload, SuspendedField) != nil {
		rec[SuspendedField] = entity.Removed()
	}
	status := entity.Str(e.Payload, "status")
	switch status {
	case QuestionAnswered, QuestionOrphaned:
	default:
		return
	}
	for _, k := range []string{"verdict", "by", "via", "why", "say", "answeredAt", "delivered"} {
		rec[k] = entity.Removed()
	}
	// The ballots clear only when a ROUND ended: a question that was answered and is posed
	// again is a new question under an old id, and the earlier ballots answered the earlier one. An
	// orphaned record is the same question with its waiter gone — a restart, a withdrawal —
	// and whoever re-poses it joins what was already cast. The deadline's stamp stands for
	// the same reason: the clock started when the question was first asked.
	if status == QuestionAnswered {
		rec["ballots"] = entity.Removed()
		// The INSTANT goes with the round it timed; the terms are this asking's own, written
		// above, and removing the whole deadline would take them with it — a question asked
		// again with `within: 72h` would stand with no deadline at all.
		if d, ok := rec["deadline"].(entity.Obj); ok {
			d["at"] = entity.Removed()
		} else {
			rec["deadline"] = entity.Removed()
		}
	}
	rec["askedAgain"] = true
}

func writeAt(store entity.Store, ref entity.Ref, o entity.Obj) {
	_, _, _ = store.Write(entity.Write{At: ref.At(), Value: o.Payload()})
}

// IsQuestion reports whether an address is a posed question's — see IsTask on why a namespace
// answers this for itself now.
func IsQuestion(ref entity.Ref) bool { return IsAsk(ref) }

// ResumeDeadline gives a question that was withdrawn while it stood the time it had left, from
// now. A withdrawn question stands in front of nobody, so its clock stopped when it was withdrawn
// (`orphanedAt`) and starts again when it is asked again: the deadline is moved to now plus what
// remained. One whose deadline had already passed when it was withdrawn is left as it is, and
// settles when it is asked. Anything but an orphaned record with a stamped deadline is left alone.
func ResumeDeadline(store entity.Store, id string, now time.Time) error {
	e, ok, err := store.Get(QuestionRef(id))
	if err != nil || !ok {
		return err
	}
	if entity.Str(e.Payload, "status") != QuestionOrphaned {
		return nil
	}
	_, _, at := DeadlineOf(e.Payload)
	fires, err1 := time.Parse(time.RFC3339Nano, at)
	withdrawn, err2 := time.Parse(time.RFC3339Nano, entity.Str(e.Payload, "orphanedAt"))
	if err1 != nil || err2 != nil {
		return nil
	}
	left := fires.Sub(withdrawn)
	if left <= 0 {
		return nil
	}
	_, _, err = store.Write(entity.Write{
		At:    QuestionRef(id).At("deadline", "at"),
		Value: entity.Val(now.Add(left).UTC().Format(time.RFC3339Nano)),
	})
	return err
}

// ownerOnRecord is the owner a node's own perms record names, "" where it has none.
func ownerOnRecord(store entity.Store, ref entity.Ref) (string, error) {
	rec, ok, err := store.Get(entity.PermsRef(ref))
	if err != nil || !ok {
		return "", err
	}
	return entity.Str(rec.Payload, entity.OwnerField), nil
}
