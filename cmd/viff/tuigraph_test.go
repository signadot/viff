package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/codec"
)

// graphCharter is a charter of no kind the tui knows: a rule deriving from a place outside it,
// one starting a task on what that derives, and one gated that derives further and loops back.
func graphCharter(t *testing.T) *ir.Node {
	t.Helper()
	n, err := codec.ParseNode([]byte(`
writes: shop.orders
sends:
- {from: out.mail, to: mail.out}
rules:
- name: ship
  condition: {subject: {path: "shop.orders.order.*"}}
  gates:
  - {name: clerk}
  action:
    derive:
    - {id: x, path: shop.orders.out.mail}
    - {id: y, path: shop.orders.order}
- name: pack
  condition: {subject: {path: "shop.orders.order.*"}}
  action: {spawn: {agent: {name: viff}}}
- name: take
  condition: {subject: {path: "till.sale.*"}}
  action:
    derive: {id: z, path: shop.orders.order}
`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func graphTask(id, status string, ago time.Duration) tuiItem {
	started := time.Now().Add(-ago).UTC().Format(time.RFC3339)
	rec := ir.FromMap(map[string]*ir.Node{"id": ir.FromString(id), "status": ir.FromString(status),
		"started": ir.FromString(started), "runnerName": ir.FromString("mirror.runners.claude")})
	if isTerminalStatus(status) {
		rec = ir.FromMap(map[string]*ir.Node{"id": ir.FromString(id), "status": ir.FromString(status),
			"started": ir.FromString(started), "ended": ir.FromString(time.Now().Add(-ago / 2).UTC().Format(time.RFC3339))})
	}
	return tuiItem{name: id, state: status + " claude", note: started, record: rec}
}

func graphModel(t *testing.T) tuiModel {
	t.Helper()
	tasks := []tuiItem{
		graphTask("shop.orders:pack@shop.orders.order.o7", "running", 2*time.Minute),
		graphTask("shop.orders:pack@shop.orders.order.o6", "done", 10*time.Minute),
		graphTask("shop.orders:pack@shop.orders.order.o5", "failed", 3*time.Hour),
		graphTask("gone.charter:sweep@gone.thing.a", "running", time.Minute),
		graphTask("old.charter:tidy@old.thing.a", "done", 5*time.Hour),
		graphTask("demo-count-06b4e350a44d", "running", 30*time.Second),
	}
	src := &fakeTUISource{snap: tuiSnapshot{
		tasks: tasks, everyTask: tasks,
		charters: []tuiItem{{name: "shop.orders", state: "3 rules", note: "shop.orders", record: graphCharter(t)}},
		questions: []tuiQuestion{
			{id: "q1", pose: "Ship order o7?", gate: "clerk", subject: "shop.orders.order.o7", options: []string{"proceed", "deny"}},
			{id: "q2", pose: "Lunch?", options: []string{"proceed", "deny"}},
		},
	}}
	m := newTUI(src, "user:ana@example.com", time.Hour)
	m.width, m.height = 140, 60
	return run(t, m, m.load())
}

// The graph is drawn from what any charter says of itself: each rule under the place it
// watches, what it derives into under it, and the work named after it on its line.
func TestTheTUIDrawsACharterAsAGraph(t *testing.T) {
	m := graphModel(t)
	next, _ := m.run("charters")
	m = press(t, next.(tuiModel), "j", "tab")
	if m.shownView() != viewGraph {
		t.Fatalf("tab on a charter shows its %s, want its graph", m.shownView())
	}
	text, _ := m.contentText(120)
	lines := strings.Split(ansi.Strip(text), "\n")
	at := func(want string) int {
		for i, l := range lines {
			if strings.Contains(l, want) {
				return i
			}
		}
		t.Fatalf("the graph has no line holding %q:\n%s", want, strings.Join(lines, "\n"))
		return -1
	}
	// The place outside the charter is the root; the rule on it feeds the place the others watch.
	root, take, order := at("till.sale.*"), at("─▶ take"), at("….order.*")
	if !(root < take && take < order && order < at("─▶ ship") && order < at("─▶ pack")) {
		t.Errorf("the rules are not under the places they watch:\n%s", strings.Join(lines, "\n"))
	}
	for _, want := range []string{"1 running", "✓ 1", "✗ 1", "o7", "gate clerk", "1 waiting", "Ship order o7?", "….out.mail.*  ↗ sent to mail.out"} {
		at(want)
	}
	// A rule deriving into the place it watches does not draw that place again under itself.
	if n := strings.Count(strings.Join(lines, "\n"), "….order.*"); n != 1 {
		t.Errorf("the place the rules watch is drawn %d times, want once", n)
	}
}

// The Tasks section's own line has the graph of everything in flight: each charter at work as
// its graph, a charter the tui cannot read by its rules' names, the tasks run by hand, the
// questions no gate posed, and the charters whose work has ended.
func TestTheTUIGraphsEverythingInFlight(t *testing.T) {
	m := graphModel(t)
	next, _ := m.run("graph")
	m = next.(tuiModel)
	if m.tab != tabTasks || !m.onHeader || m.shownView() != viewGraph {
		t.Fatalf(":graph left the cursor on %s (header %v) showing %s", m.tab, m.onHeader, m.shownView())
	}
	text, _ := m.contentText(120)
	out := ansi.Strip(text)
	for _, want := range []string{"3 tasks running · 2 waiting on you · 2 charters at work",
		"shop.orders · 1 running · 1 waiting", "─▶ pack", "gone.charter · 1 running · 0 waiting", "sweep",
		"run by hand · 1 running", "demo-count-06b4e350a44d", "asked, not by a rule's gate · 1", "Lunch?",
		"ended · 1 charters", "old.charter", "tidy ✓ 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("the graph of what is in flight does not say %q:\n%s", want, out)
		}
	}
	// tab goes on to the timeline of what runs and what just ended, then back to the table.
	m = press(t, m, "tab")
	text, _ = m.contentText(120)
	out = ansi.Strip(text)
	if m.shownView() != viewTimeline || !strings.Contains(out, "4 tasks over") || strings.Contains(out, "tidy") || strings.Contains(out, "o5") {
		t.Errorf("the tasks' timeline (view %s) holds what runs and what ended lately, no more:\n%s", m.shownView(), out)
	}
	m = press(t, m, "tab")
	if text, _ = m.contentText(120); m.shownView() != viewRecord || !strings.Contains(ansi.Strip(text), "TASK") {
		t.Errorf("a third tab is back on the table, got view %s", m.shownView())
	}
	if got := ansi.Strip(m.tabsLine()); !strings.Contains(got, "[ TABLE ]") || !strings.Contains(got, "GRAPH") {
		t.Errorf("the section's views are named table, graph and timeline: %q", got)
	}
}

func TestTheTUIGraphSaysWhenNothingRuns(t *testing.T) {
	src := &fakeTUISource{}
	m := newTUI(src, "user:ana@example.com", time.Hour)
	m = run(t, m, m.load())
	next, _ := m.run("graph")
	text, _ := next.(tuiModel).contentText(100)
	if out := ansi.Strip(text); !strings.Contains(out, "nothing is running") {
		t.Errorf("an idle viff's graph says so:\n%s", out)
	}
}

func TestTheTUIReadsATaskIDAsItsCharterAndRule(t *testing.T) {
	for id, want := range map[string][3]string{
		"shop.orders:pack@shop.orders.order.o7": {"shop.orders", "pack", "shop.orders.order.o7"},
		"me::shop.orders:pack@x.y":              {"shop.orders", "pack", "x.y"},
		"run-0492a02316f8":                      {},
		"demo-count-06b4e350a44d":               {},
	} {
		c, r, s, ok := taskOf(id)
		if got := [3]string{c, r, s}; got != want || ok != (want[0] != "") {
			t.Errorf("taskOf(%q) = %v %v, want %v", id, got, ok, want)
		}
	}
	if !pathMatches("shop.orders.order.*", "shop.orders.order.o7") || pathMatches("shop.orders.order.*", "shop.orders.order.o7.x") ||
		pathMatches("shop.orders.order.*", "shop.orders.other.o7") {
		t.Error("a subject path names the addresses one segment under it, a * for any one")
	}
}
