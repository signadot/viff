package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/signadot/tony-format/go-tony/ir"
)

// The tui's review: each test is one thing the review found the screen doing wrong.

// AN ANSWER IS AN OPTION THE QUESTION OFFERS, by the key it is shown with: 1-6 stay the
// sections, options past proceed and deny are 7-9, and a question without proceed is not sent
// one by a.
func TestTheTUIAnswersOnlyWithTheQuestionsOptions(t *testing.T) {
	src := &fakeTUISource{snap: tuiSnapshot{errs: map[tuiTab]string{}, questions: []tuiQuestion{
		{id: "turn", pose: "Reply or end?", options: []string{"reply", "end", "later", "never", "maybe"},
			says: map[string]string{"end": "stop the task"}},
	}}}
	m := loaded(t, src)
	m.width, m.height = 120, 30
	v := m.View()
	for _, want := range []string{"7 reply", "8 end", "stop the task", "9 later", ":answer <option>"} {
		if !strings.Contains(v, want) {
			t.Errorf("the question does not show %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "a proceed") {
		t.Errorf("a question without proceed offers a:\n%s", v)
	}
	m = press(t, m, "a")
	if m.mode != modeBrowse || !strings.Contains(m.status, "options are reply, end") {
		t.Fatalf("a on a question without proceed: mode %d status %q", m.mode, m.status)
	}
	m = press(t, m, "5")
	if m.tab != tabRunners {
		t.Fatalf("5 is the Runners section, not an option: tab %v", m.tab)
	}
	m = press(t, m, "1", "8", "enter")
	if len(src.answered) != 1 || !strings.HasPrefix(src.answered[0], "turn | end |") {
		t.Fatalf("8 answered %q", src.answered)
	}
	m = press(t, m, ":")
	for _, r := range "answer maybe" {
		m = press(t, m, string(r))
	}
	m = press(t, m, "enter", "enter")
	if len(src.answered) != 2 || !strings.HasPrefix(src.answered[1], "turn | maybe |") {
		t.Fatalf(":answer maybe answered %q", src.answered)
	}
}

// THE CURSOR STAYS ON THE ROW IT WAS ON: a read that puts a new question above it does not move
// a's answer onto a question the person never read.
func TestTheTUIKeepsTheSelectedRowAcrossReads(t *testing.T) {
	src := twoQuestions()
	m := press(t, loaded(t, src), "j")
	if q, _ := m.question(); q.id != "q2" {
		t.Fatalf("j selected %q", q.id)
	}
	snap := src.snap
	snap.questions = append([]tuiQuestion{{id: "q0", pose: "New?", options: []string{"proceed", "deny"}}}, snap.questions...)
	next, _ := m.Update(snapMsg{snap: snap, seq: askSeq.Add(1)})
	m = next.(tuiModel)
	if q, _ := m.question(); q.id != "q2" {
		t.Fatalf("after a new question above it, the cursor is on %q", q.id)
	}
	m = press(t, m, "a", "enter")
	if len(src.answered) != 1 || !strings.HasPrefix(src.answered[0], "q2 |") {
		t.Fatalf("a answered %q", src.answered)
	}
}

// A FAILED READ KEEPS WHAT IT HAD, and an older read arriving after a newer one is dropped.
func TestTheTUIKeepsRowsAcrossAFailedOrLateRead(t *testing.T) {
	src := twoQuestions()
	m := press(t, loaded(t, src), "2", "j")
	failed := src.snap
	failed.tasks, failed.errs = nil, map[tuiTab]string{tabTasks: "502 bad gateway"}
	next, _ := m.Update(snapMsg{snap: failed, seq: askSeq.Add(1)})
	m = next.(tuiModel)
	if len(m.items()) != 2 || m.cursor[tabTasks] != 1 || m.onHeader {
		t.Fatalf("a failed read of the tasks left %d rows, cursor %d, header %v", len(m.items()), m.cursor[tabTasks], m.onHeader)
	}
	if v := m.View(); !strings.Contains(v, "502 bad gateway") {
		t.Errorf("the failure is not said:\n%s", v)
	}
	old, newer := askSeq.Add(1), askSeq.Add(1)
	fresh := src.snap
	fresh.questions = nil
	next, _ = m.Update(snapMsg{snap: fresh, seq: newer})
	m = next.(tuiModel)
	next, _ = m.Update(snapMsg{snap: src.snap, seq: old})
	m = next.(tuiModel)
	if len(m.snap.questions) != 0 {
		t.Fatal("an older read drew over a newer one")
	}
}

// ONE READ AT A TIME: a tick while a read is out starts none, so a slow verse is not asked
// faster than it answers.
func TestTheTUIWaitsForTheReadOut(t *testing.T) {
	m := loaded(t, twoQuestions())
	m.polls.Store(0)
	_ = m.load() // a read goes out, and does not come back
	_, cmd := m.Update(tickMsg(time.Now()))
	if m.polls.Load() != 1 {
		t.Fatalf("a tick with a read out started another: %d out", m.polls.Load())
	}
	if cmd == nil {
		t.Fatal("the tick stopped ticking")
	}
	if _, isTick := cmd().(tickMsg); !isTick {
		t.Fatal("a tick with a read out did more than wait for the next")
	}
}

// A RECORD READ FOR A ROW THE CURSOR LEFT IS NOT DRAWN, and going into a place lands on its
// first row even when a read of the place left arrives first.
func TestTheTUIDrawsOnlyWhatIsUnderTheCursor(t *testing.T) {
	src := &fakeTUISource{snap: tuiSnapshot{errs: map[tuiTab]string{}},
		paths:   []string{"a", "b", "a.x", "a.y"},
		records: map[string]*ir.Node{"a": ir.FromMap(nil), "b": ir.FromMap(nil), "a.x": ir.FromMap(nil)}}
	m := newTUI(src, "user:me", time.Second)
	m = run(t, m, m.Init())
	m = press(t, m, "3")
	next, _ := m.Update(recMsg{at: "b", rec: ir.FromString("not this")})
	m = next.(tuiModel)
	if m.recFor == "b" {
		t.Fatal("a read of b drew while the cursor is on a")
	}
	// Into a: the read of the root, asked before, arrives after.
	stale, staleSeq := src.snapshot(""), askSeq.Add(1)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(tuiModel)
	next, _ = m.Update(snapMsg{snap: stale, seq: staleSeq})
	m = next.(tuiModel)
	m = run(t, m, cmd)
	if it, ok := m.item(); !ok || m.onHeader || it.note != "a.x" {
		t.Fatalf("into a: header %v, row %q", m.onHeader, it.note)
	}
}

// SCROLLING STOPS AT THE END: G goes to the last line, and one K moves up from there.
func TestTheTUIScrollStopsAtTheEnd(t *testing.T) {
	m := press(t, loaded(t, twoQuestions()), "l", "G")
	end := m.scroll
	if end == 0 || end > 200 {
		t.Fatalf("G scrolled to %d", end)
	}
	m = press(t, m, "J", "J")
	if m.scroll != end {
		t.Fatalf("J past the end scrolled to %d, the end is %d", m.scroll, end)
	}
	m = press(t, m, "K")
	if m.scroll >= end {
		t.Fatalf("K from the end stayed at %d", m.scroll)
	}
}

// AN ADDRESS IS ITS SEGMENTS: a task names `x.pr."123"` as `x.pr.123`, a dotted key is one
// segment, and a scope's task id reads as its rule and subject.
func TestTheTUIReadsAddressesAsSegments(t *testing.T) {
	if !tokenUnder("x.pr.123", `x.pr."123"`) || !tokenUnder("x.pr.123.review", `x.pr."123"`) || tokenUnder("x.pr.1234", `x.pr."123"`) {
		t.Error("a task's subject token against a quoted address")
	}
	if got := parentOf(`verse.schedule."repos.verse.git-issue:sweep"`); got != "verse.schedule" {
		t.Errorf("parentOf a dotted key = %q", got)
	}
	if !under(`x.pr."123"`, "x.pr") || under(`x.prs."1"`, "x.pr") {
		t.Error("under compares segments")
	}
	for id, want := range map[string]string{
		"me::pkg:review@x.pr.123":       "review@123",
		"repos.verse.merge:train@x.git": "train@git",
		"review@x.a_b+849a941b":         "review@a_b",
		"run-3f2a":                      "run-3f2a",
	} {
		if got := taskShortName(id); got != want {
			t.Errorf("taskShortName(%q) = %q, want %q", id, got, want)
		}
	}
}

// THE CURSOR IS NEVER BEHIND "↓ N more": in a long explorer the row it is on is drawn.
func TestTheExplorerShowsTheCursorInALongList(t *testing.T) {
	src := twoQuestions()
	for i := range 60 {
		src.snap.tasks = append(src.snap.tasks, tuiItem{name: "pkg:work@a.step.s" + string(rune('a'+i%26)) + string(rune('a'+i/26)), state: "done      x"})
	}
	m := loaded(t, src)
	m.width, m.height = 120, 24
	m = press(t, m, "2")
	exW, _, h := m.layout()
	for i := range 40 {
		m = press(t, m, "j")
		lines := m.explorerLines(exW, h)
		name := taskShortName(m.snap.tasks[m.cursor[tabTasks]].name)
		found := false
		for _, l := range lines {
			if strings.Contains(l, name) {
				found = true
			}
		}
		if !found {
			t.Fatalf("after %d j the row %q is not drawn:\n%s", i+1, name, strings.Join(lines, "\n"))
		}
	}
}

// A TASK'S QUESTION SAYS WHAT A YES LETS IT DO: the task asking, the step and its fields, who
// may answer, and when it settles on its own.
func TestATasksQuestionShowsItsStep(t *testing.T) {
	rec := ir.FromMap(map[string]*ir.Node{
		"from":      ir.FromMap(map[string]*ir.Node{"task": ir.FromString("repos.verse.merge:train@x.pr.12")}),
		"step":      ir.FromString("github.merge"),
		"tool":      ir.FromString("merge_pr"),
		"fields":    ir.FromMap(map[string]*ir.Node{"repo": ir.FromString("signadot/verse"), "number": ir.FromString("12")}),
		"waitingOn": ir.FromSlice([]*ir.Node{ir.FromString("user:ana@x.com"), ir.FromString("user:bo@x.com")}),
		"deadline":  ir.FromMap(map[string]*ir.Node{"at": ir.FromString("2026-11-06T20:00:00Z"), "default": ir.FromString("deny")}),
		"reason":    ir.FromString("github.merge repo=signadot/verse number=12"),
		"gate":      ir.FromString("autonomy[0]"),
	})
	text := questionText(questionOf("run~d1", rec))
	for _, want := range []string{"FROM", "task repos.verse.merge:train@x.pr.12", "STEP", "github.merge", "merge_pr",
		"signadot/verse", "WAITS ON", "user:ana@x.com", "DUE", "then deny"} {
		if !strings.Contains(text, want) {
			t.Errorf("the task's question does not show %q:\n%s", want, text)
		}
	}
}

// A CHARTER SHOWS WHAT IT RUNS ON ITS OWN AND WHERE IT SENDS, and what each rule watches.
func TestACharterShowsSchedulesSendsAndWatches(t *testing.T) {
	rec := ir.FromMap(map[string]*ir.Node{
		"writes": ir.FromString("repos.verse.*"),
		"rules": ir.FromSlice([]*ir.Node{ir.FromMap(map[string]*ir.Node{
			"name":      ir.FromString("merge"),
			"condition": ir.FromMap(map[string]*ir.Node{"subject": ir.FromMap(map[string]*ir.Node{"path": ir.FromString("git.pr.*")})}),
			"gating":    ir.FromMap(map[string]*ir.Node{"gates": ir.FromSlice([]*ir.Node{ir.FromMap(nil)})}),
			"action":    ir.FromMap(map[string]*ir.Node{"spawn": ir.FromMap(nil)}),
		})}),
		"schedules": ir.FromSlice([]*ir.Node{ir.FromMap(map[string]*ir.Node{"name": ir.FromString("minute"), "every": ir.FromString("1m")})}),
		"sends":     ir.FromSlice([]*ir.Node{ir.FromMap(map[string]*ir.Node{"from": ir.FromString("install"), "to": ir.FromString("git.verse.out")})}),
	})
	text := charterText(tuiItem{name: "repos.verse", record: rec}, 100)
	for _, want := range []string{"on git.pr.*", "1 gates", "SCHEDULES · 1", "minute", "every 1m", "SENDS · 1", "install → git.verse.out"} {
		if !strings.Contains(text, want) {
			t.Errorf("the charter does not show %q:\n%s", want, text)
		}
	}
}
