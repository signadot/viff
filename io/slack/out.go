package slack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/client"
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
// message rather than the input, because a message with buttons also says what became of their
// questions (see layout), and a question settling is an update nobody wrote. This program never
// writes an input, so its own writes are never new requests.
//
// Removing an input leaves its status, so the ts survives for whoever later decides what a
// removal should do to the message; for now the message stays as it is.
type Sink struct {
	s     *Slack
	verse *client.Client
	now   func() time.Time

	mu    sync.Mutex
	asked map[string][]Asked // (channel, ts) → the questions that message is asking
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
		return nil, fmt.Errorf("an input carries {text}, and this one has none")
	}
	body, asked, err := k.body(text, in.Value)
	if err != nil {
		return nil, err
	}
	fp := fingerprint(body)
	channel, ts := entity.Str(in.Status, "channel"), entity.Str(in.Status, "ts")

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
	k.remember(channel, ts, asked)
	if ts != "" && entity.Str(in.Status, "posted") == fp {
		return nil, nil // in step: the message shows this input
	}
	now := k.now().UTC().Format(time.RFC3339)

	if ts != "" {
		if err := k.s.api.updateMessage(ctx, channel, ts, body); err != nil {
			return nil, err
		}
		return entity.Obj{"updatedAt": now, "posted": fp}, nil
	}

	if channel == "" {
		if channel, err = k.channelFor(ctx, in.To); err != nil {
			return nil, err
		}
	}
	landed, ts, err := k.s.api.postMessage(ctx, channel, "", body)
	if err != nil {
		return nil, err
	}
	st := entity.Obj{"channel": landed, "ts": ts, "postedAt": now, "updatedAt": now, "posted": fp}
	// A permalink is for a reader and not for this program, so failing to get one does not undo
	// a post that happened.
	if link, err := k.s.api.permalink(ctx, landed, ts); err == nil && link != "" {
		st["permalink"] = link
	}
	k.remember(landed, ts, asked)
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
func (k *Sink) body(text string, input *ir.Node) (string, []Asked, error) {
	asks := strs(entity.Get(input, "asks"))
	if len(asks) == 0 {
		return text, nil, nil
	}
	said := []string{text}
	var asked []Asked
	var options []task.Option
	for _, id := range asks {
		q, err := k.question(id)
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
			Standing: q.standing, Line: q.line()})
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

// Asked is the questions the message at (channel, ts) is about, as the last pass read them, each
// saying whether it still takes an answer. Nothing, for a message this program did not post.
func (k *Sink) Asked(channel, ts string) []Asked {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.asked[channel+"/"+ts]
}

// remember indexes one posted message by where it landed, for Asked.
func (k *Sink) remember(channel, ts string, asked []Asked) {
	if channel == "" || ts == "" {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.asked == nil {
		k.asked = map[string][]Asked{}
	}
	if len(asked) == 0 {
		delete(k.asked, channel+"/"+ts)
		return
	}
	k.asked[channel+"/"+ts] = asked
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

// question is what a message says of a question it is about, as its record says it.
type question struct {
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
}

// question reads the record of the question with this id, where the mirror keeps it.
func (k *Sink) question(id string) (question, error) {
	q := question{id: id}
	doc, err := verseio.ReadAt(k.verse, mirror.QuestionRef(id).KPath())
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
	q.say = strs(entity.Get(doc, "say"))
	q.waiting = strs(entity.Get(doc, mirror.WaitingOnField))
	if ir.Uncomment(entity.Get(doc, "options")) != nil {
		q.opts = mirror.OptionsOf(doc)
	}
	ballots := entity.Get(doc, "ballots")
	for _, who := range verseio.Fields(ballots) {
		b := entity.Get(ballots, who)
		cast := person(who) + " " + entity.Str(b, "choice")
		// What somebody said with their ballot is half of what they decided, and a channel that
		// shows the count and drops the words shows the lesser half.
		if said := strings.Join(strs(entity.Get(b, "say")), "; "); said != "" {
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
func (q question) pending() bool {
	return q.standing && q.status == mirror.QuestionPending && q.verdict == ""
}

// line is this program's line about the question, in the message beside its buttons.
func (q question) line() string {
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
		return line
	case q.verdict != "":
		line := id + " answered *" + q.verdict + "*"
		if q.by != "" {
			line += " by " + person(q.by)
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

// strs is a list of strings, or the one string a field holds instead.
func strs(n *ir.Node) []string {
	n = ir.Uncomment(n)
	if n == nil {
		return nil
	}
	if n.Type == ir.StringType {
		return []string{n.String}
	}
	var out []string
	for _, v := range n.Values {
		if v = ir.Uncomment(v); v != nil && v.Type == ir.StringType {
			out = append(out, v.String)
		}
	}
	return out
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
		who, err := k.s.api.lookupByEmail(ctx, email)
		if err != nil {
			return "", err
		}
		return k.s.api.openDM(ctx, who)
	}
	switch {
	case strings.HasPrefix(target, "U"), strings.HasPrefix(target, "W"):
		return k.s.api.openDM(ctx, target)
	case strings.HasPrefix(target, "C"), strings.HasPrefix(target, "G"), strings.HasPrefix(target, "D"):
		return target, nil
	}
	return "", fmt.Errorf("target %q is neither a Slack id (U… a person, C… or G… a channel) "+
		"nor somebody verse knows (user:<email>)", target)
}

// fingerprint is a short, stable digest of a message, so a message that would show differently is
// an update.
func fingerprint(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:8])
}
