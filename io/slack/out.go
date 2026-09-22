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
type Out struct {
	s     *Slack
	verse *client.Client
	now   func() time.Time
	kick  chan struct{}

	mu    sync.Mutex
	asked map[string][]Asked // (channel, ts) → the questions that message is asking
}

// StatusSuffix ends the name of every status, and of no input.
const StatusSuffix = "~status"

// Out is this workspace's out half, reading and writing the verse through verse.
func (s *Slack) Out(verse *client.Client) *Out {
	return &Out{s: s, verse: verse, now: time.Now, kick: make(chan struct{}, 1)}
}

// Kick asks for a pass now rather than at the next interval: a press just answered a question,
// and the message should say so while the person who pressed is looking at it.
func (o *Out) Kick() {
	select {
	case o.kick <- struct{}{}:
	default: // one is already asked for
	}
}

// OutRef is the place inputs are written under.
func (s *Slack) OutRef() entity.Ref { return entity.RefAt(s.place("out")...) }

// Run passes every interval until ctx is done. A pass that fails is logged and the next one tries
// again, which is the whole of its retry policy: the next pass reads the same truth.
func (o *Out) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := o.Pass(ctx); err != nil {
			o.s.log("slack out: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-o.kick:
		}
	}
}

// Pass reads every input under the out place and does what its status says is not done yet.
func (o *Out) Pass(ctx context.Context) error {
	place, err := o.read()
	if err != nil {
		return err
	}
	for _, target := range fieldNames(place) {
		under := entity.Get(place, target)
		for _, name := range fieldNames(under) {
			if strings.HasSuffix(name, StatusSuffix) {
				continue // a status, or one nobody asked for any more
			}
			o.deliver(ctx, target, name, entity.Get(under, name), entity.Get(under, name+StatusSuffix))
		}
	}
	return nil
}

// read answers the out place as one document, or nil when nothing stands there. The answer is what
// stands AT the place: its targets, and under each its inputs and statuses.
func (o *Out) read() (*ir.Node, error) { return o.get(o.s.OutRef().KPath()) }

// get answers the document standing at a place, or nil when nothing stands there.
func (o *Out) get(at string) (*ir.Node, error) {
	resp, err := o.verse.Get("/state", url.Values{"at": {at}})
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", at, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	body, err := client.OKBody(resp)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", at, err)
	}
	doc, err := codec.ParseNode(body, client.ResponseFormat(resp))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", at, err)
	}
	return doc, nil
}

// deliver does what one input's status says is not done yet, and writes what happened.
func (o *Out) deliver(ctx context.Context, target, name string, input, status *ir.Node) {
	text := entity.Str(input, "text")
	if text == "" {
		o.fail(target, name, status, fmt.Errorf("an input carries {text}, and this one has none"))
		return
	}
	body, asked, err := o.body(text, input)
	if err != nil {
		o.fail(target, name, status, err)
		return
	}
	fp := fingerprint(body)
	channel, ts := entity.Str(status, "channel"), entity.Str(status, "ts")
	// Every pass, in step or not: what this message is asking is how a reply to it is matched,
	// and the pass that says nothing to Slack is the ordinary one.
	o.remember(channel, ts, asked)
	if ts != "" && entity.Str(status, "posted") == fp {
		return // in step: the message shows this input
	}
	now := o.now().UTC().Format(time.RFC3339)

	if ts != "" {
		if err := o.s.api.updateMessage(ctx, channel, ts, body); err != nil {
			o.fail(target, name, status, err)
			return
		}
		o.write(target, name, entity.Obj{"updatedAt": now, "posted": fp, "error": entity.Removed()})
		return
	}

	if channel == "" {
		var err error
		if channel, err = o.channelFor(ctx, target); err != nil {
			o.fail(target, name, status, err)
			return
		}
	}
	landed, ts, err := o.s.api.postMessage(ctx, channel, "", body)
	if err != nil {
		o.fail(target, name, status, err)
		return
	}
	st := entity.Obj{
		"channel": landed, "ts": ts, "postedAt": now, "updatedAt": now, "posted": fp,
		"error": entity.Removed(),
	}
	// A permalink is for a reader and not for this program, so failing to get one does not undo
	// a post that happened.
	if link, err := o.s.api.permalink(ctx, landed, ts); err == nil && link != "" {
		st["permalink"] = link
	}
	o.remember(landed, ts, asked)
	o.write(target, name, st)
}

