package io

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	verseapi "github.com/signadot/verse/api"
	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// The OUT half: what the verse asks of an external system, done, and what became of it written
// back. It is the reflection's law run the other way round, and the one thing that differs is
// which side dials.
//
// A reflection is dialled BY verse and answers what is true over there. An out half dials verse:
// it reads what is asked of it under the place it owns, makes the external system match, and
// writes a status beside each request saying what happened. Nothing pushes it, nothing queues for
// it, and nothing is delivered exactly once — a pass reads the truth and does the difference, so
// a crash mid-pass costs a repeat of one comparison rather than a lost message.
//
// Somebody (a person with `verse data`, a charter's `derive`) writes an INPUT anywhere under the
// place, and this answers with its STATUS at the sibling `<name>~status`. An input is a node
// carrying `to`, which says whom or what it is about — a channel, a person, an issue — and the
// connector reads the rest.
//
// TO IS A FIELD, NOT A LEVEL OF THE ADDRESS, and that is what lets anything but a person write
// one. A charter's fan-out addresses what it derives as (rule, subject, key) — seven levels deep
// for one message per person a question waits on — so a contract that read the target off the
// second segment could be written by hand and by nothing else. The address is where a request
// came from; the payload is who it is for.
//
// What an input holds beyond `to`, and what a status holds, are the connector's (Sink.Deliver);
// what is the same for every connector is here:
//
//   - a pass every interval, and one on demand (Kick), over every input under the place;
//   - a name ending in StatusSuffix is never an input, so a status can never ask for anything;
//   - the status is merged, never replaced, so two fields written by two passes both stand;
//   - a failure is written once with the instant it began, and cleared by the next success — a
//     connector that stays broken costs one commit, not one per pass;
//   - a REFUSAL is permanent (Permanent): the status says `failed: permanent`, and the input is not
//     asked again until it changes, since the same request would only be refused again;
//   - and the sink is asked on EVERY pass, including the passes where nothing has changed, which
//     is how it keeps whatever it remembers about its own deliveries current. The one exception is
//     an input refused for good, which a Keeper is told about instead.
//
// This half never writes an input. Its own writes are statuses, so nothing it does can be read
// back by the next pass as a new request — the property that makes the loop terminate.
type Out struct {
	sink  Sink
	place entity.Ref
	verse *client.Client
	log   func(string, ...any)
	now   func() time.Time
	kick  chan struct{}

	// name is the registration this half belongs to, for the pass it reports. Empty reports
	// nothing, which is what an out half run outside a registration does.
	name string
}

// Sink is the out half of a connector: it makes the external system match one input.
//
// One method, called once per input per pass. What "already done" means is the sink's to decide,
// because only it knows what it wrote over there — a Slack message it can update in place, a
// comment that cannot be edited, an email that can only be sent again. It is handed what the last
// pass recorded and answers what to record now, or nothing at all when the world already matches.
type Sink interface {
	// Deliver makes the external system match in, and answers the fields to merge onto its
	// status. Nil fields and no error is "nothing needed doing"; an error is recorded on the
	// status and retried on the next pass, unless it is Permanent.
	Deliver(ctx context.Context, in Input) (entity.Obj, error)
}

// Keeper is a sink that keeps something current about its deliveries on every pass, and so is
// told about an input it is not asked to deliver: one refused for good (Permanent), which stands
// unchanged. Slack's remembers which thread a message's replies land in.
type Keeper interface {
	Keep(in Input)
}

// Permanent marks err as a refusal the same input would get again: the far side said no to what
// was asked (most 4xx), or the sink refuses the input itself (it carries no text, it names a target
// that is no target). The status records `failed: permanent` with a fingerprint of the input, and
// the input is not handed to the sink again until it changes. Editing it is how a requester asks
// again.
//
// NOT EVERY FAILURE. A failure the same request may not meet next time — the far side down, a rate
// limit, the network — is not marked, and is tried again on every pass, as is anything a sink has
// not said is permanent. A request sent again every pass after it was refused is what tripped
// GitHub's secondary rate limit for the account it was made as (git-issue na9fce9dh12krdydqsn0).
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// IsPermanent reports whether err, or anything it wraps, was marked Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

