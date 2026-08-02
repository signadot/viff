// Package resolver is the binding that says who answers a question, and the instance that
// says who is watching one run.
//
// A resolver answers gate questions. It is INSTALLED rather than declared in a charter,
// because a binding outlives any one rule and is managed by whoever supervises rather than by
// whoever wrote the work. See docs/working/attached-resolvers.md.
package resolver

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/trigger"
)

// System is where both bindings and instances live: `resolvers:<kind>:<name>` for a binding,
// `resolvers:instance:<name>@<origin>` for one that is live.
//
// One system, so a single `--system resolvers` shows what is installed and who is watching
// what. And a reserved KIND rather than a second system, for the reason process:claim gives:
// the pair would differ by one letter, which is a thing to mistype rather than a thing to
// read.
const System = "resolvers"

// InstanceKind is reserved: a binding may not be installed under it, because an instance's
// address is derived and a binding's is chosen, and one overwriting the other would be a
// resolver silently becoming a record of itself.
const InstanceKind = "instance"

// Ref addresses a binding.
func Ref(kind, name string) entity.Ref {
	return entity.Ref{System: System, Kind: kind, ID: name}
}

// InstanceRef addresses one binding watching one origin — a run, for now.
//
// The `@` composition is the one verse already uses for a thing that exists per pair
// (`process:claim:<chain>@<subject>`, a gate question's `<origin>~d<n>`), and it is what lets
// the routes that serve a resolver name a stream with one ref instead of a ref and a query
// parameter the caller has to keep in step.
//
// The FIRST `@` is the separator and the rest is the origin, because an origin often contains
// one itself — a trigger spawn is `<trigger>@<subject>`, so watching one reads
// `resolvers:instance:security-review@guard@demo:pr:1`. Splitting on the first is the same
// rule a question id follows for `~`, and a name may not contain `@` for that reason.
func InstanceRef(name, origin string) entity.Ref {
	return entity.Ref{System: System, Kind: InstanceKind, ID: name + "@" + origin}
}

// Resolver is a binding: what it supervises, how it answers, and under what envelope.
type Resolver struct {
	// Name and Kind together are its address. Kind is the operator's own category — what this
	// resolver is FOR — the way a provider's capability is (`provider:treat:fixer`), so
	// `verse entities --system resolvers --kind security` is a question worth asking.
	Name string `tony:"field=name"`
	Kind string `tony:"field=kind"`
	// Condition makes it STANDING: nobody names it, and it supervises the work about the
	// entities it selects. Nil means it attaches only by being named.
	//
	// The two doors are not exclusive: one remit can cover the subjects it always watches
	// and still be pulled in where a charter names it.
	Condition *Condition `tony:"field=condition,omitzero"`
	// ByRef says it may be named — by a spawn's `resolver:`, or by a rule's `gating:`. It is
	// one of the two doors, and a resolver with neither open applies to nothing.
	ByRef bool `tony:"field=byRef,omitzero"`
	// Method says how it answers and what the answering may do.
	Method Method `tony:"field=method"`
	// Deadline is what happens if this binding says nothing. Optional; without one a
	// question waits for it indefinitely, which is what an advisory binding usually wants.
	Deadline *Deadline `tony:"field=deadline,omitzero"`
	// Exclusive says its answer is the one that counts, rather than the first answer to
	// arrive. It requires a Deadline: a binding that is down would otherwise turn every
	// question it owns into one nobody may answer.
	Exclusive bool `tony:"field=exclusive,omitzero"`
}

// Deadline is a clock on a question and what happens when it fires.
//
// `within` is the word a provider selection and a motion already use for a duration, and this
// is the same idea — so it is the same word. `then` names the outcome rather than the field
// name baking one in, because the two kinds of outcome are genuinely different: a motion's
// deadline picks a winning OPTION, and this one picks a new ANSWERER.
type Deadline struct {
	Within string `tony:"field=within"`
	Then   string `tony:"field=then,omitzero"`
}

