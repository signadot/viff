package slack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	verseapi "github.com/signadot/verse/api"
	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// The out half: what the verse asks to be said in Slack, said, and what became of it written back.
//
// Somebody (a person, a charter) writes an INPUT at `slack.<ws>.out.<target>.<name>`:
//
//	text: "the review of demo.pr.1 is waiting on you"
//	asks: "review@demo.pr.1~d3"   # optional: the question(s) this message is about
//
// and this answers with its STATUS at the sibling `slack.<ws>.out.<target>.<name>~status`:
//
//	channel: D0EXAMPLE000      # where it landed: the DM for a person, the channel itself otherwise
//	ts: "1790086509.181649"   # the message, which is what an update needs
//	permalink: https://…
//	postedAt / updatedAt
//	posted: 3f2a…             # the fingerprint of the message as it now shows
//	error: …                  # the last failure, retried on the next pass; absent once it works
//
// `<target>` is a Slack id: `U…` is a person, whose DM is opened; `C…`, `G…` and `D…` are said in
// as they are. `<name>` is the writer's.
//
// A SIBLING, NOT A CHILD. An input is the writer's document and grows structure (blocks, buttons);
// a status inside it would one day collide with a field of the same name. `~` is verse's mark for
// a name derived from another (a question id is `<task>~<gate>`), and a name ending in `~status` is
// never an input.
//
// The status is this program's alone by the permissions model rather than by convention: it holds
// `child-owner` on the out place, so a status it creates is its own and carries an owner record,
// and that record stops the requesters' grants from above. Everyone may read it.
//
// A PASS IS LEVEL-TRIGGERED, the source's law run backwards: it reads what is asked, lays out the
// message it asks for, compares that with what its status says was posted, and does the
// difference. Nothing is remembered between passes but what the status holds, so a restart resumes
// rather than repeats: a message whose fingerprint the status holds is in step, one whose
// fingerprint moved is an update, and one with no ts yet is a post. The fingerprint is of the
// message rather than the input, because a message about asks also says what became of them
// (see layout), and a question settling is an update nobody wrote. This program never
// writes an input, so its own writes are never new requests.
//
// Removing an input leaves its status, so the ts survives for whoever later decides what a
// removal should do to the message; for now the message stays as it is.
type Sink struct {
	s     *Slack
	verse *client.Client
	now   func() time.Time
	// As reads an ask as the person a message is for, where the input names one (9ce3bva0): an
	// ask is read by whom it asks, and this program only ever shows it to them. The person is
	// named with their Slack account, so a verse that verifies people admits the read through
	// its binding. Nil reads as this program, which reads what a rule's gate holds out and
	// nothing a person's ask does not.
	As func(principal, account string) *client.Client

	mu sync.Mutex
	// accounts is who each person is in Slack, by email, once looked up — or that they are
	// nobody there: the account a read as them is made with, and the one Slack lookup per person.
	accounts map[string]lookedUp
	// (channel, where replies land) → input → the questions its message is asking. Where replies
	// land is the message's own ts, or the thread it was posted into: several inputs can speak in
	// one thread, and a reply there is about all of their questions.
	asked map[string]map[string][]Asked
}

// Sink is this workspace's out half: what it says in Slack, and what it remembers of its own
// messages. The pass that drives it, the status it writes and the failures it records are the io
// framework's (verseio.Out); what is here is Slack.
func (s *Slack) Sink(verse *client.Client) *Sink {
	return &Sink{s: s, verse: verse, now: time.Now}
}

// OutRef is the place inputs are written under.
func (s *Slack) OutRef() entity.Ref { return entity.RefAt(s.place("out")...) }