// body is the message an input asks for: its text, and beneath it a line of this program's own for
// each question the input is about, and how to answer the ones still pending. It answers the
// questions still open too, which is what the out half remembers of its own messages (Asked).
//
// Those lines are not the writer's to word. They say what each question's record says NOW — who it
// waits on while it is pending, the verdict and who gave it once answered, why it was withdrawn —
// so a question settling redraws a message nobody wrote to, and everyone who can see it is told,
// not only whoever answered. An input that asks about nothing is its text and no more.
func (o *Out) body(text string, input *ir.Node) (string, []Asked, error) {
	asks := strs(entity.Get(input, "asks"))
	if len(asks) == 0 {
		return text, nil, nil
	}
	said := []string{text}
	var asked []Asked
	var options []task.Option
	for _, id := range asks {
		q, err := o.question(id)
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
			Line: q.line()})
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
	// Settled says the question takes no more answers. It stays in the index anyway, because a
	// thread goes on after its decision and what is said there belongs on the question.
	Settled bool
	// Line is what the message says of it, for telling somebody who answers too late what
	// became of it.
	Line string
}

// Asked is the questions the message at (channel, ts) is about, as the last pass read them, each
// saying whether it still takes an answer. Nothing, for a message this program did not post.
func (o *Out) Asked(channel, ts string) []Asked {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.asked[channel+"/"+ts]
}

// remember indexes one posted message by where it landed, for Asked.
func (o *Out) remember(channel, ts string, asked []Asked) {
	if channel == "" || ts == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.asked == nil {
		o.asked = map[string][]Asked{}
	}
	if len(asked) == 0 {
		delete(o.asked, channel+"/"+ts)
		return
	}
	o.asked[channel+"/"+ts] = asked
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
	pose     string // what it asks, in the words the gate wrote
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
func (o *Out) question(id string) (question, error) {
	q := question{id: id}
	doc, err := o.get(mirror.QuestionRef(id).KPath())
	if err != nil {
		return q, fmt.Errorf("reading question %s: %w", id, err)
	}
	if doc == nil {
		return q, nil
	}
	q.standing = true
	q.pose, q.about = entity.Str(doc, "pose"), entity.Str(doc, "about")
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
	for _, who := range fieldNames(ballots) {
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
func (o *Out) channelFor(ctx context.Context, target string) (string, error) {
	switch {
	case strings.HasPrefix(target, "U"), strings.HasPrefix(target, "W"):
		return o.s.api.openDM(ctx, target)
	case strings.HasPrefix(target, "C"), strings.HasPrefix(target, "G"), strings.HasPrefix(target, "D"):
		return target, nil
	}
	return "", fmt.Errorf("target %q is not a Slack id: U… for a person, C… or G… for a channel", target)
}

// fail records a failure on the status, keeping whatever the status already says was done. The
// same failure again is not written again: an input that stays broken would otherwise cost a
// commit a pass, saying nothing new.
func (o *Out) fail(target, name string, status *ir.Node, err error) {
	if entity.Str(status, "error") == err.Error() {
		return
	}
	o.write(target, name, entity.Obj{
		"error": err.Error(), "failedAt": o.now().UTC().Format(time.RFC3339),
	})
}

// write merges fields into the status. A refusal is logged, because it is the one failure the
// status cannot hold: most likely somebody created the status name before this program did.
func (o *Out) write(target, name string, fields entity.Obj) {
	at := entity.RefAt(o.s.place("out", target, name+StatusSuffix)...)
	resp, err := o.verse.Post("/entity", nil, verseapi.PutEntity{At: at.KPath(), Payload: fields.Payload()})
	if err != nil {
		o.s.log("slack out: writing %s: %v", at, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		o.s.log("slack out: %v", client.StatusError(resp, "writing "+at.String()))
	}
}

// fingerprint is a short, stable digest of a message, so a message that would show differently is
// an update.
func fingerprint(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:8])
}

// fieldNames is an object's keys, sorted so a pass says things in a stable order.
func fieldNames(n *ir.Node) []string {
	n = ir.Uncomment(n)
	if n == nil || n.Type != ir.ObjectType {
		return nil
	}
	names := make([]string, 0, len(n.Fields))
	for _, f := range n.Fields {
		names = append(names, f.String)
	}
	sort.Strings(names)
	return names
}