// Escalate is the only outcome this build has. The others a motion already says its own way:
// a default option that wins when the clock fires is "proceed on silence" and "deny on
// silence", for a motion over {proceed, deny}.
const Escalate = "escalate"

// After is the deadline's duration.
func (d *Deadline) After() (time.Duration, error) {
	if d == nil {
		return 0, nil
	}
	return time.ParseDuration(d.Within)
}

// Outcome is what happens when it fires; empty means escalate, which is the only one.
func (d *Deadline) Outcome() string {
	if d == nil || d.Then == "" {
		return Escalate
	}
	return d.Then
}

// Condition selects the entities a standing binding covers, in the vocabulary a trigger's
// own `subject` already uses — so nothing new is parsed and an operator who can write one can
// write the other.
type Condition struct {
	Subject trigger.Match `tony:"field=subject"`
}

// Covers reports whether this condition selects ref right now.
//
// It asks the store rather than the ref alone, because a condition reads values —
// `{area: payments}` is a fact about the entity, not about its address. It inherits Select's
// treatment of an entity on its way out: one marked VANISHING is not selected, so a gate
// about something that is leaving falls to whoever reads the docket rather than to a
// supervisor of a thing that will not be there.
func (c *Condition) Covers(r entity.Reader, ref entity.Ref) bool {
	if c == nil {
		return false
	}
	for _, got := range c.Subject.Select(r) {
		if got == ref {
			return true
		}
	}
	return false
}

// Method is the answering half. Driver chooses who decides; the rest is the envelope of
// whatever that driver runs — its ACTING grant, not the authority of what it answers.
type Method struct {
	// Driver is `url` for now: verse hands over two streams and applies what comes back,
	// running nothing itself. `llm` and `script` are drivers over this same protocol.
	Driver string `tony:"field=driver"`
	// URL is where the service is, so verse can hand it a capability and an operator can see
	// what is bound. It is not an endpoint verse posts questions to — the service dials out
	// for both streams, so one behind NAT needs no special case.
	URL string `tony:"field=url,omitzero"`
	// Vector, Tools and Grant bound whatever the driver runs. Inert for a `url`, which runs
	// nothing verse can gate, and carried anyway so the entity says the same thing about
	// every driver.
	Vector string   `tony:"field=vector,omitzero"`
	Tools  []string `tony:"field=tools,omitzero"`
	Grant  string   `tony:"field=grant,omitzero"`
}

