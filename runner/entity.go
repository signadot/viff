package runner

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// System is where installed runners live: `runners:<kind>:<name>`.
//
// A runner is an ENTITY rather than a startup flag, because the ask this answers is
// "deploying arbitrary runners in the verse namespace" — adding one is a deployment act, a
// write, not an edit to how verse itself is started. A drive adds a runner the way it adds a
// doer or a resolver. See docs/working/per-process-runners.md.
const System = "runners"

// Ref addresses an installed runner.
func Ref(kind, name string) entity.Ref {
	return entity.Ref{System: System, Kind: kind, ID: name}
}

// Entity is one installed runner: an address and somewhere to reach it.
//
// It carries a url and nothing else. A grant ceiling, a tool allowlist, a label — anything
// saying "work here may only do this" — is expressible and deliberately absent: it is the same
// narrowing a doer answers, and answering it in two places without deciding how they compose
// is how a ceiling comes to mean two things.
type Entity struct {
	// Name and Kind together are the address. Kind is the operator's own category — what this
	// runner IS (pool, host, broker) — the way a resolver's kind is what it is FOR.
	Name string `tony:"field=name"`
	Kind string `tony:"field=kind"`

	// URL is the runner's verse-facing base: what runner.HTTP drives, and what a restart
	// reconstructs from. Required, with no default and no local special case — `local` is what
	// the ABSENCE of a runner means, which keeps every runner entity somewhere to reach.
	URL string `tony:"field=url"`
}

// Check validates a runner before it is installed.
//
// The checks are real from the first commit, which is why this is a verb rather than a raw
// write: a write has nowhere to put them. What they cover is what cannot be discovered later
// — an address that cannot be addressed, and a url that is not one. Whether the far side is a
// runner at all is a separate question, answered by probing (Probe), because it is the only
// one of the three that can change on its own.
func (e Entity) Check() error {
	if e.Name == "" {
		return fmt.Errorf("runner: name is required — it is half of the address")
	}
	if e.Kind == "" {
		return fmt.Errorf("runner %q: kind is required — it is the other half of the address, "+
			"and it is what this runner IS (pool, host), the way a resolver's kind is what it "+
			"is FOR", e.Name)
	}
	// The id may contain colons — ParseRef splits three ways and the rest is the id — but the
	// kind may not, and neither may survive a slash: both are path segments on the route that
	// serves them, so a name with a slash addresses something else or nothing.
	if strings.ContainsAny(e.Kind, ":/") {
		return fmt.Errorf("runner %q: kind %q may not contain : or / — it is a segment of the "+
			"address (runners:<kind>:<name>) and of the route that serves it", e.Name, e.Kind)
	}
	if strings.Contains(e.Name, "/") {
		return fmt.Errorf("runner %q: a name may not contain / — it is a path segment on the "+
			"route that serves it, so this one would address something else", e.Name)
	}
	if e.URL == "" {
		return fmt.Errorf("runner %q: url is required — there is no default and no local "+
			"special case here; `local` is what the ABSENCE of a runner on a spawn means, so "+
			"every installed runner is somewhere to reach", e.Name)
	}
	u, err := url.Parse(e.URL)
	if err != nil {
		return fmt.Errorf("runner %q: url %q does not parse: %w", e.Name, e.URL, err)
	}
	// Parse alone is far too forgiving — it accepts "gpu-pool:9797" as a scheme and an opaque
	// body, which is exactly the typo an operator makes, and it would install as a runner
	// nothing can dial.
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("runner %q: url %q needs an http:// or https:// scheme — it is what "+
			"runner.HTTP dials and what a restart reconstructs from", e.Name, e.URL)
	}
	if u.Host == "" {
		return fmt.Errorf("runner %q: url %q has no host", e.Name, e.URL)
	}
	return nil
}

// CheckFields reports a field the decoder would drop in silence.
//
// Read as a DOCUMENT before decoding, for the reason a resolver is: gomap ignores what it does
// not recognise, so `ulr:` would install a runner with no address and report success — and the
// url is the entire content of this entity, so the silence is total.
func CheckFields(node *ir.Node) error {
	known := map[string]string{"name": "", "kind": "", "url": ""}
	node = unwrapNode(node)
	if node == nil || node.Type != ir.ObjectType {
		return nil // not an object: the decoder's complaint to make, in its own terms
	}
	for _, field := range node.Fields {
		name := field.String
		if _, ok := known[name]; ok {
			continue
		}
		want := make([]string, 0, len(known))
		for k := range known {
			want = append(want, k)
		}
		sort.Strings(want)
		return fmt.Errorf("runner has no field %q (want any of %s) — an unknown field decodes "+
			"silently, and url is the whole of what this entity carries", name, strings.Join(want, ", "))
	}
	return nil
}

func unwrapNode(n *ir.Node) *ir.Node {
	for n != nil && n.Type == ir.CommentType && len(n.Values) > 0 {
		n = n.Values[0]
	}
	return n
}

// Find reads one installed runner by the ref a charter or a CLI argument names, accepting it
// with or without the `runners:` prefix — an operator reading `verse entities` and one typing
// `verse runner show` should not have to know two spellings of one thing.
func Find(r entity.Reader, ref string) (Entity, bool) {
	parts := strings.SplitN(strings.TrimPrefix(ref, System+":"), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Entity{}, false
	}
	e, ok, err := r.Get(Ref(parts[0], parts[1]))
	if err != nil || !ok {
		return Entity{}, false
	}
	var out Entity
	if codec.FromNode(e.Payload, &out) != nil {
		return Entity{}, false
	}
	return out, true
}

// List reads every installed runner, ordered by address, so two readers see one order.
func List(r entity.Reader) []Entity {
	var out []Entity
	for _, ent := range r.List(func(ref entity.Ref) bool { return ref.System == System }) {
		var e Entity
		if codec.FromNode(ent.Payload, &e) != nil {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}
