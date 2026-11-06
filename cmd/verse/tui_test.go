package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
)

type fakeTUISource struct {
	snap     tuiSnapshot
	answered []string // id choice as say
	canceled []string
	paths    []string // every address the Data section can list
	records  map[string]*ir.Node
}

func (f *fakeTUISource) snapshot(at string) tuiSnapshot {
	s := f.snap
	var under []string
	for _, p := range f.paths {
		if parentOf(p) == at {
			under = append(under, p)
		}
	}
	s.dataAt, s.data = at, dataRows(at, under)
	return s
}
func (f *fakeTUISource) record(at string) (*ir.Node, error) {
	if r, ok := f.records[at]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("nothing stands at %s", at)
}
func (f *fakeTUISource) answer(id, choice, as, say string) (string, error) {
	f.answered = append(f.answered, strings.Join([]string{id, choice, as, say}, " | "))
	return "answered " + id, nil
}
func (f *fakeTUISource) cancel(id string) error { f.canceled = append(f.canceled, id); return nil }

// press sends keys to the model, and runs what each returns once, as the program would.
func press(t *testing.T, m tuiModel, keys ...string) tuiModel {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		next, cmd := m.Update(msg)
		m = next.(tuiModel)
		m = run(t, m, cmd)
	}
	return m
}

// run runs a command's message back through the model: an act, then the read after it.
func run(t *testing.T, m tuiModel, cmd tea.Cmd) tuiModel {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case recMsg:
		next, _ := m.Update(msg)
		m = next.(tuiModel)
	case tea.BatchMsg:
		for _, c := range msg {
			m = run(t, m, c)
		}
	case actedMsg, snapMsg:
		// What a read after the first returns is the content's read, run; the first's carries
		// the poll's tick, which is not.
		again := m.loaded
		next, more := m.Update(msg)
		m = next.(tuiModel)
		if _, isAct := msg.(actedMsg); isAct || again {
			m = run(t, m, more)
		}
	}
	return m
}

func loaded(t *testing.T, src *fakeTUISource) tuiModel {
	m := newTUI(src, "user:you@example.com", time.Second)
	next, _ := m.Update(snapMsg{snap: src.snapshot(""), seq: askSeq.Add(1)})
	return next.(tuiModel)
}

// longRecord is a record of 80 fields, longer than any pane.
func longRecord() *ir.Node {
	m := map[string]*ir.Node{}
	for i := range 80 {
		m[fmt.Sprintf("field%02d", i)] = ir.FromString("a value")
	}
	return ir.FromMap(m)
}

func twoQuestions() *fakeTUISource {
	return &fakeTUISource{snap: tuiSnapshot{errs: map[tuiTab]string{},
		questions: []tuiQuestion{
			{id: "q1", pose: "Merge the docs?", gate: "owner", options: []string{"proceed", "deny"}, subjectRecord: longRecord()},
			{id: "q2", pose: "Which provider?", options: []string{"proceed", "deny", "aws", "gcp"}, subjectRecord: longRecord()},
		},
		tasks: []tuiItem{
			{name: "pkg:work@a.step.one", state: "running   claude"},
			{name: "pkg:work@a.step.two", state: "done      claude"},
		}}}
}

// A YES GOES WITH WHATEVER IS SAID, OR NOTHING: `a` then enter proceeds as the caller.
func TestTheTUIProceedsAQuestion(t *testing.T) {
	src := twoQuestions()
	m := press(t, loaded(t, src), "a", "enter")
	if len(src.answered) != 1 || src.answered[0] != "q1 | proceed | user:you@example.com | " {
		t.Fatalf("answered %q", src.answered)
	}
	if !strings.Contains(m.status, "answered q1") {
		t.Errorf("status %q", m.status)
	}
}

// A NO SAYS WHY: enter with nothing typed answers nothing; the reason goes with the deny.
func TestTheTUIDeniesWithAReason(t *testing.T) {
	src := twoQuestions()
	m := press(t, loaded(t, src), "d", "enter")
	if len(src.answered) != 0 || m.mode != modeSay {
		t.Fatalf("a deny with no reason answered %q (mode %d)", src.answered, m.mode)
	}
	m = press(t, m, "n", "o", "t", " ", "y", "e", "t", "enter")
	if len(src.answered) != 1 || src.answered[0] != "q1 | deny | user:you@example.com | not yet" {
		t.Fatalf("answered %q", src.answered)
	}
	// esc goes back without answering.
	press(t, m, "d", "esc")
	if len(src.answered) != 1 {
		t.Fatalf("esc answered: %q", src.answered)
	}
}

