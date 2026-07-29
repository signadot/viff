package trigger

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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

	// One engine, and installs that do not persist: this is about whether a charter is
	// ACCEPTED, and a store per charter costs a logd apiece.
	eng := New(entitytest.Store(t), process.AutoResolver{})
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
			trigs, ok, timedOut := parseWithin(blk.text, 5*time.Second)
			if timedOut {
				t.Errorf("%s:%d: parsing this block does not terminate — a reader who pastes "+
					"it gets a hung client:\n\n%s", filepath.Base(path), blk.line, indent(blk.text))
				continue
			}
			if !ok {
				continue
			}
			total++
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

// parseWithin is parseTriggers, bounded. The goroutine it abandons on a timeout is the price
// of reporting a hang instead of becoming one; a hung parse holds one goroutine until the test
// binary exits.
func parseWithin(text string, d time.Duration) (trigs []Trigger, ok, timedOut bool) {
	type result struct {
		trigs []Trigger
		ok    bool
	}
	done := make(chan result, 1)
	go func() {
		ts, k := parseTriggers(text)
		done <- result{ts, k}
	}()
	select {
	case r := <-done:
		return r.trigs, r.ok, false
	case <-time.After(d):
		return nil, false, true
	}
}

// parseTriggers reads a block as a charter, in either dialect, as a list or a single rule. It
// reports false for anything that is not one — which is most blocks, and is the point: a
// fragment, an entity payload and a goal record all fail to be a trigger with a subject.
func parseTriggers(text string) ([]Trigger, bool) {
	for _, f := range []codec.Format{codec.Tony, codec.JSON} {
		var list []Trigger
		if err := codec.Unmarshal([]byte(text), &list, f); err == nil && isCharter(list) {
			return list, true
		}
		var one Trigger
		if err := codec.Unmarshal([]byte(text), &one, f); err == nil && isCharter([]Trigger{one}) {
			return []Trigger{one}, true
		}
	}
	return nil, false
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
