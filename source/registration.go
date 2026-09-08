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

// System is where installed sources live: `verse:sources:<system>:<name>`.
//
// The first segment after the system is the SLICE REFLECTED, unlike a runner's kind, which is
// the operator's own category (pool, host, broker). The difference is not cosmetic: a runner's
// kind means nothing to verse, while a source's system is already answering a question verse
// asks — who writes `git:*` — so putting it in the address makes the collision visible in a
// listing rather than only inside the payload.
const System = "sources"

// Ref addresses an installed source.
func Ref(system, name string) entity.Ref {
	return entity.VerseRef(System, system, name)
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

	// Owns is what this registration claims to be the single writer of — an address each, and
	// optionally a path inside it (`{at: "git.*.verse/*"}`; see own.go). It is what an install
	// proves disjointness against, and what a `--forget` removal tombstones. Absent means the
	// source declined to say, which is refused an install alongside anything it cannot be
	// proved apart from.
	Owns []Own `tony:"field=owns,omitzero"`

	// Scope is the copy-on-write scope this source reflects into; empty is baseline. A scoped
	// source's reflections fire that scope's triggers and nobody else's, and the registration
	// lives in the scope it reflects into — so DeleteScope takes it and everything it wrote.
	Scope string `tony:"field=scope,omitzero"`

	// LastReflect and LastError are written by VERSE, not by an operator: when verse last wrote
	// from this source, and the last failure (cleared by the next success). They are declared
	// here so that `verse source show` piped back into `verse source add` is not refused for
	// carrying fields it was just handed, and so a listing can read them without a second
	// decode. Check ignores them.
	LastReflect string   `tony:"field=lastReflect,omitzero"`
	LastError   *Failure `tony:"field=lastError,omitzero"`
}

// Failure is a source's last error together with WHEN IT STARTED, and the pair is the point:
// an error on its own cannot be told from one the source recovered from long ago, which is
// exactly the confusion it caused — a registration carried a context-deadline error for hours
// while the source was demonstrably healthy, and nothing in the record said the error was old.
//
// When is the instant the source ENTERED this failure, not the last time it repeated. A source
// failing the same way every minute keeps the When it started with, so the distance between it
// and lastPass is how long this has been going on, and a `verse source` listing can say "since
// 20:39" rather than "now" about something that has been broken since breakfast. A NEW error —
// different text — is a new failure and takes a new When.
//
// A string rather than a time.Time because every timestamp verse stores is an RFC3339 string
// and nothing in the tree tony-encodes a time.Time; one that did would be the odd one out in
// the very record an operator reads.
//
// When is a POINTER because "unknown" is one of its answers. A registration written before
// this carries lastError as a bare string: the error is known, the moment it began is not, and
// a zero timestamp would say midnight in 1970 rather than "nobody recorded it". FromTonyIR
// reads that shape and leaves When nil.
type Failure struct {
	Error string  `tony:"field=error"`
	When  *string `tony:"field=when,omitzero"`
}

