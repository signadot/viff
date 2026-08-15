// Package schema is verse's self-description: ONE tony schema, in one context, defining
// everything verse puts on the wire — the top-level paths of the API and every shape they
// name.
//
// It is assembled here rather than served from somewhere. The parts are:
//
//   - GENERATED — the per-package documents tony-codegen writes from the Go structs
//     (api.Schema, entity.Schema, …). A package owns the schema of the types it declares, and
//     because they are derived they cannot drift from the code.
//   - CURATED — curated.tony, for the types codegen cannot describe correctly (a custom codec
//     means the wire shape is not the Go shape). These CAN drift, so a test holds the set equal
//     to the types that declare `codec=custom`. See curated_test.go.
//   - THE ROOT — root.tony, the top-level paths and the shape served at each. No Go type
//     declares the surface, so nothing can generate it.
//
// Where a definition came from is OUR problem, not the reader's: what is published is one
// document, because a caller resolving verse-match wants its shape, and "hand-written or
// derived" tells them nothing they can act on.
//
// # One schema, not thirty
//
// The parts arrive as separate signature documents — one per type, each with its own context
// and cross-package aliases (`launch:verse-launch-spec`). They are FOLDED into a single
// schema's `define`, which is how tony's own base schema is written: one document, one
// signature, a namespace of definitions that refer to each other by bare name. So the
// aliases go away, the contexts merge, and what a reader gets is one self-contained thing.
//
// The point of assembling it HERE, with no route table and no server, is that the full schema
// is obtainable without either — `verse schema` reads it over HTTP, but a test, a generator or
// a person at a prompt can have it from this package for the asking. The API is downstream: it
// walks the root for the paths it serves, and has to agree with what it finds.
//
// What is defined are I/O SHAPES: what goes on the wire, which is what tony-codegen generates
// from. A tony schema can also carry an `accept:` clause and validate documents; this one does
// not, and that is a separate thing to author deliberately rather than a gap here.
package schema

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/config"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/trigger"
)

//go:embed curated.tony
var curatedDoc []byte

//go:embed root.tony
var rootDoc []byte

// Name is what the assembled schema is called, and RootDef is the definition inside it whose
// keys are the top-level paths — `verse:root`, if you are naming it from outside.
const (
	Name    = "verse"
	RootDef = "root"
)

// The parts arrive as signature documents: a tony object carrying `context` (what is in scope),
// `define` (the shape) and `signature` (its name). The name is what everything else refers to
// it by, so it is the key it folds in under.
const (
	contextField = "context"
	defineField  = "define"
	sigField     = "signature"
	nameField    = "name"
)

// versePkg prefixes the import paths of THIS module's packages. A context alias pointing at one
// of them is a cross-package reference, which folding dissolves — there is only one namespace
// afterwards, so `launch:verse-launch-spec` is just verse-launch-spec. An alias pointing
// anywhere else (tony-base, format) names a schema that is genuinely elsewhere and stays.
const versePkg = "github.com/signadot/verse/"

var (
	once     sync.Once
	global   *ir.Node
	byName   map[string]*ir.Node
	curated  map[string]bool
	loadErr  error
	sortedNm []string
)

// load parses and folds every embedded part, once. A malformed embedded schema is a build-time
// mistake surfaced at first use rather than a panic at init: the endpoint reports it, and the
// rest of the server keeps serving.
func load() {
	byName = map[string]*ir.Node{}
	curated = map[string]bool{}
	aliases := map[string]*ir.Node{}

	// Generated first, so a curated definition overrides rather than races it.
	for _, src := range [][]byte{api.Schema, entity.Schema, trigger.Schema, task.Schema, config.Schema, launch.Schema} {
		if err := fold(src, aliases, false); err != nil {
			loadErr = err
			return
		}
	}
	if err := fold(curatedDoc, aliases, true); err != nil {
		loadErr = err
		return
	}
	if err := fold(rootDoc, aliases, false); err != nil {
		loadErr = err
		return
	}
	for n := range byName {
		sortedNm = append(sortedNm, n)
	}
	sort.Strings(sortedNm)

	// `root` sorts before every `verse-…`, so the assembled document reads in the order it is
	// used: what a verse IS, then the shapes that answer it. That is luck rather than design,
	// but it is the reason the root is not called verse-root.
	// The document gets its OWN nodes: byName keeps parentless definitions, so a definition
	// read on its own does not arrive with a pointer back into the whole schema.
	defs := make(map[string]*ir.Node, len(byName))
	for n, d := range byName {
		defs[n] = d.DeepCopy()
	}
	ctx := []*ir.Node{ir.FromString("tony-format/context")}
	for _, a := range sortedKeys(aliases) {
		ctx = append(ctx, aliases[a])
	}
	global = ir.FromMap(map[string]*ir.Node{
		contextField: ir.FromSlice(ctx),
		defineField:  ir.FromMap(defs),
		sigField:     ir.FromMap(map[string]*ir.Node{nameField: ir.FromString(Name)}),
	})
}