type permanentError struct{ error }

func (p permanentError) Unwrap() error { return p.error }

// PermanentFailure is what `failed` says on the status of an input refused for good.
const PermanentFailure = "permanent"

// fingerprint is a short digest of an input as it was asked, so a refusal can say which asking it
// refused and an edit is a new one.
func fingerprint(v *ir.Node) string {
	b, err := codec.Marshal(v, codec.Tony)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// refused reports whether in was refused for good as it stands.
func refused(in Input) bool {
	if entity.Str(in.Status, "failed") != PermanentFailure {
		return false
	}
	fp := fingerprint(in.Value)
	return fp != "" && entity.Str(in.Status, "failedOn") == fp
}

// Input is one thing asked of an out half, and what the last pass did about it.
type Input struct {
	// Place is where it was asked, for a sink serving more than one — a Linear io reads a place
	// per team.
	Place entity.Ref
	// At is the input's own address, and Name its last segment. Where it stands is the
	// requester's to choose: by hand, one level under the place; by a charter, wherever its
	// derive lands.
	At   entity.Ref
	Name string
	// To is whom or what it is about, off the input's own `to`: a channel, a person verse knows,
	// an issue. What a connector makes of it is the connector's.
	To string
	// Value is what they asked for, and Status what the last pass recorded — nil the first time.
	Value, Status *ir.Node
}

// StatusSuffix ends the name of every status, and of no input.
//
// A SIBLING, NOT A CHILD. An input is the writer's document and grows structure over time; a
// status inside it would one day collide with a field of the same name. `~` is verse's mark for a
// name derived from another (a question id is `<task>~<gate>`), and it needs no quoting in a shell
// or a url.
const StatusSuffix = "~status"

// Reporting names the registration this half belongs to, so every pass tells verse what it did.
// Verse dials a reflection and therefore knows how it is doing; it never dials an out half, so
// without this a listing shows one half of an io and calls it the io.
func (o *Out) Reporting(name string) *Out {
	o.name = name
	return o
}

// NewOut runs sink against place, reading and writing the verse through verse.
func NewOut(sink Sink, place entity.Ref, verse *client.Client, log func(string, ...any)) *Out {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Out{sink: sink, place: place, verse: verse, log: log, now: time.Now,
		kick: make(chan struct{}, 1)}
}

// Kick asks for a pass now rather than at the next interval, for a connector that has just learned
// something — an answer typed in a thread, a webhook — and does not want to wait out the interval
// to say so.
func (o *Out) Kick() {
	select {
	case o.kick <- struct{}{}:
	default: // one is already asked for
	}
}

// Place is where this half reads what is asked of it.
func (o *Out) Place() entity.Ref { return o.place }

// Run passes every interval until ctx is done. A pass that fails is logged and the next one tries
// again, which is the whole of its retry policy: the next pass reads the same truth.
func (o *Out) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := o.Pass(ctx); err != nil {
			o.log("io out: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-o.kick:
		}
	}
}

// Pass finds every input under the place and asks the sink for each, then tells verse what it
// did. The report is last, and it is not part of the pass's success: a verse that would not take
// it is a verse this half has just read and written, so saying so twice adds nothing.
func (o *Out) Pass(ctx context.Context) error {
	place, err := o.Read(o.place.KPath())
	if err != nil {
		o.report(OutPass{Error: err.Error()})
		return err
	}
	var pass OutPass
	o.walk(ctx, place, o.place.Segs(), &pass)
	// AND EVERY OUTBOX SENDING HERE: a place inside a package that the package declared this io
	// reads (`sends:`, git-issue 84a83gkwh12krst5phn0). Read the same way, and each status written
	// beside its input — inside the package, where its rules can read what became of it. Asked
	// each pass, so a package installed or removed changes what is read on the next one. A
	// failure to ask is said and the own place still counts: it was read.
	froms, err := o.outboxes()
	if err != nil {
		o.log("io out: listing outboxes: %v", err)
		pass.Error = err.Error()
	}
	for _, from := range froms {
		box, err := o.Read(from.KPath())
		if err != nil {
			o.log("io out: reading outbox %s: %v", from, err)
			pass.Error = err.Error()
			continue
		}
		o.walk(ctx, box, from.Segs(), &pass)
	}
	o.report(pass)
	return nil
}

