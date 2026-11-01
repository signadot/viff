package trigger

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/stock"
	"github.com/signadot/verse/task"
)

// Every charter written in the docs must install.
//
// This exists because one did not. The tutorial taught a spawn-only rule, `patch` was made a
// prerequisite for spawning, and nothing connected the two — the first person to find out was
// someone typing the tutorial in. Prose about a grammar goes stale silently; the only thing
// that does not is running it.
//
// It extracts fenced tony/json blocks AND heredoc bodies inside shell blocks (which is how the
// tutorial shows `verse c -a -f -`), keeps whatever parses as a trigger with a name and
// a subject, and installs it on a fresh engine. Anything that is not a charter — a fragment, a
// goal record, an entity payload — simply does not parse as one and is skipped.
func TestDocCharartersInstall(t *testing.T) {
	docs, err := filepath.Glob("../docs/*.md")
	if err != nil || len(docs) == 0 {
		t.Fatalf("no docs found: %v", err)
	}
	nested, _ := filepath.Glob("../docs/*/*.md")
	docs = append(docs, nested...)

	// One STORE, and installs that do not persist: this is about whether a charter is
	// ACCEPTED, and a store per charter costs a logd apiece. An ENGINE per block, though,
	// which costs nothing (New starts nothing until Start) and is what lets a doc declare
	// strata: an ordering is charter-wide, so two docs each owning `gh` at level 0 would
	// collide on a shared engine over nothing more than both being examples.
	store := entitytest.Store(t)
	total, n, proposed, resolvers, packaged := 0, 0, 0, 0, 0
	for _, path := range docs {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, blk := range charterBlocks(string(src)) {
			if blk.proposed {
				proposed++
				continue
			}
			// A resolver is tony, is not a charter, and would be read as one carrying
			// unknown fields. It cannot be CHECKED here — `resolver` imports this package, so
			// the arrow cannot come back — so it is classified structurally and counted: a
			// resolver says how it answers, and no rule has a `method`.
			if isResolver(blk.text) {
				resolvers++
				continue
			}
			// Bounded, because a parse can HANG rather than fail: go-tony loops forever on a
			// comment following a flow collection (`a: {} # c`), which is a shape a charter is
			// naturally written in and which therefore reached the docs. A hang here is a
			// failure like any other rather than a suite that never finishes.
			trigs, nodes, strata, pkg, ok, timedOut := parseWithin(blk.text, 5*time.Second)
			if timedOut {
				t.Errorf("%s:%d: parsing this block does not terminate — a reader who pastes "+
					"it gets a hung client:\n\n%s", filepath.Base(path), blk.line, indent(blk.text))
				continue
			}
			if !ok {
				continue
			}
			total++
			eng := New(store, task.AutoResolver{})
			eng.SetOfferCheck(upOffers)
			// A BLOCK THAT DECLARES A PACKAGE IS CHECKED AS ONE: confined to the address it
			// names and reading only what it declares it reads, so a doc example claiming isolation is
			// held to it rather than passing on an engine that has neither.
			if pkg.at != "" || len(pkg.reads) > 0 {
				packaged++
				at, err := ParseAt(pkg.at)
				if err != nil {
					t.Errorf("%s:%d: `at: %s` is not an address a package installs at:\n  %v",
						filepath.Base(path), blk.line, pkg.at, err)
					continue
				}
				eng.Confine(at)
				eng.SetReads(pkg.reads)
			}
			// The ORDERING first, as an install does — a rule naming a stratum that is not
			// there is held rather than refused, which would make a stratified example pass
			// this test by not being checked.
			for _, s := range strata {
				if err := eng.addStratum(s); err != nil {
					t.Errorf("%s:%d: stratum %q does not install:\n  %v\n\n%s",
						filepath.Base(path), blk.line, s.Name, err, indent(blk.text))
				}
			}
			// The field check, which is the SERVER's and not addTrigger's: an unknown field
			// decodes silently, so a doc charter carrying one installs cleanly here and is
			// refused over the wire — which is the one way this test could pass while the
			// thing it documents does not work.
			for i, node := range nodes {
				if err := CheckFields(node); err != nil {
					t.Errorf("%s:%d: charter %q would be refused by `verse c -a`:\n  %v\n\n%s",
						filepath.Base(path), blk.line, trigs[i].Name, err, indent(blk.text))
				}
			}
			for _, tr := range trigs {
				name := tr.Name
				n++
				tr.Name = fmt.Sprintf("doc%d", n) // unique: one engine holds them all
				if err := eng.addTrigger(tr, "", RunnersChecked); err != nil {
					t.Errorf("%s:%d: charter %q does not install:\n  %v\n\n%s",
						filepath.Base(path), blk.line, name, err, indent(blk.text))
				}
			}
		}
	}
	if total == 0 {
		t.Fatal("found no charters in the docs — the extractor has stopped working, which " +
			"would make this test pass forever while checking nothing")
	}
	t.Logf("installed %d charter block(s) from %d docs (%d as packages, confined and declared); "+
		"%d marked proposed, %d resolvers", total, len(docs), packaged, proposed, resolvers)
	if packaged == 0 {
		t.Error("no doc block declared a package — either the docs stopped documenting them or " +
			"the declaration stopped being recognized, and both make this test check less than " +
			"it says")
	}
}