// Deliver says one input in Slack, or updates what it already said, and answers what to record.
//
// WHETHER ANYTHING IS DUE is decided here rather than by the framework, because only this half
// knows what a message shows: the fingerprint is of the drawn message, not of the input, so a
// question settling is an update nobody wrote and a field the writer added that changes nothing
// visible is not. Every pass re-reads the questions and re-remembers what this message is asking,
// including the passes that say nothing to Slack, which is how a reply in a thread is matched to
// the question that thread is about (socket.go).
func (k *Sink) Deliver(ctx context.Context, in verseio.Input) (entity.Obj, error) {
	text := entity.Str(in.Value, "text")
	if text == "" {
		return nil, verseio.Permanent(fmt.Errorf("an input carries {text}, and this one has none"))
	}
	body, asked, err := k.body(ctx, text, in.Value, in.To)
	if err != nil {
		return nil, err
	}
	fp := fingerprint(body)
	channel, ts := entity.Str(in.Status, "channel"), entity.Str(in.Status, "ts")
	// A THREAD, when the input names one: the message is a reply there, and a reply to it lands in
	// that thread rather than under the message itself.
	thread := entity.Str(in.Value, "thread")
	replies := func(ts string) string {
		if thread != "" {
			return thread
		}
		return ts
	}

	// NEVER CREATE A MESSAGE ABOUT A QUESTION THAT DOES NOT STAND.
	//
	// A message whose every question is unreadable says only that: "No question `…` stands",
	// sent to a person who can do nothing with it and did not ask. That is what an orphaned
	// question produces today — the charter selects it as the system, which can read it, and
	// this program cannot — and one message per person per pass is how it arrives.
	//
	// A REDRAW is the opposite case and keeps the line. There the message exists, somebody was
	// asked, and what became of the question is the news; saying nothing would leave them
	// waiting on a question that is gone. So the rule is about creating, not about saying.
	//
	// The refusal lands on the status and the next pass tries again, which is also right for the
	// race it covers: an input derived a moment before its question became readable here.
	if ts == "" && len(asked) > 0 && !anyStanding(asked) {
		return nil, fmt.Errorf("nothing to say: this asks about %s, and no such question stands "+
			"here — a message about a question nobody can read tells its reader nothing",
			strings.Join(askedIDs(asked), ", "))
	}
	// REMEMBERED ONCE SLACK SHOWS IT, and not before: what is remembered includes the edit each ask
	// stands at, which is what a reply here is taken to answer, so remembering a rewording the
	// message does not show yet would count a reply to the old words against the new ones.
	if ts != "" && entity.Str(in.Status, "posted") == fp {
		k.remember(channel, replies(ts), in.At.String(), asked)
		return nil, nil // in step: the message shows this input
	}
	now := k.now().UTC().Format(time.RFC3339)

	if ts != "" {
		err := k.s.api.updateMessage(ctx, channel, ts, body)
		if err == nil {
			k.remember(channel, replies(ts), in.At.String(), asked)
			return entity.Obj{"updatedAt": now, "posted": fp}, nil
		}
		// GONE: somebody deleted the message. The input still asks for one showing this, so it is
		// said again, rather than an edit of what is not there refused for good
		// (git-issue ne9fce9dh12ks1q4qsn0). A reply under the old one has nowhere to land.
		if !strings.Contains(err.Error(), "message_not_found") {
			return nil, err
		}
		if thread == "" {
			k.remember(channel, ts, in.At.String(), nil)
		}
	}

	if channel == "" {
		if channel, err = k.channelFor(ctx, in.To); err != nil {
			return nil, err
		}
	}
	landed, ts, err := k.s.api.postMessage(ctx, channel, thread, body)
	if err != nil {
		return nil, err
	}
	st := entity.Obj{"channel": landed, "ts": ts, "postedAt": now, "updatedAt": now, "posted": fp}
	if thread != "" {
		st["thread"] = thread
	}
	// A permalink is for a reader and not for this program, so failing to get one does not undo
	// a post that happened.
	if link, err := k.s.api.permalink(ctx, landed, ts); err == nil && link != "" {
		st["permalink"] = link
	}
	k.remember(landed, replies(ts), in.At.String(), asked)
	return st, nil
}

