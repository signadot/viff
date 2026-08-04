package source

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// System is where installed sources live: `sources:<system>:<name>`.
//
// The first segment after the system is the SLICE REFLECTED, unlike a runner's kind, which is
// the operator's own category (pool, host, broker). The difference is not cosmetic: a runner's
// kind means nothing to verse, while a source's system is already answering a question verse
// asks — who writes `git:*` — so putting it in the address makes the collision visible in a
// listing rather than only inside the payload.
const System = "sources"

// Ref addresses an installed source.
func Ref(system, name string) entity.Ref {
	return entity.Ref{System: System, Kind: system, ID: name}
}

// Entity is one installed source: an address, somewhere to reach it, and what it claims.
//
// Three ways to reach one, and exactly one of them is set. They differ only in where the
// net.Conn comes from — `Reflect` is the same call down all three:
//
//	url      verse dials the source
//	dial-in  the source dials verse, and verse dials back down that connection
//	dir      the in-process git source, with no network at all
//
// This is deliberately NOT the runner's "a runner with no url is refused". There, absence
// already meant something — `local` — so a url-less entity would have been ambiguous. Here the
// field PRESENT says which it is, and refusing `dir` would delete a working, zero-deployment
// path to buy a symmetry nobody reads.
type Entity struct {
	// Name and System together are the address.
	Name   string `tony:"field=name"`
	System string `tony:"field=system"`

	// Exactly one of these three. See the type doc.
	URL    string `tony:"field=url,omitzero"`
	DialIn string `tony:"field=dial-in,omitzero"`
	Dir    string `tony:"field=dir,omitzero"`

	// Owns is what this registration claims to be the single writer of, as ref globs
	// (`git:*:verse/*` — see own.go). It is what an install proves disjointness against, and
	// what a `--forget` removal tombstones. Absent means the source declined to say, which is
	// refused an install alongside anything it cannot be proved apart from.
	Owns []string `tony:"field=owns,omitzero"`

	// Scope is the copy-on-write scope this source reflects into; empty is baseline. A scoped
	// source's reflections fire that scope's triggers and nobody else's, and the registration
	// lives in the scope it reflects into — so DeleteScope takes it and everything it wrote.
	Scope string `tony:"field=scope,omitzero"`

	// LastReflect and LastError are written by VERSE, not by an operator: when verse last wrote
	// from this source, and the last failure (cleared by the next success). They are declared
	// here so that `verse source show` piped back into `verse source add` is not refused for
	// carrying fields it was just handed, and so a listing can read them without a second
	// decode. Check ignores them.
	LastReflect string `tony:"field=lastReflect,omitzero"`
	LastError   string `tony:"field=lastError,omitzero"`
}

// Check validates a source before it is installed.
//
// A VERB rather than a raw write, for the reason `verse runner add` is one: a write has nowhere
// to put the checks. What they cover is what cannot be discovered later — an address that
// cannot be addressed, a way in that is not one, and a claim that does not parse. Whether the
// far side is a source at all is a separate question, answered by probing, because it is the
// one that can change on its own.
func (e Entity) Check() error {
	if e.Name == "" {
		return fmt.Errorf("source: name is required — it is half the address")
	}
	if e.System == "" {
		return fmt.Errorf("source %q: system is required — it is the other half of the address, "+
			"and it is the SLICE this source reflects and is the single writer of", e.Name)
	}
	// The system is the kind segment of `sources:<system>:<name>` and a segment of the route
	// that serves it, so neither punctuation survives. The name is the id, which may carry a
	// colon (ParseRef splits three ways and the rest is the id) but not a slash.
	if strings.ContainsAny(e.System, ":/") {
		return fmt.Errorf("source %q: system %q may not contain : or / — it is a segment of the "+
			"address (sources:<system>:<name>) and of the route that serves it", e.Name, e.System)
	}
	if strings.Contains(e.Name, "/") {
		return fmt.Errorf("source %q: a name may not contain / — it is a path segment on the "+
			"route that serves it, so this one would address something else", e.Name)
	}
	if err := e.checkReach(); err != nil {
		return err
	}
	// Every claim parses, HERE, where the operator is present. A claim that failed to parse at
	// install would leave a registration whose ownership verse cannot read — so it could be
	// proved disjoint from nothing, and every later install in its system would be refused for
	// a reason belonging to this one.
	for _, own := range e.Owns {
		if _, err := ParseClaim(own); err != nil {
			return fmt.Errorf("source %q: %w", e.Name, err)
		}
	}
	return nil
}

