// Package resolver is the record of an installed resolver: a service that watches asks and
// answers for whoever granted it (9ce3bva0, stage 4).
//
// A resolver answers asks. It is INSTALLED rather than declared in a charter, because a resolver
// outlives any one rule and is managed by whoever supervises rather than by whoever wrote the
// work. Its `match` says which asks it watches; whom it answers for is granted, by a person at
// `verse.for.<person>` and by a place's owner on record at the place (entity.Answer).
package resolver

import (
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/trigger"
)

// System is where resolvers live: `verse.resolvers.<name>`, one name beneath it and nothing else.
const System = "resolvers"

// Ref addresses a resolver: its name, which is the whole of its address.
func Ref(name string) entity.Ref {
	return entity.VerseRef(System, name)
}

// IsResolver reports whether an address is a resolver's.
func IsResolver(ref entity.Ref) bool { return ref.ChildOf(entity.VerseRoot, System) }

// Resolver is a service that watches asks and answers for whoever granted it.
type Resolver struct {
	// Name is its address: `verse.resolvers.<name>`.
	Name string `tony:"field=name"`
	// Match is the asks it watches: what they are about, and their labels. It routes nothing and
	// owns nothing: whom the resolver answers for is granted (entity.Answer), and a match says
	// only which of those asks it serves.
	Match *Match `tony:"field=match,omitzero"`
	// Method says what answers: a service, which dials verse.
	Method Method `tony:"field=method"`
}

// Match is the asks a resolver watches. An absent dimension admits any ask; an empty match admits
// every ask put to whoever granted the resolver.
type Match struct {
	// About is the address the ask is about, as a kinded path: an ask about that node or one
	// beneath it is matched. A path and nothing else: a value changes while an ask stands, and
	// two matches over values could not be told apart at install.
	About trigger.Match `tony:"field=about,omitzero"`
	// Labels are the labels the ask carries, each equal (mirror.LabelsOf).
	Labels map[string]string `tony:"field=labels,omitzero"`
}

// check refuses a match whose overlap with another could not be decided.
func (m *Match) check() error {
	if err := m.About.Validate(); err != nil {
		return fmt.Errorf("match.about: %w", err)
	}
	if m.About.HasPredicate() {
		return fmt.Errorf("match.about is a path, with no value: a value changes while an ask " +
			"stands, and whether two resolvers could serve one ask would not be decidable at install")
	}
	return task.CheckLabels(m.Labels)
}

// Overlaps reports whether one ask could be matched by both: their paths could meet, and no label
// both name has two values.
func (m *Match) Overlaps(o *Match) bool {
	if !m.About.IsEmpty() && !o.About.IsEmpty() && !m.About.Overlaps(o.About) {
		return false
	}
	for k, v := range m.Labels {
		if w, ok := o.Labels[k]; ok && w != v {
			return false
		}
	}
	return true
}