// body is the message an input asks for: its text, and beneath it a line of this program's own for
// each question the input is about, and how to answer the ones still pending. It answers the
// questions still open too, which is what the out half remembers of its own messages (Asked).
//
// Those lines are not the writer's to word. They say what each question's record says NOW — who it
// waits on while it is pending, the verdict and who gave it once answered, why it was withdrawn —
// so a question settling redraws a message nobody wrote to, and everyone who can see it is told,
// not only whoever answered. An input that asks about nothing is its text and no more.
func (k *Sink) body(ctx context.Context, text string, input *ir.Node, to string) (string, []Asked, error) {
	asks := verseio.Strs(entity.Get(input, "asks"))
	if len(asks) == 0 {
		return text, nil, nil
	}
	said := []string{text}
	var asked []Asked
	var options []task.Option
	reader, whom := k.readerFor(ctx, to)
	for _, id := range asks {
		q, err := k.question(reader, whom, id)
		if err != nil {
			return "", nil, err
		}
		said = append(said, "> "+q.line())
		if q.about != "" {
			said = append(said, "> _"+q.about+"_")
		}
		// Settled ones stay in what this message is asking, with no options on them: a thread
		// outlives its decision, and what people say there afterwards — why it went that way,
		// what it cost — is worth keeping on the question rather than dropping on the floor.
		asked = append(asked, Asked{ID: id, Options: optionIDs(q.opts), Settled: !q.pending(),
			Standing: q.standing, Line: q.line(), Edit: q.edit, Served: q.servedBy})
		if !q.pending() {
			continue
		}
		for _, opt := range q.opts {
			if !slices.Contains(optionIDs(options), opt.ID) {
				options = append(options, opt)
			}
		}
	}
	// How to answer, in the words the question itself takes. A reply in this thread is the whole
	// interface: it needs no block to render, and it is what one person would have typed to
	// another.
	if len(options) > 0 {
		said = append(said, "Answer in this thread: "+choices(options))
	}
	return strings.Join(said, "\n"), asked, nil
}

// Asked is one question a posted message is about, and what it takes to answer it: what the out
// half remembers of its own messages, so a reply in a thread can be matched to the question that
// thread is asking (socket.go).
type Asked struct {
	ID      string
	Options []string
	// Standing says the question could be read at all. A question record carries the grants
	// versed wrote at posing, so one that has outlived them is invisible to this program while
	// the docket — which reads as the system — goes on listing it.
	Standing bool
	// Settled says the question takes no more answers. It stays in the index anyway, because a
	// thread goes on after its decision and what is said there belongs on the question.
	Settled bool
	// Line is what the message says of it, for telling somebody who answers too late what
	// became of it.
	Line string
	// Edit is the edit of the ask the message shows, which a reply answers: one to words since
	// changed is refused (api.Resolve.Edit).
	Edit int
	// Served names the resolver serving the ask for whom the message is for, where one does:
	// answering it yourself then takes `force` (9ce3bva0, stage 5).
	Served string
}

// anyStanding says whether any of the questions an input asks about could be read.
func anyStanding(asked []Asked) bool {
	for _, a := range asked {
		if a.Standing {
			return true
		}
	}
	return false
}

// askedIDs are the questions an input names, for a refusal that says which.
func askedIDs(asked []Asked) []string {
	out := make([]string, 0, len(asked))
	for _, a := range asked {
		out = append(out, a.ID)
	}
	return out
}

