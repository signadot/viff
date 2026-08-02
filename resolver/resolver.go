// Package resolver is the binding that says who answers a question, and the instance that
// says who is watching one run.
//
// A resolver answers gate questions. It is INSTALLED rather than declared in a charter,
// because a binding outlives any one rule and is managed by whoever supervises rather than by
// whoever wrote the work. See docs/working/attached-resolvers.md.
package resolver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
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
	// ByRef says it may be named — by a spawn's `resolver:`, or by a rule's `gating:`. It is
	// one of the two doors, and a resolver with neither open applies to nothing.
	ByRef bool `tony:"field=byRef,omitzero"`
	// Method says how it answers and what the answering may do.
	Method Method `tony:"field=method"`
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
	if !r.ByRef {
		return fmt.Errorf("resolver %q: byRef must be true — it is the only door open in this "+
			"increment, and a resolver with none applies to nothing. A standing `condition` "+
			"is the other, and arrives with the layer that reads it", r.Name)
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
		"name": "", "kind": "", "byRef": "", "method": "",
		"condition": "a standing binding arrives with the layer that reads it; " +
			"this increment attaches by name only (byRef)",
		"escalatesAfter": "escalation arrives with the second layer — with one layer there is " +
			"nothing above but the human docket, which silence already reaches",
		"exclusive": "exclusivity requires a deadline, so it arrives with escalatesAfter; " +
			"a binding is advisory until then and the first answer wins",
	}, func(field string, val *ir.Node) error {
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