// upOffers checks a spawn as `verse up`'s own runner, with a model key, would at install: a doc
// charter is typed in on `verse up` first, and a spawn that runner refuses (it builds only the
// host's network) is a tutorial that stops at its first install. A spawn for an agent `verse up`
// does not offer, or giving a system its runner does not hold (a repository, a Slack channel), is
// for a runner set up elsewhere, and is not this test's to judge.
func upOffers(l runner.Launch) error {
	if l.Who() != "" && l.Who() != stock.AgentName {
		return nil
	}
	desc := stock.Config{Agents: map[string]stock.Agent{stock.AgentName: {}}, Script: &stock.Script{}}.Describe([]string{runner.NetworkOpen})
	for _, o := range desc.Offers {
		if o.Script != (l.Script != "") {
			continue
		}
		for name := range l.Environment.Systems {
			if _, held := o.Systems[name]; !held && name != "network" {
				return nil
			}
		}
	}
	return runnerd.CheckOfferAsWritten(&l, desc)
}

type block struct {
	text     string
	line     int
	proposed bool // fenced ```tony proposed: syntax that does not exist yet
}

// isResolver reports whether a block is a resolver rather than a charter: it says how it
// ANSWERS, which no rule does. Structural rather than a fence marker, so it cannot be applied to
// a charter by mistake — the thing `proposed` has to be watched for.
func isResolver(text string) bool {
	return strings.Contains(text, "method:") && strings.Contains(text, "driver:") &&
		!strings.Contains(text, "action:")
}

// A fence may carry `proposed` after the language — ```tony proposed — which marks a block
// that shows syntax the grammar does not have yet. A working note argues for a shape before
// it exists, and holding those to "must install" would either fail the suite or push the
// proposal out of the doc into prose, where it stops being checkable by a reader too.
//
// The marker is part of the pattern rather than an unmatched fence so the skip is DELIBERATE:
// a block that simply failed to match would be skipped silently, which is the rot this test
// exists to catch. Skipped blocks are counted and logged, so "proposed" cannot quietly become
// where charters go to avoid being checked.
var fence = regexp.MustCompile("(?s)```(tony|json|yaml)( +proposed)?\\n(.*?)```")

// charterBlocks yields every fenced tony/json block, plus every heredoc body (which is how a
// doc shows a charter being piped into `verse c -a -f -`). Go's regexp has no
// backreference, so the heredoc terminator is matched by scanning rather than by pattern.
func charterBlocks(src string) []block {
	var out []block
	at := func(idx int) int { return strings.Count(src[:idx], "\n") + 1 }
	for _, m := range fence.FindAllStringSubmatchIndex(src, -1) {
		out = append(out, block{text: src[m[6]:m[7]], line: at(m[0]), proposed: m[4] >= 0})
	}
	out = append(out, heredocs(src, at)...)
	return out
}