// Asked is the questions a reply at (channel, ts) is about — ts the thread it lands in — as the
// last pass read them, each saying whether it still takes an answer: those of the message posted
// there and of every input posted into that thread, by the inputs' addresses.
// Nothing, for a thread this program asked nothing in.
func (k *Sink) Asked(channel, ts string) []Asked {
	k.mu.Lock()
	defer k.mu.Unlock()
	by := k.asked[channel+"/"+ts]
	names := make([]string, 0, len(by))
	for name := range by {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []Asked
	for _, name := range names {
		out = append(out, by[name]...)
	}
	return out
}

// remember indexes what one input's message asks, by where a reply to it lands, for Asked.
func (k *Sink) remember(channel, replies, input string, asked []Asked) {
	if channel == "" || replies == "" {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	key := channel + "/" + replies
	if len(asked) == 0 {
		delete(k.asked[key], input)
		if len(k.asked[key]) == 0 {
			delete(k.asked, key)
		}
		return
	}
	if k.asked == nil {
		k.asked = map[string]map[string][]Asked{}
	}
	if k.asked[key] == nil {
		k.asked[key] = map[string][]Asked{}
	}
	k.asked[key][input] = asked
}

// code is a list as Slack renders code: `proceed`, `deny`.
func code(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = "`" + w + "`"
	}
	return strings.Join(out, ", ")
}

// choices is what a question takes, each word with what choosing it means when the gate said:
// three ids in a row is a question answered by guessing.
func choices(opts []task.Option) string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = "`" + o.ID + "`"
		if o.Says != "" {
			out[i] += " — " + o.Says
		}
	}
	return strings.Join(out, ", ")
}

// optionIDs is what those options are answered with.
func optionIDs(opts []task.Option) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.ID
	}
	return out
}

// posed is what a message says of a question it is about, as its record says it.
type posed struct {
	id       string
	pose     string // what it asks, in the words of whoever asked: a gate's pose, a task's reason
	about    string // what its subject says about itself
	standing bool   // a record stands at the question's place
	status   string
	verdict  string
	by       string
	why      string
	say      []string
	waiting  []string
	due      time.Time // when the clock settles it, and what it settles on
	settles  string
	opts     []task.Option // what it takes to answer, in the order the gate wrote them
	ballots  []string      // "<who> <choice>", and what they said with it, in name order
	spoken   bool          // some ballot carried words
	edit     int           // the edit of the ask this shows
	asker    string        // who asked, for a person's ask
	on       []string      // what each row of its `on` runs, in words, in order
	via      string        // the resolver an answer came through
	servedBy string        // the resolver serving it for whom this message is for
}

// readerFor is who reads the asks a message is about: the person it is for, where the input names
// one verse knows, and this program otherwise. A person is read as with their Slack account, found
// by their email once and kept: the account is what a verse that verifies people admits the read
// through. A person Slack does not know is read as this program, which is what the message to
// them would have failed on anyway.
func (k *Sink) readerFor(ctx context.Context, to string) (*client.Client, string) {
	if k.As == nil {
		return k.verse, ""
	}
	g, err := entity.ParsePrincipal(to)
	if err != nil || (g.Kind != entity.UserKind && g.Kind != entity.AgentKind) {
		return k.verse, ""
	}
	acct := ""
	if g.Kind == entity.UserKind {
		if acct, err = k.accountOf(ctx, g.ID); err != nil {
			k.s.log("slack out: who %s is in Slack: %v", g.ID, err)
			return k.verse, ""
		}
	}
	return k.As(g.String(), acct), g.String()
}

// accountOf is the Slack account of the member with this email, `slack:<user id>`, asked of Slack
// once per person and kept, the failure included: a person Slack does not know is asked about once
// and not on every pass of every input that names them, which with a few such inputs is Slack's
// rate limit. A person who joins the workspace later is found after a restart.
func (k *Sink) accountOf(ctx context.Context, email string) (string, error) {
	k.mu.Lock()
	known, ok := k.accounts[email]
	k.mu.Unlock()
	if ok {
		return known.acct, known.err
	}
	id, err := k.s.api.lookupByEmail(ctx, email)
	known = lookedUp{err: err}
	if err == nil {
		known.acct = account(id)
	}
	k.mu.Lock()
	if k.accounts == nil {
		k.accounts = map[string]lookedUp{}
	}
	k.accounts[email] = known
	k.mu.Unlock()
	return known.acct, known.err
}