// Check validates a binding for THIS increment, refusing what it cannot yet honour rather
// than storing it.
//
// Refusing is the point. A field accepted and not read is a supervisor that reports installed
// and supervises nothing — which is the failure already in the tree at `launch.Spec.Resolver`,
// decoded and bound and dropped, so a charter can name a resolver on a spawn today and have it
// do nothing at all. Every refusal here says which increment will read the field, so an
// operator learns what to wait for rather than that they were wrong.
func (r Resolver) Check() error {
	if r.Name == "" {
		return fmt.Errorf("resolver: name is required — it is half of the address")
	}
	if r.Kind == "" {
		return fmt.Errorf("resolver %q: kind is required — it is the other half of the address, "+
			"and it is what this resolver is FOR (security, cost), the way a provider's "+
			"capability is", r.Name)
	}
	if strings.Contains(r.Name, "@") {
		return fmt.Errorf("resolver %q: a name may not contain @ — it is the separator in an "+
			"instance's address (resolvers:instance:<name>@<origin>), and an origin often "+
			"contains one of its own", r.Name)
	}
	if r.Kind == InstanceKind {
		return fmt.Errorf("resolver %q: kind %q is reserved for the record of a live one "+
			"(resolvers:%s:<name>@<origin>)", r.Name, InstanceKind, InstanceKind)
	}
	// Two doors, and one of them must be open. A binding that may not be named and covers
	// nothing applies to nothing, which is a supervisor that reports installed and
	// supervises nothing — the failure this whole design is arranged around.
	if !r.ByRef && r.Condition == nil {
		return fmt.Errorf("resolver %q: needs a door — `byRef: true` to be named by a spawn "+
			"or a rule, or a `condition` to cover the subjects it watches. Both is allowed; "+
			"neither applies to nothing", r.Name)
	}
	if r.Condition != nil && !r.Condition.Subject.Selects() {
		return fmt.Errorf("resolver %q: condition.subject must name a system and a kind — it "+
			"selects the entities this binding covers, and one that selects everything is a "+
			"claim on every question in the verse", r.Name)
	}
	if r.Deadline != nil {
		if _, err := r.Deadline.After(); err != nil {
			return fmt.Errorf("resolver %q: deadline.within %q is not a duration (4h, 30m): %w",
				r.Name, r.Deadline.Within, err)
		}
		if got := r.Deadline.Outcome(); got != Escalate {
			return fmt.Errorf("resolver %q: deadline.then %q is not built; %q is, and it is the "+
				"one a motion cannot already say — a default option that wins when the clock "+
				"fires is how a motion says proceed-on-silence", r.Name, got, Escalate)
		}
	}
	// The coupling the design states, enforced where it is cheap: an exclusive binding that
	// is down is a question nobody may answer, and a deadline is what makes that a delay
	// instead of a deadlock.
	if r.Exclusive && r.Deadline == nil {
		return fmt.Errorf("resolver %q: exclusive needs a deadline — its answer is the only "+
			"one that counts, so a binding that is down would turn every question it owns "+
			"into one nobody may answer. `deadline: {within: \"4h\"}` makes that a delay",
			r.Name)
	}
	switch r.Method.Driver {
	case "url":
		if r.Method.URL == "" {
			return fmt.Errorf("resolver %q: method.url is required for driver url — it is "+
				"where the service is, which is what verse hands a capability to", r.Name)
		}
	case "":
		return fmt.Errorf("resolver %q: method.driver is required", r.Name)
	case "llm", "script":
		return fmt.Errorf("resolver %q: driver %q is not built yet; %q is, and the other two "+
			"are drivers over the same protocol", r.Name, r.Method.Driver, "url")
	default:
		return fmt.Errorf("resolver %q: no driver %q (want url)", r.Name, r.Method.Driver)
	}
	return nil
}

// CheckFields reports a field the decoder would drop in silence.
//
// Read as a DOCUMENT before decoding, for the reason a trigger is: gomap ignores what it does
// not recognise, so `resolvre:` or `metod:` installs a resolver that answers nothing and says
// it installed fine. The fields refused here are the ones this increment cannot honour, and
// each says what will read it.
func CheckFields(node *ir.Node) error {
	return checkFields(node, "resolver", map[string]string{
		"name": "", "kind": "", "byRef": "", "method": "", "condition": "",
		"deadline": "", "exclusive": "",
		"escalatesAfter": "spelled `deadline: {within: \"4h\", then: escalate}` — `within` is what " +
			"a selection and a motion already call a duration, and `then` says which outcome " +
			"because a motion's deadline picks an option while this one picks an answerer",
	}, func(field string, val *ir.Node) error {
		if field == "condition" {
			return checkFields(val, "resolver condition", map[string]string{
				"subject": "",
				"when": "a standing binding covers ENTITIES, and its spine is that subject's " +
					"whole story rather than a filtered view of it; there is no firing here " +
					"for a `when` to dispatch",
			}, nil)
		}
		if field == "deadline" {
			return checkFields(val, "resolver deadline", map[string]string{
				"within": "", "then": "",
			}, nil)
		}
		if field == "method" {
			return checkFields(val, "resolver method", map[string]string{
				"driver": "", "url": "", "vector": "", "tools": "", "grant": "",
			}, nil)
		}
		return nil
	})
}

