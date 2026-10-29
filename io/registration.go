package io

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// System is where installed sources live: `verse.iods.<name>`. A source's name is the whole
// of its address. WHAT it reflects is on the registration, not in it: `root`, the root segment
// of the tree the source writes (`git`, `github`), and `owns`, the places it claims there.
const System = "iods"

// Ref addresses an installed source.
func Ref(name string) entity.Ref {
	return entity.VerseRef(System, name)
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
//
// It is what `POST /iods` takes and `GET /iods/{name}` answers.
//
//tony:schemagen=verse-installed-iod,notag
type Entity struct {
	// Name is the address: `verse.iods.<name>`.
	Name string `tony:"field=name"`
	// Root is the root segment of the tree this source writes — `git`, `github`, `linear` —
	// which is also which connector it is: an install checks it against what the source says
	// of itself, so a `github` registration pointed at a git source is refused while somebody
	// is present to hear it.
	Root string `tony:"field=root"`

	// Exactly one of these three. See the type doc.
	URL    string `tony:"field=url,omitzero"`
	DialIn string `tony:"field=dial-in,omitzero"`
	Dir    string `tony:"field=dir,omitzero"`

	// Owns is what this registration claims to be the single writer of — an address each
	// (`{at: "git.app"}`; see own.go). It is what an install
	// proves disjointness against, and what a `--forget` removal tombstones. Absent means the
	// source declined to say, which is refused an install alongside anything it cannot be
	// proved apart from.
	Owns []Own `tony:"field=owns,omitzero"`

	// Scope is the copy-on-write scope this source reflects into; empty is baseline. A scoped
	// source's reflections fire that scope's triggers and nobody else's, and the registration
	// lives in the scope it reflects into — so DeleteScope takes it and everything it wrote.
	Scope string `tony:"field=scope,omitzero"`

	// Out is the OUT half: the places it reads what is asked of it, who it acts as while doing
	// it, and who may ask. Absent for an io that only reflects, which is most of them today.
	Out *OutHalf `tony:"field=out,omitzero"`

	// LastReflect and LastError are written by VERSE, not by an operator: when verse last wrote
	// from this source, and the last failure (cleared by the next success). They are declared
	// here so that `verse iod show` piped back into `verse iod add` is not refused for
	// carrying fields it was just handed, and so a listing can read them without a second
	// decode. Check ignores them.
	LastReflect string   `tony:"field=lastReflect,omitzero"`
	LastError   *Failure `tony:"field=lastError,omitzero"`
}

// OutHalf is what a registration says about acting back on the external system.
//
// It exists so that an install is ONE act. Before it, standing up an out half meant a flag naming
// a principal, a place hardcoded in the connector, and four `verse perms` lines an operator had to
// know and get right — and a verse that knew none of it, so `verse iod` could show a healthy io
// whose out half had been refused for an hour.
//
//tony:schemagen=verse-iod-out,notag
type OutHalf struct {
	// Places are where it reads what is asked of it, as kinded paths. Each must lie inside this
	// registration's claim (an io may only be asked for things in its own slice) and outside the
	// covers its reflection carries, or the next pass would tombstone the requests — which the
	// connector arranges by putting them somewhere it does not reflect.
	//
	// A LIST, because one io may serve several: a Linear io reads a place per team, since a
	// claim per team is what keeps two installs from writing each other's.
	Places []string `tony:"field=places"`
	// As is the principal the out half reads and writes as — `agent:slack-iod`. It is what the
	// grants below are written against, and what owns every status the io writes.
	As string `tony:"field=as"`
	// Requesters are the grantees who may ask: they get `read-write` BELOW each place, which is
	// what lets them write inputs and not the statuses. Empty asks for nobody, and an io nobody
	// may ask is an io that will never do anything — allowed, since a place whose grants are
	// managed by hand is a legitimate arrangement, and saying so here is the way to say it.
	Requesters []string `tony:"field=requesters,omitzero"`
	// LastError is written by verse from what the io reports, not by an operator: the last
	// failure that stopped a pass, and when it began. Verse never dials an out half, so the only
	// way it can know is to be told (POST /iods/<name>/pass). When a pass last finished and what
	// it did are the report's too, and stand in scratch at this record's `out`, not on the
	// record: they are about now, and a `verse iod` listing reads them from there.
	LastError *Failure `tony:"field=lastError,omitzero"`
}

// OutPass is what an out half says about a pass it has just finished.
//
// COUNTS OF THE LAST PASS, not totals: a total would be a number nobody can act on, while "the
// last pass delivered nothing and failed twice" is the sentence an operator wants when they ask
// why a message has not arrived. What the io is doing right now is the pass's own business; this
// is the part verse cannot see for itself. It is what `POST /iods/{name}/pass` takes.
//
//tony:schemagen=verse-iod-pass,notag
type OutPass struct {
	Delivered int    `tony:"field=delivered,omitzero"`
	Failed    int    `tony:"field=failed,omitzero"`
	Error     string `tony:"field=error,omitzero"`
}

// Failure is a source's last error together with WHEN IT STARTED, and the pair is the point:
// an error on its own cannot be told from one the source recovered from long ago, which is
// exactly the confusion it caused — a registration carried a context-deadline error for hours
// while the source was demonstrably healthy, and nothing in the record said the error was old.
//
// When is the instant the source ENTERED this failure, not the last time it repeated. A source
// failing the same way every minute keeps the When it started with, so the distance between it
// and lastPass is how long this has been going on, and a `verse iod` listing can say "since
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
//
//tony:schemagen=verse-iod-failure,notag,codec=custom
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
func (f *Failure) FromTonyIR(node *ir.Node, _ ...gomap.UnmapOption) error {
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

// ToTonyIR writes a failure as the record, which is the only spelling written now, and an empty
// one as null. The generated decoder allocates a Failure before it reads one, so a stored
// `lastError: null` arrives as an empty Failure, and writing it back as null keeps "nothing
// recorded" as it was.
func (f *Failure) ToTonyIR(_ ...gomap.MapOption) (*ir.Node, error) {
	if f == nil || (f.Error == "" && f.When == nil) {
		return ir.Null(), nil
	}
	m := map[string]*ir.Node{"error": ir.FromString(f.Error)}
	if f.When != nil {
		m["when"] = ir.FromString(*f.When)
	}
	return ir.FromMap(m), nil
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
		return fmt.Errorf("source: name is required — it is the address")
	}
	if e.Root == "" {
		return fmt.Errorf("source %q: root is required — it is the root of the tree this source "+
			"writes and is the single writer of (git, github, linear), and which connector it "+
			"is", e.Name)
	}
	// A root is one segment of an address, so neither punctuation survives.
	if strings.ContainsAny(e.Root, ":/.") {
		return fmt.Errorf("source %q: root %q may not contain : / or . — it is one segment of "+
			"an address", e.Name, e.Root)
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
	// proved disjoint from nothing, and every later install in its root would be refused for
	// a reason belonging to this one.
	if _, err := ParseOwns(e.Owns); err != nil {
		return fmt.Errorf("source %q: %w", e.Name, err)
	}
	return e.Out.check(e.Name)
}

// check refuses an out half that could not work on its own terms: no place to read, nobody to
// act as, or a principal the grammar does not admit. Whether its places are inside the io's
// claim is CheckClaimed, asked separately because the claim is not always in the request.
func (o *OutHalf) check(name string) error {
	if o == nil {
		return nil
	}
	if len(o.Places) == 0 {
		return fmt.Errorf("io %q: out.places is required — an out half with nowhere to read is "+
			"one nobody can ask for anything", name)
	}
	if o.As == "" {
		return fmt.Errorf("io %q: out.as is required — every status it writes is owned by "+
			"somebody, and an io that says who is one whose statuses nobody else can forge", name)
	}
	if _, err := entity.ParsePrincipal(o.As); err != nil {
		return fmt.Errorf("io %q: out.as %w", name, err)
	}
	for _, who := range o.Requesters {
		if _, err := entity.ParseGrantee(who); err != nil {
			return fmt.Errorf("io %q: out.requesters %w", name, err)
		}
	}
	return nil
}

// CheckClaimed refuses an out half whose places fall outside what the io claims, and is asked
// with the claim the install SETTLED ON rather than the one the request carried.
//
// The difference is the whole reason it is its own call. An operator types `verse iod add slack
// <url> --root slack --out slack.Signadot.out` and names no claim at all: the claim comes from
// probing the io, which happens after the request is read. Asked of the request, this check saw
// an empty claim and refused every out half an operator ever typed — the deployment path, since
// repopulate.sh types exactly that.
//
// INSIDE THE CLAIM is load-bearing. An io asked for things outside its own slice would write
// statuses where another writer owns the tree, and the grants this install seeds would be seeded
// there too — so the check is the same disjointness the in half has always had, applied to the
// other direction.
func (o *OutHalf) CheckClaimed(name string, owns []Own) error {
	if o == nil {
		return nil
	}
	claims, err := ParseOwns(owns)
	if err != nil {
		return fmt.Errorf("io %q: %w", name, err)
	}
	for _, at := range o.Places {
		ref, err := entity.ParseAddr(at)
		if err != nil {
			return fmt.Errorf("io %q: out.places %q: %w", name, at, err)
		}
		if len(Claimed(claims, ref)) == 0 {
			return fmt.Errorf("io %q: out.places %q is outside what this io claims (%s) — an io "+
				"is asked for things in its own slice, and the grants an install seeds are "+
				"seeded there", name, at, ownsAt(owns))
		}
	}
	return nil
}

// ownsAt is the claims as an operator wrote them, for a refusal that shows what was allowed.
func ownsAt(owns []Own) string {
	at := make([]string, 0, len(owns))
	for _, o := range owns {
		at = append(at, o.At)
	}
	return strings.Join(at, ", ")
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
		// Designed, not built (git-issue 1yke34rr). Refused rather than accepted,
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
			"io.HTTP dials and what a restart reconstructs from", e.Name, e.URL)
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
		"name", "root", "url", "dial-in", "dir", "owns", "out", "scope",
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

// Find reads one installed source by what a CLI argument names: its address as it is stored and
// listed (`verse.iods.laptop`), or its name (`laptop`) — an operator reading `verse data` and
// one typing `verse iod show` should not have to know two spellings of one thing.
//
// FindErr is the same read reporting whether it HAPPENED — see versed's findRunnerErr.
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

// List reads every installed source, ordered by address, so two readers see one order.
func List(r entity.Reader) []Entity {
	out, _ := ListErr(r)
	return out
}

// ListErr is List, reporting the error THIS read failed with — see entity.ListErr.
func ListErr(r entity.Reader) ([]Entity, error) {
	var out []Entity
	// At the subtree — see versed's installedRunners for why a List(pred) is the wrong read here: it is a
	// Match on the root, so listing the sources read the whole verse, sources' own output
	// included.
	registry := []string{entity.VerseRoot, System}
	ents, err := entity.ListAtErr(r, registry, len(registry)+1, IsRegistration)
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
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// IsRegistration reports whether an address is an installed source's — the namespace's own
// question, since the reserved root rather than the registry's name is what makes it verse's.
func IsRegistration(ref entity.Ref) bool { return ref.Under(entity.VerseRoot, System) }

// NameOf is the source's name from either spelling: the whole address as it is listed and
// stored, or the name alone. Anything else names no source.
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
