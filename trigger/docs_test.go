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
	"github.com/signadot/verse/process"
)

// Every charter written in the docs must install.
//
// This exists because one did not. The tutorial taught a spawn-only rule, `patch` was made a
// prerequisite for spawning, and nothing connected the two — the first person to find out was
// someone typing the tutorial in. Prose about a grammar goes stale silently; the only thing
// that does not is running it.
//
// It extracts fenced tony/json blocks AND heredoc bodies inside shell blocks (which is how the
// tutorial shows `verse trigger add -f -`), keeps whatever parses as a trigger with a name and
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
	total, n := 0, 0
	for _, path := range docs {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, blk := range charterBlocks(string(src)) {
			// Bounded, because a parse can HANG rather than fail: go-tony loops forever on a
			// comment following a flow collection (`a: {} # c`), which is a shape a charter is
			// naturally written in and which therefore reached the docs. A hang here is a
			// failure like any other rather than a suite that never finishes.
			trigs, strata, ok, timedOut := parseWithin(blk.text, 5*time.Second)
			if timedOut {
				t.Errorf("%s:%d: parsing this block does not terminate — a reader who pastes "+
					"it gets a hung client:\n\n%s", filepath.Base(path), blk.line, indent(blk.text))
				continue
			}
			if !ok {
				continue
			}
			total++
			eng := New(store, process.AutoResolver{})
			// The ORDERING first, as an install does — a rule naming a stratum that is not
			// there is held rather than refused, which would make a stratified example pass
			// this test by not being checked.
			for _, s := range strata {
				if err := eng.addStratum(s, false); err != nil {
					t.Errorf("%s:%d: stratum %q does not install:\n  %v\n\n%s",
						filepath.Base(path), blk.line, s.Name, err, indent(blk.text))
				}
			}
			for _, tr := range trigs {
				name := tr.Name
				n++
				tr.Name = fmt.Sprintf("doc%d", n) // unique: one engine holds them all
				if err := eng.addTrigger(tr, false); err != nil {
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
	t.Logf("installed %d charter block(s) from %d docs", total, len(docs))
}

type block struct {
	text string
	line int
}

var fence = regexp.MustCompile("(?s)```(tony|json|yaml)\\n(.*?)```")

// charterBlocks yields every fenced tony/json block, plus every heredoc body (which is how a
// doc shows a charter being piped into `verse trigger add -f -`). Go's regexp has no
// backreference, so the heredoc terminator is matched by scanning rather than by pattern.
func charterBlocks(src string) []block {
	var out []block
	at := func(idx int) int { return strings.Count(src[:idx], "\n") + 1 }
	for _, m := range fence.FindAllStringSubmatchIndex(src, -1) {
		out = append(out, block{text: src[m[4]:m[5]], line: at(m[0])})
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
func parseWithin(text string, d time.Duration) (trigs []Trigger, strata []Stratum, ok, timedOut bool) {
	type result struct {
		trigs  []Trigger
		strata []Stratum
		ok     bool
	}
	done := make(chan result, 1)
	go func() {
		ts, ss, k := parseBlock(text)
		done <- result{ts, ss, k}
	}()
	select {
	case r := <-done:
		return r.trigs, r.strata, r.ok, false
	case <-time.After(d):
		return nil, nil, false, true
	}
}

// parseBlock reads a block as a charter, in either dialect: one definition, a list of them, or
// several `---`-separated documents, mixing rules and strata as a real charter file does.
//
// It reports false for anything that is not a charter — which is most blocks, and is the point:
// a fragment, an entity payload and a goal record all fail to be a trigger with a subject. A
// block of ONLY strata is not a charter either; there would be no rule to check against it.
func parseBlock(text string) ([]Trigger, []Stratum, bool) {
	for _, f := range []codec.Format{codec.Tony, codec.JSON} {
		defs, err := codec.ParseNodes([]byte(text), f)
		if err != nil {
			continue
		}
		var flat []*ir.Node
		for _, doc := range defs {
			doc = codec.Unwrap(doc)
			if doc == nil || doc.Type == ir.NullType {
				continue
			}
			if doc.Type == ir.ArrayType {
				for _, el := range doc.Values {
					if el = codec.Unwrap(el); el != nil {
						flat = append(flat, el)
					}
				}
				continue
			}
			flat = append(flat, doc)
		}
		trigs, strata, ok := classify(flat)
		if ok {
			return trigs, strata, true
		}
	}
	return nil, nil, false
}

// classify sorts a block's definitions into rules and strata, and reports false the moment one
// is neither. Told apart by decoding: a stratum refuses a field it does not know (so a rule
// fails to be one), and a rule without a subject is not installable (so a stratum fails to be
// one).
func classify(defs []*ir.Node) ([]Trigger, []Stratum, bool) {
	var trigs []Trigger
	var strata []Stratum
	for _, def := range defs {
		var s Stratum
		if err := s.FromTonyIR(def); err == nil && isOrdering([]Stratum{s}) {
			strata = append(strata, s)
			continue
		}
		var t Trigger
		if err := t.FromTonyIR(def); err == nil && isCharter([]Trigger{t}) {
			trigs = append(trigs, t)
			continue
		}
		return nil, nil, false
	}
	return trigs, strata, len(trigs) > 0
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