// lookedUp is what Slack said of an email, once: the account, or why there is none.
type lookedUp struct {
	acct string
	err  error
}

// question reads the record of the question with this id, where the mirror keeps it.
func (k *Sink) question(reader *client.Client, whom, id string) (posed, error) {
	q := posed{id: id}
	doc, err := verseio.ReadAt(reader, mirror.QuestionRef(id).KPath())
	if err != nil {
		return q, fmt.Errorf("reading question %s: %w", id, err)
	}
	if doc == nil {
		return q, nil
	}
	q.standing = true
	q.pose, q.about = entity.Str(doc, "pose"), entity.Str(doc, "about")
	// A QUESTION THAT WROTE NO POSE IS ASKED IN ITS REASON. A task or a worker raising a
	// question says it there (task.GateQuestion), so without this somebody asked by a task
	// reads an id and two options. A rule's gate that wrote no pose has the rule, the gate and
	// the subject there, which says what is asked about where the id does not.
	if q.pose == "" {
		q.pose = entity.Str(doc, "reason")
	}
	if _, dflt, at := mirror.DeadlineOf(doc); at != "" {
		q.due, _ = time.Parse(time.RFC3339Nano, at)
		q.settles = dflt
	}
	q.status, q.verdict = entity.Str(doc, "status"), entity.Str(doc, "verdict")
	q.by, q.why = entity.Str(doc, "by"), entity.Str(doc, "why")
	q.edit, q.asker, q.via = mirror.EditOf(doc), mirror.PrincipalFrom(doc), entity.Str(doc, "via")
	q.on = onWords(entity.Get(doc, "on"))
	if whom != "" && q.status == mirror.QuestionPending {
		q.servedBy = servedFor(reader, id)
	}
	q.say = verseio.Strs(entity.Get(doc, "say"))
	q.waiting = verseio.Strs(entity.Get(doc, mirror.WaitingOnField))
	if ir.Uncomment(entity.Get(doc, "options")) != nil {
		q.opts = mirror.OptionsOf(doc)
	}
	ballots := entity.Get(doc, "ballots")
	for _, who := range verseio.Fields(ballots) {
		b := entity.Get(ballots, who)
		cast := person(who) + " " + entity.Str(b, "choice")
		// What somebody said with their ballot is half of what they decided, and a channel that
		// shows the count and drops the words shows the lesser half.
		if said := strings.Join(verseio.Strs(entity.Get(b, "say")), "; "); said != "" {
			cast += " — " + said
			q.spoken = true
		}
		q.ballots = append(q.ballots, cast)
	}
	return q, nil
}

// pending reports whether the question still takes an answer. A verdict is written only by the
// settle, in the same write as the answered status, so a record carrying one is answered even
// when another writer's pending status reached it after.
func (q posed) pending() bool {
	return q.standing && q.status == mirror.QuestionPending && q.verdict == ""
}