// AN OPTION BEYOND THE TWO is chosen by its number: the third option is 7 (proceed 5, deny 6).
func TestTheTUIChoosesANamedOption(t *testing.T) {
	src := twoQuestions()
	press(t, loaded(t, src), "j", "8", "enter")
	if len(src.answered) != 1 || !strings.HasPrefix(src.answered[0], "q2 | gcp |") {
		t.Fatalf("answered %q", src.answered)
	}
}

// A CANCEL IS CONFIRMED, and only a task still running is offered one.
func TestTheTUICancelsARunningTaskOnceConfirmed(t *testing.T) {
	src := twoQuestions()
	m := press(t, loaded(t, src), "2", "x", "n")
	if len(src.canceled) != 0 {
		t.Fatalf("cancelled without a yes: %q", src.canceled)
	}
	m = press(t, m, "x", "y")
	if len(src.canceled) != 1 || src.canceled[0] != "pkg:work@a.step.one" {
		t.Fatalf("cancelled %q", src.canceled)
	}
	press(t, m, "j", "x", "y")
	if len(src.canceled) != 1 {
		t.Fatalf("a finished task was cancelled: %q", src.canceled)
	}
}

// THE SCREEN SAYS WHAT WAITS: the tab counts, the question, and the keys that answer it.
func TestTheTUIShowsWhatWaits(t *testing.T) {
	m := loaded(t, twoQuestions())
	m.width, m.height = 140, 30
	v := m.View()
	for _, want := range []string{"QUESTIONS", "2 need you", "TASKS", "1 run", "Merge the docs?", "a proceed", "d deny", "EXPLORER", "cmd ▸"} {
		if !strings.Contains(v, want) {
			t.Errorf("the screen does not say %q:\n%s", want, v)
		}
	}
}

