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

// System is where installed runners live: `verse.runners.<name>`.
//
// A runner is an ENTITY rather than a startup flag, because the ask this answers is
// "deploying arbitrary runners in the verse namespace" — adding one is a deployment act, a
// write, not an edit to how verse itself is started. A drive adds a runner the way it adds a
// doer or a resolver.
const System = "runners"

// Ref addresses an installed runner: one name beneath the registry, which is the whole of its
// address.
func Ref(name string) entity.Ref {
	return entity.VerseRef(System, name)
}

// Entity is one installed runner: an address and somewhere to reach it.
//
// It carries a url and who the runner acts as, and nothing about what work may do there. A
// grant ceiling, a tool allowlist, a label — anything saying "work here may only do this" — is
// expressible and deliberately absent: it is the same
// narrowing a doer answers, and answering it in two places without deciding how they compose
// is how a ceiling comes to mean two things.
type Entity struct {
	// Name is the address: `verse.runners.<name>`.
	Name string `tony:"field=name"`

	// URL is the runner's verse-facing base: what runner.HTTP drives, and what a restart
	// reconstructs from. Required, with no default and no local special case — `local` is what
	// the ABSENCE of a runner means, which keeps every runner entity somewhere to reach.
	URL string `tony:"field=url"`
	// Identity is the id of the principal the runner acts as, `runner:<identity>`: what a task's
	// reports are written as, and what the spawn grants `read-write` and `control` on the task. A runner acts as itself,
	// not as whoever installed it or whoever's work it runs. Defaults to the runner's name.
	Identity string `tony:"field=identity,omitzero"`
}

// Principal is who the runner acts as: `runner:` its identity, or its name.
func (e Entity) Principal() string {
	if e.Identity != "" {
		return entity.RunnerKind + ":" + e.Identity
	}
	return entity.RunnerKind + ":" + e.Name
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
		return fmt.Errorf("runner: name is required — it is the address")
	}
	if strings.Contains(e.Name, "/") {
		return fmt.Errorf("runner %q: a name may not contain / — it is a path segment on the "+
			"route that serves it, so this one would address something else", e.Name)
	}
	// The identity is the id alone: the runner acts as `runner:<identity>`, so an identity that
	// spells a kind of its own would act as `runner:user:…`, which is nobody anybody granted.
	if _, err := entity.ParsePrincipal(e.Principal()); err != nil || (e.Identity != "" && entity.KindOf(e.Identity) != "") {
		return fmt.Errorf("runner %q: identity %q is the id of the principal it acts as, "+
			"runner:<identity>, and carries no kind or space of its own", e.Name, e.Identity)
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
	known := map[string]string{"name": "", "url": "", "identity": ""}
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

// Find reads one installed runner by what a charter or a CLI argument names: its address as it
// is stored and listed (`verse.runners.gpu`), or its name (`gpu`) — an operator reading `verse
// data` and one typing `verse runner show` should not have to know two spellings of one thing.
//
// FindErr is the same read reporting whether it HAPPENED, and it is the one a caller deciding
// something uses: a read that failed, or one the reader may not make, is not a runner that is
// not installed, and a spawn told "not installed" is work that does not run.
func Find(r entity.Reader, ref string) (Entity, bool) {
	e, ok, _ := FindErr(r, ref)
	return e, ok
}

func FindErr(r entity.Reader, ref string) (Entity, bool, error) {
	name, ok := NameOf(ref)
	if !ok {
		return Entity{}, false, nil
	}
	e, ok, err := r.Get(Ref(name))
	if err != nil || !ok {
		return Entity{}, false, err
	}
	var out Entity
	if codec.FromNode(e.Payload, &out) != nil {
		return Entity{}, false, nil
	}
	return out, true, nil
}

// List reads every installed runner, ordered by address, so two readers see one order.
func List(r entity.Reader) []Entity {
	out, _ := ListErr(r)
	return out
}

// ListErr is List, reporting the error THIS read failed with — see entity.ListErr for why a
// listing cannot say so in its signature, and why asking the store afterwards is a different
// question from asking the read.
func ListErr(r entity.Reader) ([]Entity, error) {
	var out []Entity
	// AT THE SUBTREE, not over the document. A List(pred) is a Match on the ROOT — the whole
	// verse across the wire and filtered here — so a listing of a handful of runners cost a
	// read of everything every source had ever reflected. On a store carrying two mirrored
	// repositories that was 455 KB a call, and `verse status` (which lists runners, sources
	// and questions) paid it three times per readiness probe.
	registry := []string{entity.VerseRoot, System}
	ents, err := entity.ListAtErr(r, registry, len(registry)+1, Is)
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
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Is reports whether an address is an installed runner's — the namespace's own question, since
// the reserved root rather than the system is what makes it verse's (see mirror.IsTask).
func Is(ref entity.Ref) bool { return ref.Under(entity.VerseRoot, System) }

// NameOf is the runner's name from either spelling an operator has in front of them: the whole
// address as it is listed and stored (`verse.runners.gpu`), or the name alone. Anything else —
// the address without its reserved root, a path with more to it — names no runner.
func NameOf(ref string) (string, bool) {
	segs, err := entity.PathSegs(ref)
	if err != nil {
		return "", false
	}
	if at := entity.RefAt(segs...); at.ChildOf(entity.VerseRoot, System) {
		return at.Name(), at.Name() != ""
	}
	if len(segs) != 1 || segs[0] == "" {
		return "", false
	}
	return segs[0], true
}