// Admits reports whether an ask about about, carrying labels, is one this match watches. about is
// the zero ref for an ask about nothing, which only a match with no `about` admits.
func (m *Match) Admits(about entity.Ref, labels map[string]string) bool {
	if !m.About.IsEmpty() && (len(about.Segs()) == 0 || !m.About.MatchRef(about)) {
		return false
	}
	for k, v := range m.Labels {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// Method is what answers. Its service acts as the resolver by being whoever installed it
// (api.ResolverHeader), and dials verse; verse never dials it, so nothing here is an address verse
// uses.
type Method struct {
	// Driver is `service`, which runs wherever its installer runs it. `llm` and `script` are
	// drivers over the same protocol, run by verse, and are not built.
	Driver string `tony:"field=driver"`
	// URL is where the service runs, a note for an operator reading the record. Optional, and
	// never dialled or parsed.
	URL string `tony:"field=url,omitzero"`
}

// DriverService is the one driver: a service, which dials verse.
const DriverService = "service"

// Check validates a resolver, refusing what this build cannot honour rather than storing it.
func (r Resolver) Check() error {
	if r.Name == "" {
		return fmt.Errorf("resolver: name is required — it is the address")
	}
	if r.Match == nil {
		return fmt.Errorf("resolver %q: needs a `match` — the asks it watches (`match: {}` for every "+
			"ask put to whoever grants it); whom it answers for is granted, `verse resolver grant`", r.Name)
	}
	if err := r.Match.check(); err != nil {
		return fmt.Errorf("resolver %q: %w", r.Name, err)
	}
	switch r.Method.Driver {
	case DriverService:
	case "url":
		return fmt.Errorf("resolver %q: driver %q is %q — verse never dials a resolver's service, "+
			"which dials verse; method.url may stay, as a note of where it runs", r.Name, r.Method.Driver, DriverService)
	case "":
		return fmt.Errorf("resolver %q: method.driver is required", r.Name)
	case "llm", "script":
		return fmt.Errorf("resolver %q: driver %q is not built yet; %q is, and the other two "+
			"are drivers over the same protocol", r.Name, r.Method.Driver, DriverService)
	case "person", "group":
		return fmt.Errorf("resolver %q: driver %q is retired (9ce3bva0, stage 4, Q4): a person "+
			"answers for themselves, and a resolver that forwards to one raises an onward ask", r.Name, r.Method.Driver)
	default:
		return fmt.Errorf("resolver %q: no driver %q (want %s)", r.Name, r.Method.Driver, DriverService)
	}
	return nil
}

// retired is what each field of the ownership ladder is told (9ce3bva0, stage 4).
const retired = "retired (9ce3bva0, stage 4): "

// CheckFields reports a field the decoder would drop in silence.
//
// Read as a DOCUMENT before decoding, for the reason a trigger is: gomap ignores what it does
// not recognise, so `metod:` installs a resolver that answers nothing and says it installed fine.
// A field of the retired ownership ladder is refused saying what took its place.
func CheckFields(node *ir.Node) error {
	return checkFields(node, "resolver", map[string]string{
		"name": "", "method": "", "match": "",
		"condition": retired + "a resolver does not own the questions about a subject; `match: " +
			"{about: <path>}` says which asks it watches, and a place's owner on record grants it there",
		"byRef": retired + "nothing names a resolver; whoever it answers for grants it, " +
			"`verse resolver grant`",
		"deadline": retired + "a resolver hands nothing up; it escalates by raising an onward ask, " +
			"whose clock fits inside the ask it serves",
		"exclusive": retired + "whose answer counts is the party's grant; a party's resolver " +
			"answers for them, and the party may still answer for themselves",
		"escalatesAfter": retired + "a resolver hands nothing up; it escalates by raising an onward ask",
	}, func(field string, val *ir.Node) error {
		if field == "match" {
			return checkFields(val, "resolver match", map[string]string{
				"about": "", "labels": "",
				"to": "a match does not route (9ce3bva0): whom a resolver answers for is granted, " +
					"`verse resolver grant`",
			}, nil)
		}
		if field == "method" {
			return checkFields(val, "resolver method", map[string]string{
				"driver": "", "url": "",
				"who":    retired + "the person driver is gone; a person answers for themselves",
				"group":  retired + "the group driver is gone; a group's members answer for themselves",
				"vector": retired + "a resolver is a service, and runs nothing verse gates",
				"tools":  retired + "a resolver is a service, and runs nothing verse gates",
				"grant":  retired + "a resolver is a service, and runs nothing verse gates",
			}, nil)
		}
		return nil
	})
}

// checkFields refuses a field that is unknown, and one that is known-but-unread with the
// reason. The two are different news: one is a typo, the other is a thing that is coming.
func checkFields(node *ir.Node, what string, fields map[string]string, descend func(string, *ir.Node) error) error {
	node = ir.Uncomment(node)
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
			if strings.HasPrefix(notYet, retired) {
				return fmt.Errorf("%s: %q is %s", what, name, notYet)
			}
			return fmt.Errorf("%s: %q is not read yet — %s", what, name, notYet)
		}
		if descend != nil {
			if err := descend(name, ir.Uncomment(node.Values[i])); err != nil {
				return err
			}
		}
	}
	return nil
}

// List reads every installed resolver, ordered by name.
//
// The order is the point, not a convenience: it is what decides ownership when more than one
// standing resolver matches a subject, so it must be total, stable, and the same for everyone
// who asks.
func List(r entity.Reader) []Resolver {
	var out []Resolver
	for _, ent := range entity.Below(r, IsResolver, 1, entity.VerseRoot, System) {
		var res Resolver
		if err := codec.FromNode(ent.Payload, &res); err != nil {
			// A resolver this build cannot read is one it cannot honour either — and it SAYS so,
			// once, because the alternative is an answerer that has quietly stopped existing and
			// questions that go unanswered with nothing pointing at why.
			if _, said := unreadable.LoadOrStore(ent.Ref.String(), true); !said {
				slog.Warn("resolver: a resolver this build cannot read answers nothing",
					"resolver", ent.Ref.String(), "err", err)
			}
			continue
		}
		out = append(out, res)
	}
	slices.SortFunc(out, func(a, b Resolver) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// unreadable is the resolvers List has already said it cannot read, so it says it once rather
// than at every question.
var unreadable sync.Map

// Find reads one resolver by its ref, for a caller that has an owner and wants its dials.
//
// FindErr is the same read reporting whether it HAPPENED. Find keeps the two-value shape for the
// callers that genuinely cannot act on the difference, but a caller deciding something on the
// strength of "no such resolver" wants FindErr: an unreadable store answering "not installed"
// makes an exclusive resolver look advisory, which is how a second answerer got admitted and the
// record came out not naming them both (git-issue nkptp9b3h12ksx3kg9n0).
func Find(r entity.Reader, ref string) (Resolver, bool) {
	res, ok, _ := FindErr(r, ref)
	return res, ok
}

func FindErr(r entity.Reader, ref string) (Resolver, bool, error) {
	parsed, err := entity.ParseAddr(ref)
	if err != nil {
		return Resolver{}, false, nil
	}
	e, ok, err := r.Get(parsed)
	if err != nil {
		return Resolver{}, false, err
	}
	if !ok {
		return Resolver{}, false, nil
	}
	var res Resolver
	if codec.FromNode(e.Payload, &res) != nil {
		return Resolver{}, false, nil
	}
	return res, true, nil
}