// heredocs finds `<<TAG` / `<<'TAG'` bodies, ending each at a line holding just TAG.
func heredocs(src string, at func(int) int) []block {
	var out []block
	lines := strings.Split(src, "\n")
	off := 0
	for i := 0; i < len(lines); i++ {
		off += len(lines[i]) + 1
		j := strings.Index(lines[i], "<<")
		if j < 0 {
			continue
		}
		tag := strings.TrimSpace(strings.Trim(lines[i][j+2:], "'\""))
		if tag == "" || strings.ContainsAny(tag, " \t") {
			continue
		}
		start, body := off, []string{}
		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != tag; i++ {
			body = append(body, lines[i])
			off += len(lines[i]) + 1
		}
		if i < len(lines) {
			off += len(lines[i]) + 1
			out = append(out, block{text: strings.Join(body, "\n"), line: at(start)})
		}
	}
	return out
}

// parseWithin is parseBlock, bounded. The goroutine it abandons on a timeout is the price
// of reporting a hang instead of becoming one; a hung parse holds one goroutine until the test
// binary exits.
func parseWithin(text string, d time.Duration) (trigs []Trigger, nodes []*ir.Node, strata []Stratum, pkg pkgDecl, ok, timedOut bool) {
	type result struct {
		trigs  []Trigger
		nodes  []*ir.Node
		strata []Stratum
		pkg    pkgDecl
		ok     bool
	}
	done := make(chan result, 1)
	go func() {
		ts, ns, ss, p, k := parseBlock(text)
		done <- result{ts, ns, ss, p, k}
	}()
	select {
	case r := <-done:
		return r.trigs, r.nodes, r.strata, r.pkg, r.ok, false
	case <-time.After(d):
		return nil, nil, nil, pkgDecl{}, false, true
	}
}

// pkgDecl is the `at:`/`reads:` document a doc block may open with — what makes the block a
// PACKAGE rather than a charter, and what the engine below is confined and declared from.
//
// Without this a doc example that says it installs at an address was checked as if it did not:
// classify saw a document that was neither a rule nor a stratum and skipped the whole block, so
// the one example whose point is the confinement was the one nothing ran. (Found that way: the
// example in trigger-model.md derived with no stratum and did not install.)
type pkgDecl struct {
	at    string
	reads []Subtree
}

// parseBlock reads a block as a charter, in either dialect: a fielded charter, or the shorthand —
// one definition, a list of them, or several `---`-separated documents of them.
//
// It reports false for anything that is not a charter — which is most blocks, and is the point:
// a fragment, an entity payload and a goal record all fail to be a trigger with a subject. A
// block of ONLY strata is not a charter either; there would be no rule to check against it.
func parseBlock(text string) ([]Trigger, []*ir.Node, []Stratum, pkgDecl, bool) {
	for _, f := range []codec.Format{codec.Tony, codec.JSON} {
		defs, err := codec.ParseNodes([]byte(text), f)
		if err != nil {
			continue
		}
		var flat []*ir.Node
		charters, bare := 0, false
		for _, doc := range defs {
			doc = codec.Unwrap(doc)
			if doc == nil || doc.Type == ir.NullType {
				continue
			}
			if doc.Type == ir.ArrayType {
				bare = true
				for _, el := range doc.Values {
					if el = codec.Unwrap(el); el != nil {
						flat = append(flat, el)
					}
				}
				continue
			}
			if decl, lists, ok := fielded(doc); ok {
				charters++
				if decl {
					flat = append(flat, doc)
				}
				flat = append(flat, lists...)
				continue
			}
			bare = true
			flat = append(flat, doc)
		}
		// One charter, or bare definitions — as the install takes them. A block that mixes the
		// two is refused there, so it is no charter here.
		if charters > 1 || (charters > 0 && bare) {
			continue
		}
		trigs, nodes, strata, pkg, ok := classify(flat)
		if ok {
			return trigs, nodes, strata, pkg, true
		}
	}
	return nil, nil, nil, pkgDecl{}, false
}