// line is this program's line about the question, in the message above the options it takes.
func (q posed) line() string {
	id := "`" + q.id + "`"
	switch {
	case !q.standing:
		return "No question " + id + " stands"
	case q.pending():
		// What the gate asks, in its own words, and the id only when it wrote none: somebody
		// answering does not need the address of what they are answering.
		line := "Answers " + id
		if q.pose != "" {
			line = q.pose
		}
		if q.asker != "" {
			line += " · asked by " + person(q.asker)
		}
		// WHAT AN ANSWER RUNS, shown as it stands at this edit: the answer is consent to it, and
		// a change to it is a new asking that redraws this message and refuses a reply to the old.
		for _, w := range q.on {
			line += " · " + w
		}
		if len(q.waiting) > 0 {
			line += " · waiting on " + people(q.waiting)
		}
		if len(q.ballots) > 0 {
			line += " · so far: " + strings.Join(q.ballots, ", ")
		}
		if !q.due.IsZero() {
			// Silence decides this one, so the message says so while it can still be answered.
			// The instant is Slack's own date token, which every reader sees in their own
			// timezone; the fallback is what a client that cannot render it shows.
			when := fmt.Sprintf("<!date^%d^{time}|%s>", q.due.Unix(), q.due.UTC().Format("15:04Z"))
			if q.settles == "" {
				line += " · closes at " + when + ", decided by nobody"
			} else {
				line += " · `" + q.settles + "` at " + when + " unless answered"
			}
		}
		// A RESOLVER ANSWERS THIS FOR YOU: shown, since you chose it, and answerable yourself by
		// saying so (9ce3bva0, stage 5).
		if q.servedBy != "" {
			line += " · served for you by `" + q.servedBy + "`: reply `force <option>` to answer it yourself"
		}
		return line
	case q.verdict != "":
		line := id + " answered *" + q.verdict + "*"
		if q.by != "" {
			line += " by " + person(q.by)
		}
		if q.via != "" {
			line += " via `" + strings.TrimPrefix(q.via, "via:") + "`"
		}
		if said := strings.Join(q.say, "; "); said != "" {
			line += ": " + said
		}
		// Who else voted and what they said with it: worth keeping in front of the channel where
		// more than one voice settled it, and noise where the `by` above says it all.
		if len(q.ballots) > 1 || q.spoken {
			line += " · ballots: " + strings.Join(q.ballots, ", ")
		}
		return line
	case q.status == mirror.QuestionOrphaned:
		line := id + " was withdrawn unanswered"
		if q.why != "" {
			line += ": " + q.why
		}
		return line
	}
	return id + " is " + q.status
}

// person is a principal as a reader in Slack would name it: a person by their email, a wildcard as
// the docket it stands for — there is no list of every person to name — and anyone else as verse
// names them.
//
// As code, because Slack turns an email in a message into a mailto link, and a decision that reads
// `answered by <mailto:ana@acme.test|ana@acme.test>` is a decision nobody can scan.
func person(p string) string {
	if strings.HasSuffix(p, "*") {
		return "the docket"
	}
	return "`" + strings.TrimPrefix(p, "user:") + "`"
}

func people(ps []string) string {
	var out []string
	for _, p := range ps {
		if said := person(p); !slices.Contains(out, said) {
			out = append(out, said) // `user:*` and `agent:*` are one docket, said once
		}
	}
	return strings.Join(out, ", ")
}

// Keep remembers what a message refused for good still asks, as a delivery would, where Slack
// shows the input as it stands — an input retargeted after delivery, say: the message stands, and
// a reply in its thread is still matched to the question it is about (socket.go). Nothing is asked
// of Slack but who the person is, once: the answer, found or not, is kept (accountOf).
func (k *Sink) Keep(ctx context.Context, in verseio.Input) {
	channel, ts := entity.Str(in.Status, "channel"), entity.Str(in.Status, "ts")
	if ts == "" {
		return // refused before anything was posted: there is no thread to match
	}
	body, asked, err := k.body(ctx, entity.Str(in.Value, "text"), in.Value, in.To)
	if err != nil {
		return
	}
	// ONLY WHAT SLACK SHOWS, as a delivery remembers (git-issue ny9fce9dh12ks1q9qsn0). What is
	// remembered includes the edit each ask stands at, which a reply here is taken to answer; an
	// input whose edit was refused is words the message does not show, and remembering them would
	// count a reply to the old words against the new.
	if entity.Str(in.Status, "posted") != fingerprint(body) {
		return
	}
	replies := ts
	if thread := entity.Str(in.Value, "thread"); thread != "" {
		replies = thread
	}
	k.remember(channel, replies, in.At.String(), asked)
}