// checkFields refuses a field that is unknown, and one that is known-but-unread with the
// reason. The two are different news: one is a typo, the other is a thing that is coming.
func checkFields(node *ir.Node, what string, fields map[string]string, descend func(string, *ir.Node) error) error {
	node = unwrap(node)
	if node == nil || node.Type != ir.ObjectType {
		return nil // not an object: the decoder's complaint to make, in its own terms
	}
	for i, field := range node.Fields {
		name := field.String
		notYet, known := fields[name]
		if !known {
			want := make([]string, 0, len(fields))
			for k, v := range fields {
				if v == "" {
					want = append(want, k)
				}
			}
			sort.Strings(want)
			return fmt.Errorf("%s has no field %q (want any of %s) — an unknown field decodes "+
				"silently, so this would install as a resolver that does less than it says",
				what, name, strings.Join(want, ", "))
		}
		if notYet != "" {
			return fmt.Errorf("%s: %q is not read yet — %s", what, name, notYet)
		}
		if descend != nil {
			if err := descend(name, unwrap(node.Values[i])); err != nil {
				return err
			}
		}
	}
	return nil
}

// unwrap steps past a comment wrapper, as the charter's own reader does: a resolver file is
// exactly where a person writes comments, and a commented value is a CommentType node holding
// the real one.
func unwrap(n *ir.Node) *ir.Node {
	for n != nil && n.Type == ir.CommentType && len(n.Values) > 0 {
		n = n.Values[0]
	}
	return n
}

// List reads every installed binding, ordered by name.
//
// The order is the point, not a convenience: it is what decides ownership when more than one
// standing binding covers a subject, so it must be total, stable, and the same for everyone
// who asks.
func List(r entity.Reader) []Resolver {
	var out []Resolver
	for _, ent := range r.List(func(ref entity.Ref) bool {
		return ref.System == System && ref.Kind != InstanceKind
	}) {
		var res Resolver
		if codec.FromNode(ent.Payload, &res) != nil {
			continue // a binding this build cannot read is one it cannot honour either
		}
		out = append(out, res)
	}
	slices.SortFunc(out, func(a, b Resolver) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Owner returns the binding that answers questions about subject, and whether there is one.
//
// OVERLAP IS ORDERED, NOT FORBIDDEN. When several standing bindings cover a subject, the
// first by name owns it. That keeps the design's requirement — a question has exactly one
// answerer at its layer — while dropping the one it cannot keep: disjointness of two
// predicates is undecidable in general, so a refusal at install has to be conservative, and a
// conservative refusal tells an operator with `{area: payments}` and `{area: infra}` no for a
// reason that is not true of their case.
//
// What ordering costs is a binding silently shadowed by an earlier name. Two things answer
// that without the check having to be sound: the owner is stamped on every question, so "why
// is mine idle" is a selection rather than an inference, and installing reports what a new
// binding overlaps.
func Owner(r entity.Reader, subject entity.Ref) (Resolver, bool) {
	for _, res := range List(r) {
		if res.Condition.Covers(r, subject) {
			return res, true
		}
	}
	return Resolver{}, false
}

// Overlaps reports the installed bindings that share a (system, kind) with res — what a new
// one may find itself ordered against.
//
// Deliberately cheap and deliberately not sound: it is a NOTICE rather than a refusal, so a
// false positive costs a sentence instead of an install, and there is nothing here that has
// to decide whether two predicates can both hold.
func Overlaps(r entity.Reader, res Resolver) []string {
	if res.Condition == nil {
		return nil
	}
	var out []string
	for _, other := range List(r) {
		if other.Name == res.Name || other.Condition == nil {
			continue
		}
		if other.Condition.Subject.System() == res.Condition.Subject.System() &&
			other.Condition.Subject.Kind() == res.Condition.Subject.Kind() {
			out = append(out, Ref(other.Kind, other.Name).String())
		}
	}
	return out
}