// classify sorts a block's definitions into rules and strata, and reports false the moment one
// is neither. Told apart by decoding: a stratum refuses a field it does not know (so a rule
// fails to be one), and a rule without a subject is not installable (so a stratum fails to be
// one).
func classify(defs []*ir.Node) ([]Trigger, []*ir.Node, []Stratum, pkgDecl, bool) {
	var trigs []Trigger
	var nodes []*ir.Node
	var strata []Stratum
	var pkg pkgDecl
	for _, def := range defs {
		if at, ok := declaredAt(def); ok {
			ims, err := ReadsFromNode(def)
			if err != nil {
				return nil, nil, nil, pkgDecl{}, false
			}
			pkg = pkgDecl{at: at, reads: ims}
			continue
		}
		var s Stratum
		if err := s.FromTonyIR(def); err == nil && isOrdering([]Stratum{s}) {
			strata = append(strata, s)
			continue
		}
		var t Trigger
		if err := t.FromTonyIR(def); err == nil && isCharter([]Trigger{t}) {
			trigs = append(trigs, t)
			nodes = append(nodes, def) // kept for the field check, which reads the DOCUMENT
			continue
		}
		return nil, nil, nil, pkgDecl{}, false
	}
	return trigs, nodes, strata, pkg, len(trigs) > 0
}

// fielded reports whether a document is a fielded charter — `at:`, `reads:`, `strata:`,
// `schedules:`, `rules:` — and if so, whether it declares a package and the levels and rules it
// holds. Its schedules are not checked here: a block is checked against its rules and levels.
func fielded(doc *ir.Node) (decl bool, defs []*ir.Node, ok bool) {
	if doc.Type != ir.ObjectType {
		return false, nil, false
	}
	for i, key := range doc.Fields {
		if key == nil || key.Type != ir.StringType {
			continue
		}
		switch key.String {
		case "at", "reads":
			decl, ok = true, true
		case "schedules":
			ok = true
		case "rules", "strata":
			ok = true
			if v := codec.Unwrap(doc.Values[i]); v != nil && v.Type == ir.ArrayType {
				for _, el := range v.Values {
					if el = codec.Unwrap(el); el != nil {
						defs = append(defs, el)
					}
				}
			}
		default:
			return false, nil, false
		}
	}
	return decl, defs, ok
}

// declaredAt reads a package declaration's address, and reports whether the document is one at
// all: `at:` alone is a package that imports nothing, and `reads:` alone is one whose address
// comes from `--at` on the command line.
func declaredAt(def *ir.Node) (string, bool) {
	n := codec.Unwrap(def)
	if n == nil || n.Type != ir.ObjectType {
		return "", false
	}
	var at string
	var isDecl bool
	for i, key := range n.Fields {
		if key == nil || key.Type != ir.StringType {
			continue
		}
		switch key.String {
		case "at":
			isDecl = true
			if v := codec.Unwrap(n.Values[i]); v != nil && v.Type == ir.StringType {
				at = v.String
			}
		case "reads":
			isDecl = true
		case "condition", "action", "level", "match", "name":
			return "", false // a rule or a stratum, whatever else it carries
		}
	}
	return at, isDecl
}

// isOrdering reports whether this decoded as a real stratum — a name and a pattern, which a
// trigger, a goal record and an entity payload all fail to have under these field names.
func isOrdering(ss []Stratum) bool {
	if len(ss) == 0 {
		return false
	}
	for _, s := range ss {
		if s.Name == "" || len(s.Match) == 0 {
			return false
		}
	}
	return true
}

func isCharter(ts []Trigger) bool {
	if len(ts) == 0 {
		return false
	}
	for _, t := range ts {
		if t.Name == "" || len(t.Condition.Subject) == 0 {
			return false
		}
	}
	return true
}

func indent(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("    " + l + "\n")
	}
	return b.String()
}