// checkReach enforces exactly one way in, and validates the one that is set.
func (e Entity) checkReach() error {
	var set []string
	for _, f := range []struct{ name, val string }{
		{"url", e.URL}, {"dial-in", e.DialIn}, {"dir", e.Dir},
	} {
		if f.val != "" {
			set = append(set, f.name)
		}
	}
	switch len(set) {
	case 1:
	case 0:
		return fmt.Errorf("source %q: needs exactly one way in — `url` (verse dials it), "+
			"`dial-in` (it dials verse), or `dir` (the in-process git source)", e.Name)
	default:
		return fmt.Errorf("source %q: %s are all set, and they are three different ways in — "+
			"name exactly one", e.Name, strings.Join(set, ", "))
	}
	if e.DialIn != "" {
		// Designed, not built (docs/working/sources-residual.md). Refused rather than accepted,
		// because a registration that installed and then never dialled would sit in a listing
		// looking installed — which is the one state an operator cannot debug from the outside.
		return fmt.Errorf("source %q: `dial-in` is designed but not built yet — a registration "+
			"that installed and never dialled would look installed", e.Name)
	}
	if e.URL == "" {
		return nil // `dir`: a path, checked when the reflector starts rather than here
	}
	u, err := url.Parse(e.URL)
	if err != nil {
		return fmt.Errorf("source %q: url %q does not parse: %w", e.Name, e.URL, err)
	}
	// Parse alone is far too forgiving — it accepts "build-host:9801" as a scheme and an opaque
	// body, which is exactly the typo an operator makes, and it would install a source nothing
	// can dial.
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("source %q: url %q needs an http:// or https:// scheme — it is what "+
			"source.HTTP dials and what a restart reconstructs from", e.Name, e.URL)
	}
	if u.Host == "" {
		return fmt.Errorf("source %q: url %q has no host", e.Name, e.URL)
	}
	return nil
}

// CheckFields reports a field the decoder would drop in silence.
//
// Read as a DOCUMENT before decoding, for the reason a runner is: gomap ignores what it does
// not recognise, so `ulr:` would install a source with no address and report success. It
// matters MORE here than for a runner, which carries one field that could go missing: a dropped
// `owns` installs a source that claims nothing, which is refused an install beside anything —
// and a dropped `scope` silently reflects into baseline, where everybody sees it.
func CheckFields(node *ir.Node) error {
	known := []string{
		"name", "system", "url", "dial-in", "dir", "owns", "scope",
		"lastReflect", "lastError",
	}
	node = codec.Unwrap(node)
	if node == nil || node.Type != ir.ObjectType {
		return nil // not an object: the decoder's complaint to make, in its own terms
	}
	have := map[string]bool{}
	for _, k := range known {
		have[k] = true
	}
	for _, field := range node.Fields {
		if have[field.String] {
			continue
		}
		return fmt.Errorf("source has no field %q (want any of %s) — an unknown field decodes "+
			"silently, so this would install as a source that never carried it",
			field.String, strings.Join(known, ", "))
	}
	return nil
}

// Find reads one installed source by the ref a CLI argument names, accepting it with or without
// the `sources:` prefix — an operator reading `verse entities` and one typing `verse source
// show` should not have to know two spellings of one thing.
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

// List reads every installed source, ordered by address, so two readers see one order.
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
		if out[i].System != out[j].System {
			return out[i].System < out[j].System
		}
		return out[i].Name < out[j].Name
	})
	return out
}