// channelFor is where a target is said: a person's DM, opened if need be, or the channel itself.
//
// A PERSON MAY BE NAMED AS VERSE KNOWS THEM — `user:ana@acme.test`, or the bare email — and not
// only as Slack does. That is what lets a charter address the people a question waits on: what it
// reads off the record is a principal, and asking it to carry Slack ids would mean somebody
// maintaining a table mapping one to the other, stale precisely when a gate most needs to reach
// somebody. The lookup is Slack's own (users.lookupByEmail), and the DM it opens is written on the
// status, so it is paid once per request rather than once per pass.
func (k *Sink) channelFor(ctx context.Context, target string) (string, error) {
	if email := strings.TrimPrefix(target, "user:"); strings.Contains(email, "@") {
		acct, err := k.accountOf(ctx, email)
		if err != nil {
			return "", err
		}
		return k.s.api.openDM(ctx, strings.TrimPrefix(acct, "slack:"))
	}
	switch {
	case strings.HasPrefix(target, "U"), strings.HasPrefix(target, "W"):
		return k.s.api.openDM(ctx, target)
	case strings.HasPrefix(target, "C"), strings.HasPrefix(target, "G"), strings.HasPrefix(target, "D"):
		return target, nil
	}
	return "", verseio.Permanent(fmt.Errorf("target %q is neither a Slack id (U… a person, C… or G… "+
		"a channel) nor somebody verse knows (user:<email>)", target))
}

// fingerprint is a short, stable digest of a message, so a message that would show differently is
// an update.
func fingerprint(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:8])
}

// onWords is what each row of an ask's `on` runs, in words, in its order: the answers it matches
// and what runs as the asker, or that nothing does, since such a row stops the rows after it.
func onWords(on *ir.Node) []string {
	on = ir.Uncomment(on)
	if on == nil || on.Type != ir.ArrayType {
		return nil
	}
	var out []string
	for _, row := range on.Values {
		cond := "on any answer"
		if m := entity.Get(row, "match"); m != nil {
			// A verdict by name alone reads as the word; a tagged one (`!not proceed`) as the
			// pattern it is, since its word is not what it matches.
			if v := ir.Uncomment(entity.Get(m, "verdict")); v != nil && v.Tag == "" && v.Type == ir.StringType && len(verseio.Fields(m)) == 1 {
				cond = "on *" + v.String + "*"
			} else if b, err := codec.NodeBytesWire(m, codec.Tony); err == nil {
				cond = "on `" + string(b) + "`"
			}
		}
		if w := thenWords(entity.Get(row, "then")); w != "" {
			out = append(out, cond+", runs as the asker: "+w)
		} else {
			out = append(out, cond+", nothing runs")
		}
	}
	return out
}

// thenWords is what a row's then runs, in words, "" for nothing.
func thenWords(then *ir.Node) string {
	if then == nil {
		return ""
	}
	if sp := entity.Get(then, "spawn"); sp != nil {
		if prompt := entity.Str(sp, "prompt"); prompt != "" {
			return "“" + prompt + "”"
		}
		if script := entity.Str(sp, "script"); script != "" {
			return "the script `" + script + "`"
		}
		return ""
	}
	// An answer to another ask, or another ask: what it answers, or whom it asks what.
	if a := entity.Get(then, "answer"); a != nil {
		return fmt.Sprintf("answers `%s` *%s*", entity.Str(a, "ask"), entity.Str(a, "choice"))
	}
	if q := entity.Get(then, "ask"); q != nil {
		who := verseio.Strs(entity.Get(q, "to"))
		line := "asks “" + entity.Str(q, "question") + "”"
		if len(who) > 0 {
			line += " of " + people(who)
		}
		return line
	}
	return ""
}

// servedFor is the resolver serving ask id for whom reader acts as, "" where none does or verse
// did not say.
func servedFor(reader *client.Client, id string) string {
	resp, err := reader.Get("/asks/"+url.PathEscape(id)+"/served", nil)
	if err != nil {
		return ""
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return ""
	}
	var out verseapi.AskServed
	if err := client.Decode(resp, &out); err != nil {
		return ""
	}
	return out.Resolver
}
