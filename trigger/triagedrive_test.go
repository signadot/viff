package trigger

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// The triage drive's `mark` rule, as the file spells it, selects exactly the open issues no row
// says were judged as they now stand: a seeded row keeps the backlog quiet, an issue nobody
// judged is selected, and an issue that moves past its judgement is selected again — while one
// judged as it stands is not, however often the store is touched. The action is swapped for a
// patch so the selection is what is under test, not the coderunner.
func TestTheTriageDriveMarksWhatMovedAndOnlyThat(t *testing.T) {
	src, err := os.ReadFile("../docs/working/issue-triage.tony")
	if err != nil {
		t.Fatal(err)
	}
	trigs, _, _, _, ok := parseBlock(string(src))
	if !ok {
		t.Fatal("docs/working/issue-triage.tony did not parse as a charter")
	}
	var mark Trigger
	for _, tr := range trigs {
		if tr.Name == "mark" {
			mark = tr
		}
	}
	if mark.Name == "" {
		t.Fatal("no `mark` rule in the file")
	}
	mark.Stratum = "" // no levels in this engine: the selection is the question
	mark.Action = setStage("judged")

	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	seeded := entity.RefAt("git-issue", "issue", "verse/aaa")
	fresh := entity.RefAt("git-issue", "issue", "verse/bbb")
	other := entity.RefAt("git-issue", "issue", "tony-format/ccc")
	setPayload(t, store, seeded, `{id: aaa, repo: verse, status: open, updatedAt: "t1", title: seeded}`)
	setPayload(t, store, fresh, `{id: bbb, repo: verse, status: open, updatedAt: "t1", title: fresh}`)
	setPayload(t, store, other, `{id: ccc, repo: tony-format, status: open, updatedAt: "t1", title: elsewhere}`)
	// What scripts/triage-seed.sh writes: the seeded issue was judged as it stands.
	setPayload(t, store, entity.RefAt("verse-dev", "triage", "next", "aaa"), `{judged: {id: aaa, updatedAt: "t1"}}`)

	if err := eng.AddTrigger(mark); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// A commit to consider the rule at: the fresh issue is selected, the seeded and the other
	// repository's are not.
	setPayload(t, store, fresh, `{id: bbb, repo: verse, status: open, updatedAt: "t1", title: fresh, touched: 1}`)
	waitStage(t, store, fresh, "judged")
	if got := field(t, store, seeded, "stage"); got != nil {
		t.Fatalf("the seeded issue was judged: stage=%v", got)
	}
	if got := field(t, store, other, "stage"); got != nil {
		t.Fatalf("another repository's issue was judged: stage=%v", got)
	}

	// The judgement lands as the hook would write it, and the issue is quiet from then on.
	setPayload(t, store, entity.RefAt("verse-dev", "triage", "next", "bbb"), `{judged: {id: bbb, updatedAt: "t1"}, next: build}`)
	setPayload(t, store, fresh, `{id: bbb, repo: verse, status: open, updatedAt: "t1", title: fresh, stage: quiet}`)
	setPayload(t, store, seeded, `{id: aaa, repo: verse, status: open, updatedAt: "t1", title: seeded, touched: 2}`)
	if got := field(t, store, fresh, "stage"); got != "quiet" {
		t.Fatalf("an issue judged as it stands was judged again: stage=%v", got)
	}

	// It moves — a comment, a label, a link — and is selected again.
	setPayload(t, store, seeded, `{id: aaa, repo: verse, status: open, updatedAt: "t2", title: seeded, comments: 1}`)
	waitStage(t, store, seeded, "judged")
}

// The `mark` rule's derive, as written: an issue with none of the optional facts — no labels,
// no links, no body, no discussion — still gets its row and its mark, so an issue that says
// little is not one the drive trips over; and when the issue moves after a judgement landed,
// the re-derive reopens the row by taking `stage` off it while the judgement stays readable.
func TestTheTriageDriveMarksAnIssueThatSaysLittleAndReopensARow(t *testing.T) {
	src, err := os.ReadFile("../docs/working/issue-triage.tony")
	if err != nil {
		t.Fatal(err)
	}
	trigs, _, strata, _, ok := parseBlock(string(src))
	if !ok {
		t.Fatal("did not parse")
	}
	var mark Trigger
	for _, tr := range trigs {
		if tr.Name == "mark" {
			mark = tr
		}
	}

	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	for _, st := range strata {
		if err := eng.AddStratum(st); err != nil {
			t.Fatalf("AddStratum %s: %v", st.Name, err)
		}
	}
	issue := entity.RefAt("git-issue", "issue", "verse/ddd")
	row := entity.RefAt("verse-dev", "triage", "next", "ddd")
	// As the source writes an issue that says little: every field present, the lists empty.
	terse := `id: ddd, repo: verse, status: open, createdAt: "t0", updatedAt: "t1", title: terse, labels: [], commits: [], branches: [], related: [], blocks: [], blockedBy: [], body: "", comments: 0, discussion: []`
	setPayload(t, store, issue, "{"+terse+"}")
	if err := eng.AddTrigger(mark); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	setPayload(t, store, issue, "{"+terse+", touched: 1}")
	waitField(t, store, row, "title", "terse")
	e, _, _ := store.Get(row)
	if got := entity.Str(entity.Get(e.Payload, "judged"), "updatedAt"); got != "t1" {
		t.Fatalf("the mark = %v", e.Payload)
	}

	// The judge lands, as its hook writes it.
	setPayload(t, store, row, `{judged: {id: ddd, updatedAt: "t1"}, next: note, why: "a note", stage: judged}`)
	// The issue moves: the row is re-marked, reopened, and the last judgement still stands on it.
	setPayload(t, store, issue, `{id: ddd, repo: verse, status: open, createdAt: "t0", updatedAt: "t2", title: terse, labels: [], commits: [], branches: [], related: [], blocks: [], blockedBy: [], body: "", comments: 1, discussion: [{at: "t2", text: "on it"}]}`)
	deadline := time.After(3 * time.Second)
	for {
		e, _, _ := store.Get(row)
		if entity.Str(entity.Get(e.Payload, "judged"), "updatedAt") == "t2" {
			p := entity.ToObj(e.Payload)
			if _, has := p["stage"]; has {
				t.Fatalf("the re-derive did not reopen the row: %v", e.Payload)
			}
			if p["next"] != "note" {
				t.Fatalf("the last judgement was lost on re-mark: %v", e.Payload)
			}
			if entity.Get(e.Payload, "discussion") == nil {
				t.Fatalf("the discussion was not copied onto the row: %v", e.Payload)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the moved issue was not re-marked: %v", e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