// outboxes is the places packages declared this io reads beside its own (GET /outboxes).
func (o *Out) outboxes() ([]entity.Ref, error) {
	resp, err := o.verse.Get("/outboxes", url.Values{"to": {o.place.KPath()}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // no registration names this place: an out half run on its own
	}
	body, err := client.OKBody(resp)
	if err != nil {
		return nil, err
	}
	doc, err := codec.ParseNode(body, client.ResponseFormat(resp))
	if err != nil {
		return nil, err
	}
	var out []entity.Ref
	list := ir.Uncomment(doc)
	if list == nil {
		return nil, nil
	}
	for _, el := range list.Values {
		ref, err := entity.ParseAddr(entity.Str(el, "from"))
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, nil
}

// walk descends the place looking for inputs, which are the nodes carrying `to`.
//
// AT ANY DEPTH, because what writes them decides where they stand: a person types one under the
// place, and a charter's fan-out lands one per person under (rule, subject, key). A node with no
// `to` is a level on the way to one — or somebody's own structure, which this walks past without
// touching.
func (o *Out) walk(ctx context.Context, node *ir.Node, at []string, pass *OutPass) {
	for _, name := range Fields(node) {
		if strings.HasSuffix(name, StatusSuffix) {
			continue // a status, or one nobody asked for any more
		}
		child := entity.Get(node, name)
		segs := append(append([]string{}, at...), name)
		if to := entity.Str(child, "to"); to != "" {
			if o.deliver(ctx, Input{
				Place: o.place, At: entity.RefAt(segs...), Name: name, To: to,
				Value: child, Status: entity.Get(node, name+StatusSuffix),
			}) {
				pass.Delivered++
			} else {
				pass.Failed++
			}
			continue
		}
		o.walk(ctx, child, segs, pass)
	}
}

// report tells verse what a pass did, for a registration that asked to be told.
func (o *Out) report(p OutPass) {
	if o.name == "" {
		return
	}
	resp, err := o.verse.Post("/io/"+url.PathEscape(o.name)+"/pass", nil, p)
	if err != nil {
		o.log("io out: reporting a pass: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		o.log("io out: %v", client.StatusError(resp, "reporting a pass"))
	}
}

// deliver asks the sink about one input and records what it answered. It reports whether the sink
// got its way — an input already matching counts as delivered, since nothing is wrong with it.
func (o *Out) deliver(ctx context.Context, in Input) bool {
	// REFUSED FOR GOOD, AS IT STANDS: asking again would be refused again, so it is not asked.
	// It still counts as failed — it stands unmet — and a Keeper still hears of it.
	if refused(in) {
		if k, ok := o.sink.(Keeper); ok {
			k.Keep(in)
		}
		return false
	}
	// A TARGET IS FIXED AT FIRST DELIVERY, and a changed one is refused here rather than in each
	// connector.
	//
	// Every sink resolves `to` only when it has nothing yet — a Slack channel is in the status,
	// a Linear comment's id is, and both are what an update is addressed by — so a `to` edited
	// afterwards was read by nobody and the input went on quietly editing the message it first
	// made. Retargeting neither moved it nor failed, which is the one outcome nothing can be
	// written against. Refusing says so on the status and costs no call to the far side.
	//
	// It is asked of what the status SAYS was delivered, not of what the sink would resolve now:
	// resolving costs an API call per input per pass, which is exactly what the status exists to
	// avoid. A status written before this carries no `to` and is left alone — the next delivery
	// records one.
	if was := entity.Str(in.Status, "to"); was != "" && was != in.To {
		o.fail(in, Permanent(fmt.Errorf("this was delivered to %s and now asks for %s: a target is "+
			"fixed at first delivery, because what was said is addressed by what the status "+
			"already holds. Ask under a new name, or put %s back", was, in.To, was)))
		return false
	}
	fields, err := o.sink.Deliver(ctx, in)
	if err != nil {
		o.fail(in, err)
		return false
	}
	if len(fields) == 0 {
		return true // already matching, and the sink said so without writing anything
	}
	// The error goes with the success that ends it, in the same write: a status carrying both a
	// fresh `postedAt` and last week's `error` is one no reader can interpret.
	if _, said := fields["error"]; !said {
		fields["error"] = entity.Removed()
		fields["failedAt"] = entity.Removed()
		fields["failed"] = entity.Removed()
		fields["failedOn"] = entity.Removed()
	}
	// WHO IT WENT TO, as the input said it. The connector's own fields say where it landed on
	// the far side — a channel id, a comment uuid — which is not the address a requester wrote
	// and cannot be compared with one. This is what makes the check above possible, and it
	// answers "who was this for" for a reader of the status.
	if _, said := fields["to"]; !said {
		fields["to"] = in.To
	}
	o.Write(in, fields)
	return true
}

// fail records a failure on the status, keeping whatever the status already says was done. The
// same failure again is not written again: an input that stays broken would otherwise cost a
// commit a pass, saying nothing new, and the instant it began would slide forward while it did.
//
// A PERMANENT ONE SAYS WHICH ASKING IT REFUSED, and that is part of "the same": an input edited and
// refused again with the same words is a new refusal, and one recorded against the old fingerprint
// would be asked again on every pass.
func (o *Out) fail(in Input, err error) {
	fields := entity.Obj{"error": err.Error(), "failedAt": o.now().UTC().Format(time.RFC3339)}
	on := ""
	if IsPermanent(err) {
		on = fingerprint(in.Value)
		fields["failed"], fields["failedOn"] = PermanentFailure, on
	} else {
		fields["failed"], fields["failedOn"] = entity.Removed(), entity.Removed()
	}
	if entity.Str(in.Status, "error") == err.Error() && entity.Str(in.Status, "failedOn") == on {
		return
	}
	o.Write(in, fields)
}

// Write merges fields into one input's status, which stands beside the input at whatever depth it
// was written. A refusal is logged, because it is the one failure a status cannot hold: most
// likely somebody created the status name before this program did (a grant says who may create
// beneath a node, never which name is whose).
func (o *Out) Write(in Input, fields entity.Obj) {
	segs := in.At.Segs()
	at := entity.RefAt(append(append([]string{}, segs[:len(segs)-1]...), in.Name+StatusSuffix)...)
	resp, err := o.verse.Post("/entity", nil, verseapi.PutEntity{At: at.KPath(), Payload: fields.Payload()})
	if err != nil {
		o.log("io out: writing %s: %v", at, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		o.log("io out: %v", client.StatusError(resp, "writing "+at.String()))
	}
}

// Read answers the document standing at a place, or nil when nothing stands there.
func (o *Out) Read(at string) (*ir.Node, error) { return ReadAt(o.verse, at) }

// ReadAt is that read without an Out to hang it off: what a sink uses when what it is asked for is
// not all it needs to know, and a gate's record is the case in hand.
func ReadAt(verse *client.Client, at string) (*ir.Node, error) {
	resp, err := verse.Get("/state", url.Values{"at": {at}})
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

// Fields is an object's keys, sorted so a pass does things in a stable order — and so a sink
// reading its own structure says things in one.
func Fields(n *ir.Node) []string {
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