// fold parses a multi-document tony file and files each document's `define` under its signature
// name, with cross-package references rewritten to the bare names they become in one namespace.
// Context entries naming a schema OUTSIDE this module are collected for the assembled document.
func fold(src []byte, aliases map[string]*ir.Node, isCurated bool) error {
	docs, err := codec.ParseNodes(src, codec.Tony)
	if err != nil {
		return fmt.Errorf("parse schema: %w", err)
	}
	for _, d := range docs {
		// A signature document may OPEN with a comment — every file here does — and a head
		// comment wraps the document it precedes. Unwrapped, the whole document folds in under
		// no name and vanishes silently.
		d = ir.Uncomment(d)
		name := signatureName(d)
		if name == "" {
			continue // a document with no signature name folds in under nothing
		}
		def := ir.Get(d, defineField)
		if def == nil {
			continue
		}
		def = def.DeepCopy()
		strip := contexts(d, aliases)
		dissolve(def, strip)
		if isCurated {
			// A CURATED DEFINITION ALWAYS WINS. This used to be "curated fills gaps only" —
			// a generated definition won unless its `define` was empty — on the theory that
			// derived truth beats a hand-written claim and a fixed generator would take over
			// by itself. That rule was wrong, and wrong in the dangerous direction.
			//
			// An empty `define` is the BENIGN symptom of a hand-written codec, and it is
			// what codegen emits for a named map (trigger.Match). For a STRUCT with a custom
			// codec it emits the Go type's shape instead — a non-empty, confident, false
			// description, since `codec=custom` is precisely the declaration that the wire
			// shape is not the Go shape. verse-formula is published as {args, op, req} and
			// emits `!quorum 2`; verse-condition types its `when` as that object and emits an
			// address string. Under the old rule a curated correction for either would have
			// been shadowed by the lie it was written to replace.
			//
			// So: writing a curated entry is the deliberate act, and removing one is too —
			// as already happened for verse-entity and verse-event once v0.0.93 could derive
			// them. Nothing silently reverts to a generated definition; that is the point.
			curated[name] = true
		}
		byName[name] = def
	}
	return nil
}

// contexts records a document's external context entries in aliases and returns the alias
// prefixes to dissolve — the ones naming packages of this module, which cease to be elsewhere
// once everything shares one define.
func contexts(d *ir.Node, aliases map[string]*ir.Node) []string {
	var strip []string
	list := ir.Get(d, contextField)
	if list == nil {
		return nil
	}
	for _, entry := range list.Values {
		if entry.Type != ir.ObjectType || len(entry.Fields) != 1 {
			continue // a bare context URI (tony-format/context), which every part carries
		}
		alias, path := entry.Fields[0].String, entry.Values[0].String
		if strings.HasPrefix(path, versePkg) {
			strip = append(strip, alias)
			continue
		}
		aliases[alias] = entry.DeepCopy()
	}
	return strip
}

// dissolve rewrites `<alias>:verse-thing` to `verse-thing` throughout a definition, in both
// type expressions (`.[nullable(launch:verse-launch-spec)]`) and tags. A qualified name that
// survived would resolve against a schema that no longer exists separately.
func dissolve(n *ir.Node, strip []string) {
	if n == nil || len(strip) == 0 {
		return
	}
	for _, alias := range strip {
		n.String = strings.ReplaceAll(n.String, alias+":", "")
		n.Tag = strings.ReplaceAll(n.Tag, alias+":", "")
	}
	for _, f := range n.Fields {
		dissolve(f, strip)
	}
	for _, v := range n.Values {
		dissolve(v, strip)
	}
}

// signatureName reads `signature.name` from a signature document.
func signatureName(d *ir.Node) string {
	d = ir.Uncomment(d)
	if d == nil || d.Type != ir.ObjectType {
		return ""
	}
	sig := ir.Get(d, sigField)
	if sig == nil || sig.Type != ir.ObjectType {
		return ""
	}
	n := ir.Get(sig, nameField)
	if n == nil || n.Type != ir.StringType {
		return ""
	}
	return n.String
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Global returns the whole schema as one tony document: the context it lives in, every
// definition under its name (the root first), and the signature naming it.
//
// The copy is the caller's — nothing hands out the assembled node itself, since a consumer that
// rendered it and a consumer that edited it would otherwise be the same consumer.
func Global() (*ir.Node, error) {
	once.Do(load)
	if loadErr != nil {
		return nil, loadErr
	}
	return global.DeepCopy(), nil
}

// Names lists every definition, sorted.
func Names() ([]string, error) {
	once.Do(load)
	return sortedNm, loadErr
}

// Get returns one definition by name — the shape itself, as it stands in the schema's define.
// Its references are bare names resolving against the rest of Global, so a definition read on
// its own is a fragment of that document rather than a self-contained schema.
func Get(name string) (*ir.Node, bool, error) {
	once.Do(load)
	if loadErr != nil {
		return nil, false, loadErr
	}
	d, ok := byName[name]
	if !ok {
		return nil, false, nil
	}
	return d.DeepCopy(), true, nil
}

// Root returns the definition whose keys are the top-level paths — what a verse looks like.
// This is what the API walks to find the paths it must agree with.
func Root() (*ir.Node, bool, error) { return Get(RootDef) }

// Curated reports the names whose definition is hand-written rather than generated — the part
// of the schema that is a claim about the code rather than a product of it.
//
// This is BUILD HYGIENE, not part of the published schema: it exists so a test can hold the
// curated set equal to the types that actually need one, and catch the entry that outlives its
// reason. Nothing in the document says which kind a definition is.
func Curated() ([]string, error) {
	once.Do(load)
	if loadErr != nil {
		return nil, loadErr
	}
	return sortedKeys(curated), nil
}
