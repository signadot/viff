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
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	verseapi "github.com/signadot/verse/api"
	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// The out half: what the verse asks to be said in Slack, said, and what became of it written back.
//
// Somebody (a person, a charter) writes an INPUT at `slack.<ws>.out.<target>.<name>`:
//
//	text: "the review of demo.pr.1 is waiting on you"
//	buttons:                  # optional; a button can only answer a question (see socket.go)
//	- {label: Approve, answer: {question: "review@demo.pr.1~d3", option: proceed}}
//	- {label: Deny, answer: {question: "review@demo.pr.1~d3", option: deny}}
//
// and this answers with its STATUS at the sibling `slack.<ws>.out.<target>.<name>~status`:
//
//	channel: D0EXAMPLE000      # where it landed: the DM for a person, the channel itself otherwise
//	ts: "1790086509.181649"   # the message, which is what an update needs
//	permalink: https://…
//	postedAt / updatedAt
//	posted: 3f2a…             # the fingerprint of the input the message now shows
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
// A PASS IS LEVEL-TRIGGERED, the source's law run backwards: it reads what is asked, compares it
// with what its status says was done, and does the difference. Nothing is remembered between
// passes but what the status holds, so a restart resumes rather than repeats: an input whose status
// says it is posted at this fingerprint is in step, one whose fingerprint moved is an update, and
// one with no ts yet is a post. This program never writes an input, so its own writes are never
// new requests.
//
// Removing an input leaves its status, so the ts survives for whoever later decides what a
// removal should do to the message; for now the message stays as it is.
type Out struct {
	s     *Slack
	verse *client.Client
	now   func() time.Time
}

// StatusSuffix ends the name of every status, and of no input.
const StatusSuffix = "~status"

// Out is this workspace's out half, reading and writing the verse through verse.
func (s *Slack) Out(verse *client.Client) *Out {
	return &Out{s: s, verse: verse, now: time.Now}
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

// read answers the out place as one document, or nil when nothing stands there.
func (o *Out) read() (*ir.Node, error) {
	at := o.s.OutRef().KPath()
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
	// The answer is what stands AT the place: its targets, and under each its inputs and statuses.
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
	blocks, err := blocksFor(text, input)
	if err != nil {
		o.fail(target, name, status, err)
		return
	}
	fp := fingerprint(input)
	channel, ts := entity.Str(status, "channel"), entity.Str(status, "ts")
	if ts != "" && entity.Str(status, "posted") == fp {
		return // in step: the message shows this input
	}
	now := o.now().UTC().Format(time.RFC3339)

	if ts != "" {
		if err := o.s.api.updateMessage(ctx, channel, ts, text, blocks); err != nil {
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
	landed, ts, err := o.s.api.postMessage(ctx, channel, text, blocks)
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
	o.write(target, name, st)
}

// blocksFor lays an input out for Slack: its text and, when it has buttons, the buttons and a line
// of this program's own saying which question they answer. That line is not the writer's to word:
// a label is, and a label is exactly what could misstate what a click does. No buttons is no
// blocks, and the message is its text.
func blocksFor(text string, input *ir.Node) ([]any, error) {
	btns := ir.Uncomment(entity.Get(input, "buttons"))
	if btns == nil {
		return nil, nil
	}
	if btns.Type != ir.ArrayType || len(btns.Values) == 0 {
		return nil, fmt.Errorf("buttons is a list of {label, answer: {question, option}}")
	}
	var elements []any
	var questions []string
	for i, b := range btns.Values {
		label := entity.Str(b, "label")
		ans := entity.Get(b, "answer")
		q, opt := entity.Str(ans, "question"), entity.Str(ans, "option")
		if label == "" || q == "" || opt == "" {
			return nil, fmt.Errorf("button %d needs a label and answer: {question, option}", i+1)
		}
		value, err := answerValue(q, opt)
		if err != nil {
			return nil, err
		}
		elements = append(elements, map[string]any{
			"type": "button", "action_id": fmt.Sprintf("%s%d", AnswerAction, i), "value": value,
			"text": map[string]any{"type": "plain_text", "text": label},
		})
		if !slices.Contains(questions, q) {
			questions = append(questions, q)
		}
	}
	said := make([]string, len(questions))
	for i, q := range questions {
		said[i] = "`" + q + "`"
	}
	return []any{
		map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
		map[string]any{"type": "actions", "elements": elements},
		map[string]any{"type": "context", "elements": []any{
			map[string]any{"type": "mrkdwn", "text": "Answers " + strings.Join(said, ", ")},
		}},
	}, nil
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

// fingerprint is a short, stable digest of an input document, so a changed input is an update.
func fingerprint(input *ir.Node) string {
	b, err := codec.NodeBytes(input, codec.Tony)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
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