// THE LISTINGS READ AS THE ROUTES ANSWER: tasks running first then the newest, projects by
// name with done over their stage.
func TestTheTUIListings(t *testing.T) {
	tasks, err := codec.ParseNode([]byte(`
- {id: "p:work@x.step.old", status: done, started: "2026-11-01T00:00:00Z", runnerName: verse.runners.claude}
- {id: "p:work@x.step.new", status: done, started: "2026-11-02T00:00:00Z", runnerName: verse.runners.claude}
- {id: "p:steward@x.project.sso", status: running, started: "2026-10-01T00:00:00Z", runnerName: verse.runners.claude}
`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	rows := taskRows(tasks, false)
	if len(rows) != 3 || rows[0].name != "p:steward@x.project.sso" || rows[1].name != "p:work@x.step.new" {
		t.Fatalf("task rows %+v", rows)
	}
	if got := taskShortName(rows[0].name); got != "steward@sso" {
		t.Errorf("short name %q", got)
	}
}

// THE HEADER STAYS ON THE FIRST LINE whatever a record holds: a subject of hundreds of long
// lines, at any terminal size, draws a frame no taller than the terminal.
func TestTheTUINeverOutgrowsTheTerminal(t *testing.T) {
	var big strings.Builder
	big.WriteString("{")
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&big, "k%d: %q, ", i, strings.Repeat("a long value that wraps ", 12))
	}
	big.WriteString("}")
	rec, err := codec.ParseNode([]byte(big.String()), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	src := twoQuestions()
	src.snap.questions[0].subjectRecord = rec
	src.snap.tasks[0].record = rec
	for _, size := range [][2]int{{80, 24}, {200, 60}, {320, 90}, {60, 12}} {
		m := loaded(t, src)
		m.width, m.height = size[0], size[1]
		for _, tab := range []string{"1", "2"} {
			m = press(t, m, tab)
			v := m.View()
			lines := strings.Split(v, "\n")
			if len(lines) > m.height {
				t.Errorf("%dx%d tab %s: %d lines", size[0], size[1], tab, len(lines))
			}
			if !strings.HasPrefix(lines[0], "verse") {
				t.Errorf("%dx%d tab %s: the first line is %q", size[0], size[1], tab, lines[0])
			}
			for i, l := range lines {
				if w := lipgloss.Width(l); w > m.width {
					t.Errorf("%dx%d tab %s: line %d is %d wide", size[0], size[1], tab, i, w)
					break
				}
			}
		}
	}
}

// THE PANES TAKE TURNS: → focuses the detail, where ↓ scrolls it and leaves the list's cursor
// alone; ← or esc goes back, where ↓ moves the cursor again.
func TestTheTUIMovesBetweenPanes(t *testing.T) {
	src := twoQuestions()
	m := press(t, loaded(t, src), "l", "j", "j")
	if m.focus != paneDetail || m.cursor[tabQuestions] != 0 || m.scroll != 2 {
		t.Fatalf("focus %d, cursor %d, scroll %d", m.focus, m.cursor[tabQuestions], m.scroll)
	}
	m = press(t, m, "h", "j")
	if m.focus != paneList || m.cursor[tabQuestions] != 1 || m.scroll != 0 {
		t.Fatalf("focus %d, cursor %d, scroll %d", m.focus, m.cursor[tabQuestions], m.scroll)
	}
	m = press(t, m, "enter", "esc")
	if m.focus != paneList {
		t.Fatalf("esc left focus on %d", m.focus)
	}
}

// A FILTER KEEPS THE ROWS THAT SAY IT, and what is answered is what the filter left selected.
func TestTheTUIFilters(t *testing.T) {
	src := twoQuestions()
	m := press(t, loaded(t, src), "/", "p", "r", "o", "v", "enter")
	if m.rows() != 1 || m.filter != "prov" {
		t.Fatalf("rows %d, filter %q", m.rows(), m.filter)
	}
	press(t, m, "a", "enter")
	if len(src.answered) != 1 || !strings.HasPrefix(src.answered[0], "q2 |") {
		t.Fatalf("answered %q", src.answered)
	}
	m = press(t, m, "esc")
	if m.filter != "" || m.rows() != 2 {
		t.Fatalf("esc kept the filter %q", m.filter)
	}
}

// A CLICK SELECTS THE ROW IT LANDS ON, and the wheel moves the pane under the pointer.
func TestTheTUITakesTheMouse(t *testing.T) {
	src := twoQuestions()
	m := loaded(t, src)
	m.width, m.height = 140, 30
	next, _ := m.Update(tea.MouseMsg{X: 3, Y: 4, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = next.(tuiModel)
	if m.cursor[tabQuestions] != 1 || m.focus != paneList {
		t.Fatalf("a click on the second row: cursor %d focus %d", m.cursor[tabQuestions], m.focus)
	}
	next, _ = m.Update(tea.MouseMsg{X: 100, Y: 10, Button: tea.MouseButtonWheelDown})
	m = next.(tuiModel)
	if m.focus != paneDetail || m.scroll != 3 {
		t.Fatalf("the wheel over the detail: focus %d scroll %d", m.focus, m.scroll)
	}
}

// THE COMMAND LINE TAKES WHAT THE KEYS DO, by name: `:` then a command, tab completing it, and
// `?` lists them.
func TestTheTUICommandLine(t *testing.T) {
	src := twoQuestions()
	src.paths = []string{"repos", "people", "repos.a"}
	src.records = map[string]*ir.Node{"repos": ir.FromMap(nil), "people": ir.FromMap(nil)}
	m := loaded(t, src)
	m.width, m.height = 140, 30
	m = press(t, m, ":", "c", "h", "tab")
	if got := m.cmd.Value(); got != "charters " {
		t.Fatalf("tab completed %q", got)
	}
	m = press(t, m, "esc", ":", "d", "a", "t", "a", " ", "r", "tab")
	if got := m.cmd.Value(); got != "data repos " {
		t.Fatalf("tab completed an address as %q", got)
	}
	m = press(t, m, "enter")
	m = run(t, m, m.load())
	if m.tab != tabData || m.dataAt != "repos" {
		t.Fatalf(":data repos: tab %v at %q", m.tab, m.dataAt)
	}
	m = press(t, m, ":", "v", "i", "e", "w", " ", "t", "i", "enter")
	if m.dview != viewTimeline {
		t.Fatalf(":view ti… is %v", m.dview)
	}
	m = press(t, m, ":", "t", "a", "s", "k", "s", "enter")
	if v := m.View(); m.tab != tabTasks || !m.onHeader || !strings.Contains(v, "[ TABLE ]") || !strings.Contains(v, "TASK") ||
		!strings.Contains(v, "work@one") {
		t.Fatalf(":tasks is not the tasks table:\n%s", v)
	}
	m = press(t, m, ":", "n", "o", "p", "e", "enter")
	if !strings.Contains(m.status, "no command nope") {
		t.Fatalf("an unknown command says %q", m.status)
	}
	m = press(t, m, "?")
	if v := m.View(); m.mode != modeHelp || !strings.Contains(v, "COMMANDS") || !strings.Contains(v, "answer <option>") {
		t.Fatalf("? is not the palette:\n%s", v)
	}
	m = press(t, m, "esc")
	if m.mode != modeBrowse {
		t.Fatalf("a key did not close the palette")
	}
}

// THE EXPLORER IS A TREE: each section's line, its rows under it; ← on a row goes to its
// section's line, which shows the section as a table, and ⏎ there folds its rows.
func TestTheTUIExplorer(t *testing.T) {
	src := twoQuestions()
	m := loaded(t, src)
	m.width, m.height = 140, 30
	if m.tab != tabQuestions || m.onHeader || m.cursor[tabQuestions] != 0 {
		t.Fatalf("it does not open on the first question: tab %v header %v", m.tab, m.onHeader)
	}
	m = press(t, m, "h")
	if v := m.View(); !m.onHeader || !strings.Contains(v, "[ TABLE ]") || !strings.Contains(v, "Which provider?") {
		t.Fatalf("← did not go to the questions' table:\n%s", v)
	}
	m = press(t, m, "enter")
	if v := m.View(); !m.folded[tabQuestions] || strings.Contains(strings.Split(v, "TASKS")[0], "┣━") {
		t.Fatalf("⏎ did not fold the questions:\n%s", v)
	}
	m = press(t, m, "j")
	if m.tab != tabTasks || !m.onHeader {
		t.Fatalf("↓ from a folded section is the next section: tab %v header %v", m.tab, m.onHeader)
	}
}

// A CONTENT PANE THAT HOLDS MORE THAN IT SHOWS SAYS WHERE IT IS.
func TestTheTUISaysWhereItIs(t *testing.T) {
	src := twoQuestions()
	rec, _ := codec.ParseNode([]byte(`{a: 1, b: 2, c: 3, d: 4, e: 5, f: 6, g: 7, h: 8, i: 9, j: 10, k: 11, l: 12, m: 13, n: 14}`), codec.Tony)
	src.snap.questions[0].subjectRecord = rec
	m := loaded(t, src)
	m.width, m.height = 100, 16
	if v := m.View(); !strings.Contains(v, "scroll ↑/↓") || !strings.Contains(v, "1-9 of") {
		t.Fatalf("no scroll position:\n%s", v)
	}
	m = press(t, m, "J")
	if v := m.View(); strings.Contains(v, "1-9 of") {
		t.Fatalf("J did not scroll:\n%s", v)
	}
}

// THE DATA IS BROWSED AN ADDRESS AT A TIME, knowing no charter: ⏎ opens a place into what is
// under it, u comes back up onto the place it left, and the row under the cursor shows its
// record and what derived it.
func TestTheTUIBrowsesTheData(t *testing.T) {
	derived, err := codec.ParseNode([]byte("verdict: approve\nverse:\n  meta:\n    derivedFrom:\n      rule: review\n      subject: repos.a.pr.7\n"), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeTUISource{snap: tuiSnapshot{errs: map[tuiTab]string{}},
		paths:   []string{"repos", "people", "repos.a", "repos.a.pr", "repos.a.review", "repos.a.review.7"},
		records: map[string]*ir.Node{"repos.a.review.7": derived, "repos": ir.FromMap(map[string]*ir.Node{})}}
	m := newTUI(src, "user:you@example.com", time.Second)
	m = run(t, m, m.Init())
	m.width, m.height = 140, 30
	m = press(t, m, "3")
	if m.tab != tabData || len(m.items()) != 2 || m.items()[0].name != "people" {
		t.Fatalf("the root lists %+v", m.items())
	}
	for _, k := range []string{"j", "enter", "enter", "j"} { // repos → a → review
		m = press(t, m, k)
		if k == "enter" {
			m = run(t, m, m.load())
		}
	}
	if m.dataAt != "repos.a" {
		t.Fatalf("data at %q", m.dataAt)
	}
	m = press(t, m, "enter")
	m = run(t, m, m.load())
	m = run(t, m, m.loadRecord())
	v := m.View()
	for _, want := range []string{"DATA repos.a.review", "repos.a.review.7", "by rule review from repos.a.pr.7", "verdict"} {
		if !strings.Contains(v, want) {
			t.Errorf("the place does not show %q:\n%s", want, v)
		}
	}
	m = press(t, m, "u")
	m = run(t, m, m.load())
	if it, _ := m.item(); m.dataAt != "repos.a" || it.note != "repos.a.review" {
		t.Fatalf("u came up to %q, on %q", m.dataAt, it.note)
	}
	m = press(t, m, ":")
	for _, r := range "data people" {
		m = press(t, m, string(r))
	}
	m = press(t, m, "enter")
	m = run(t, m, m.load())
	if m.dataAt != "people" || m.tab != tabData {
		t.Fatalf(":data people went to %q", m.dataAt)
	}
}

// THE CHARTERS ARE THE TREE'S PACKAGES: each address one is installed at, a place only holding
// others being none.
func TestCharterRows(t *testing.T) {
	n, err := codec.ParseNode([]byte(`writes: anywhere
rules: []
strata: []
children:
  repos:
    children:
      x:
        writes: repos.x
        rules:
        - name: review
          action: {task: {}}
        - name: merge
          action: {task: {}}
        strata: []
        ttl: 24h
`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	rows := charterRows(n)
	if len(rows) != 2 || rows[0].name != "(root)" || rows[1].name != "repos.x" || rows[1].state != "2 rules · ttl 24h" {
		t.Fatalf("rows %+v", rows)
	}
	if strings.Contains(recordText(rows[0].record), "children") {
		t.Error("a package's record carries the packages under it")
	}
	text := charterText(rows[1], 80)
	for _, want := range []string{"RULES · 2", "review", "task"} {
		if !strings.Contains(text, want) {
			t.Errorf("the charter does not show %q:\n%s", want, text)
		}
	}
}

// THE SOURCE READS A REAL VERSE: an address's children by a listing, the root's included.
func TestTheTUISourceListsTheData(t *testing.T) {
	store := serve(t)
	for _, p := range [][]string{{"demo", "drift", "range", "abc"}, {"demo", "drift", "x"}, {"other", "y"}} {
		if _, _, err := store.Write(entity.Write{At: entity.RefAt(p...).At(), Value: entity.Obj{"to": "abc"}.Payload()}); err != nil {
			t.Fatal(err)
		}
	}
	rootCmd()
	g.Addr = testAddr
	h := &httpSource{c: apiClient()}
	s := h.snapshot("demo.drift")
	if e := s.errs[tabData]; e != "" {
		t.Fatal(e)
	}
	var got []string
	for _, d := range s.data {
		got = append(got, d.name+"="+d.note)
	}
	if strings.Join(got, " ") != "range=demo.drift.range x=demo.drift.x" {
		t.Errorf("demo.drift lists %q", got)
	}
	if root := h.snapshot(""); len(root.data) != 2 || root.data[0].note != "demo" {
		t.Errorf("the root lists %+v", root.data)
	}
	if len(s.charters) == 0 {
		t.Errorf("no charters: %v", s.errs[tabCharters])
	}
	rec, err := h.record("demo.drift.x")
	if err != nil || entity.Str(rec, "to") != "abc" {
		t.Errorf("record: %v %v", rec, err)
	}
}

// A CUT IS BY WHAT SHOWS: a coloured string is cut at its visible width, escapes and all kept
// whole, so a coloured row is not cut to nothing.
func TestCutCountsCellsNotEscapes(t *testing.T) {
	s := "\x1b[33mowner\x1b[0m Merge the docs?"
	if got := ansiStrip(cut(s, 12)); got != "owner Merge…" {
		t.Errorf("cut = %q", got)
	}
	if cut(s, 40) != s {
		t.Error("a string that fits was changed")
	}
	if got := ruleDoes(ir.FromMap(map[string]*ir.Node{"onDone": ir.FromMap(nil), "spawn": ir.FromMap(nil), "patch": ir.FromMap(nil)})); got != "spawn" {
		t.Errorf("ruleDoes = %q", got)
	}
}

func ansiStrip(s string) string { return ansi.Strip(s) }

// A GATE'S QUESTION IS POSED BY ITS RULE: it carries no pose, so the screen asks whether the
// rule may act on its subject rather than showing a blank.
func TestAGateQuestionIsPosedByItsRule(t *testing.T) {
	p := ir.FromMap(map[string]*ir.Node{
		"gate": ir.FromString("on-call"), "subject": ir.FromString("acme.bug.inc4211"), "reason": ir.FromString("trigger hotfix"),
		"from": ir.FromMap(map[string]*ir.Node{"at": ir.FromString("acme"), "rule": ir.FromString("hotfix")}),
	})
	if q := questionOf("q", p); q.pose != "Let rule hotfix act on acme.bug.inc4211?" {
		t.Errorf("pose %q", q.pose)
	}
}

// A LONG ROW STAYS ONE LINE: selected or not, the explorer cuts it to its width.
func TestTheExplorerNeverWrapsARow(t *testing.T) {
	src := twoQuestions()
	src.snap.questions[0].pose = strings.Repeat("Let rule hotfix act on acme.bug.inc4211? ", 3)
	m := loaded(t, src)
	m.width, m.height = 120, 30
	exW, _, h := m.layout()
	for _, l := range m.explorerLines(exW, h) {
		if lipgloss.Width(l) > exW || strings.Contains(l, "\n") {
			t.Fatalf("a row overruns the explorer (%d > %d): %q", lipgloss.Width(l), exW, l)
		}
	}
}

// A RECORD'S CONTROL CHARACTERS NEVER REACH THE SCREEN: a PR body's CRLF, a script's tabs, a
// progress bar's carriage returns and a log's cursor movements each moved the terminal's cursor
// without the layout counting it, and a row scrolled into view lost its explorer and its rule.
// Scrolled through, every row of the body keeps its rule and fits the terminal.
func TestTheTUIDrawsARecordsControlCharactersSafely(t *testing.T) {
	// As the verse sends it: a block, its tabs and carriage returns raw.
	doc := "\ntitle: \"PR 99\"\nbody: |-\n"
	for i := range 40 {
		doc += fmt.Sprintf("  line %d:\tfunc main() {\tfmt.Println(\"hi\")\t}\r\n", i)
	}
	rec, err := codec.ParseNode([]byte(doc), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recordText(rec), "\r") {
		t.Fatalf("the fixture's record does not draw its carriage returns raw: %q", recordText(rec)[:80])
	}
	src := &fakeTUISource{snap: tuiSnapshot{errs: map[tuiTab]string{}}, paths: []string{"acme", "acme.pr"},
		records: map[string]*ir.Node{"acme.pr": rec}}
	m := newTUI(src, "user:you@example.com", time.Second)
	m.dataAt = "acme"
	m = run(t, m, m.Init())
	m.width, m.height = 110, 24
	m = press(t, m, "3")
	m = run(t, m, m.loadRecord())
	m = press(t, m, "l")
	for step := range 6 {
		v := m.View()
		lines := strings.Split(v, "\n")
		for i, l := range lines[2 : len(lines)-3] {
			if !strings.Contains(l, "┃") {
				t.Fatalf("scroll %d, row %d lost its rule: %q", step, i, l)
			}
		}
		for _, l := range lines {
			if strings.ContainsAny(ansi.Strip(l), "\r\t\x07") || strings.Contains(l, "\x1b[1A") || strings.Contains(l, "\x1b[2K") {
				t.Fatalf("a control character reached the screen: %q", l)
			}
			if lipgloss.Width(l) > m.width {
				t.Fatalf("a row overruns the terminal: %d > %d: %q", lipgloss.Width(l), m.width, l)
			}
		}
		if step > 0 {
			// what scrolled into view is the body's lines, each drawn
			n := strings.Count(ansi.Strip(v), "func main()")
			if n < 10 {
				t.Fatalf("scroll %d drew %d of the body's lines:\n%s", step, n, ansi.Strip(v))
			}
		}
		m = press(t, m, "J")
	}
}

func TestScreenSafe(t *testing.T) {
	for in, want := range map[string]string{
		"a\tb":                      "a   b",
		"ab\tc":                     "ab  c",
		"x\r\ny":                    "x\ny",
		"50%\r100%":                 "100%",
		"done\r":                    "done",
		"\x1b[33mline\r\x1b[0m":     "\x1b[33mline\x1b[0m",
		"\x1b[31mred\x1b[0m":        "\x1b[31mred\x1b[0m",
		"up\x1b[1Aclear\x1b[2K":     "upclear",
		"bell\x07 osc\x1b]0;t\x07!": "bell osc!",
		"plain":                     "plain",
	} {
		if got := screenSafe(in); got != want {
			t.Errorf("screenSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