// FromTonyIR decodes both spellings of a failure: the record, and the bare string every
// registration written before this one carries.
//
// It exists because the alternative was a migration. Decoding a string into a record is not a
// mismatch gomap forgives — it fails the whole entity ("expected object, got String") — and
// source.ListErr SKIPS an entity it cannot decode, so every source carrying an error at the
// moment of an upgrade would have dropped out of the listing and not been restored. That is
// precisely the population an error field is for. gomap calls this hook with the raw node
// before it dispatches on type, which is the one place that can see both shapes.
//
// A null is nothing recorded, and is not an error to read.
func (f *Failure) FromTonyIR(node *ir.Node) error {
	if node == nil {
		return nil
	}
	node = ir.Uncomment(node)
	switch node.Type {
	case ir.NullType:
		return nil
	case ir.StringType: // the legacy spelling: what went wrong, but not when
		f.Error, f.When = node.String, nil
		return nil
	case ir.ObjectType:
		str := func(name string) (string, bool) {
			v := ir.Uncomment(ir.ToMap(node)[name])
			if v == nil || v.Type != ir.StringType {
				return "", false
			}
			return v.String, true
		}
		f.Error, _ = str("error")
		if w, ok := str("when"); ok {
			f.When = &w
		}
		return nil
	default:
		return fmt.Errorf("lastError: expected a record or a string, got %s", node.Type)
	}
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
	// The system is the kind segment of `verse:sources:<system>:<name>` and a segment of the route
	// that serves it, so neither punctuation survives. The name is the id, which may carry a
	// colon (ParseRef splits three ways and the rest is the id) but not a slash.
	if strings.ContainsAny(e.System, ":/") {
		return fmt.Errorf("source %q: system %q may not contain : or / — it is a segment of the "+
			"address (verse:sources:<system>:<name>) and of the route that serves it", e.Name, e.System)
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
	if _, err := ParseOwns(e.Owns); err != nil {
		return fmt.Errorf("source %q: %w", e.Name, err)
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
	return ownsAreDocuments(node)
}

// ownsAreDocuments explains the one shape that used to work and no longer does.
//
// `owns` was a list of ref globs and is now a list of claims, because a claim may name a path
// inside a payload and there is no terse spelling for one (own.go). The decoder's own complaint
// is "expected object, got String", which is true and says nothing about what to write instead
// — and the person reading it is upgrading, which is the moment to spend a sentence.
func ownsAreDocuments(node *ir.Node) error {
	for i, field := range node.Fields {
		if field.String != "owns" {
			continue
		}
		list := codec.Unwrap(node.Values[i])
		if list == nil || list.Type != ir.ArrayType {
			return nil // the decoder's complaint to make
		}
		for _, el := range list.Values {
			if codec.Unwrap(el).Type != ir.StringType {
				continue
			}
			return fmt.Errorf("owns carries %q, and a claim is now a document: write "+
				"{at: %q} instead. A claim may name a path inside a payload — "+
				"{at: \"linear.project.*\", path: [owners, ENG]} — and there is no way to spell "+
				"that as one string, so there is one shape rather than two",
				el.String, el.String)
		}
	}
	return nil
}

// Find reads one installed source by the address a CLI argument names, accepting it as it is
// stored (`verse.sources.<system>.<name>`) or as the bare pair below it (`git.laptop`) — an
// operator reading `verse data` and one typing `verse source show` should not have to know two
// spellings of one thing. The middle form is refused; trimAddress says why.
//
// ONE GRAMMAR, TWO DEPTHS. The string is read as the kinded path it is, and its depth says
// which of the two spellings arrived. Trimming a prefix by hand and splitting what is left
// would be a second address grammar living beside the real one.
func Find(r entity.Reader, ref string) (Entity, bool) {
	parts, ok := trimAddress(ref)
	if !ok {
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
	out, _ := ListErr(r)
	return out
}

// ListErr is List, reporting the error THIS read failed with — see entity.ListErr.
func ListErr(r entity.Reader) ([]Entity, error) {
	var out []Entity
	// At the subtree — see runner.List for why a List(pred) is the wrong read here: it is a
	// Match on the root, so listing the sources read the whole verse, sources' own output
	// included.
	ents, err := entity.ListAtErr(r, []string{entity.VerseRoot, System}, entity.Depth+1, IsRegistration)
	if err != nil {
		return nil, err
	}
	for _, ent := range ents {
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
	return out, nil
}

// IsRegistration reports whether an address is an installed source's — the namespace's own
// question, since the reserved root rather than the system is what makes it verse's.
func IsRegistration(ref entity.Ref) bool { return ref.Reserved() && ref.System() == System }

// trimAddress takes the two segments that name one of these, from either spelling an operator
// has in front of them: the whole address as it is listed and stored
// (`verse.sources.<system>.<name>`), or the bare pair typed on a command line (`git.laptop`).
//
// The middle form — the address without its reserved root — is deliberately NOT accepted. It
// was the whole spelling before verse's own half of the tree had a name, and keeping it would
// leave two ways to say one address, which is the mush the reserved root exists to end.
func trimAddress(ref string) ([2]string, bool) {
	segs, err := entity.PathSegs(ref)
	if err != nil {
		return [2]string{}, false
	}
	if len(segs) == entity.Depth+1 && segs[0] == entity.VerseRoot && segs[1] == System {
		segs = segs[2:]
	}
	if len(segs) != 2 || segs[0] == "" || segs[1] == "" {
		return [2]string{}, false
	}
	return [2]string{segs[0], segs[1]}, true
}
