package main

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
)

// A ROUND READS WHAT IS DUE: runners and the Data section's listing always; the tasks and the
// questions when their watch is down, has counted a change no drawn read has, or tuiResync has
// passed; the charters while their section is open, else every tuiCharterEvery.
func TestTheTUIReadsWhatIsDue(t *testing.T) {
	m := loaded(t, twoQuestions())
	now := time.Now()
	if got := m.due(now); got != readAll {
		t.Fatalf("with no watch, a round reads %b, not every section", got)
	}
	m = watching(m, now)
	quiet := readsOf(tabRunners, tabData)
	m.watch.tasks.live.Store(false)
	m.watch.questions.live.Store(false)
	if got := m.due(now); got != quiet|readsOf(tabTasks, tabQuestions) {
		t.Fatalf("with the watches down, a round reads %b", got)
	}
	m.watch.tasks.live.Store(true)
	m.watch.questions.live.Store(true)
	if got := m.due(now); got != quiet {
		t.Fatalf("with the watches quiet, a round reads %b, not %b", got, quiet)
	}
	m.watch.tasks.gen.Add(1)
	if got := m.due(now); got != quiet|readsOf(tabTasks) {
		t.Fatalf("after a task changed, a round reads %b", got)
	}
	m = draw(t, m, m.loadOf(m.due(now)))
	if got := m.due(now); got != quiet {
		t.Fatalf("the change was read and drawn, and the next round still reads %b", got)
	}
	if got := m.due(now.Add(tuiResync + time.Second)); got != readAll {
		t.Fatalf("after tuiResync a round reads %b, not every section", got)
	}
	m.tab = tabCharters
	if got := m.due(now); got != quiet|readsOf(tabCharters) {
		t.Fatalf("with the charters open, a round reads %b", got)
	}
}

// A CHANGE IS READ UNTIL A READ OF IT IS DRAWN: a round dropped for a newer one that did not
// read the tasks, or a read of them that failed, leaves them due.
func TestTheTUIReadsAChangeUntilItIsDrawn(t *testing.T) {
	src := twoQuestions()
	src.paths = []string{"a", "a.b"}
	now := time.Now()
	m := watching(loaded(t, src), now)
	m.watch.tasks.gen.Add(1)
	tickRound := m.loadOf(m.due(now)) // reads the tasks
	next, goRound := m.dataGo("a", "")
	m = draw(t, next.(tuiModel), goRound) // the data only, asked later, drawn first
	m = draw(t, m, tickRound)             // dropped: older than what is drawn
	if !m.due(now).has(tabTasks) {
		t.Fatal("the round that read the change was dropped, and the tasks are no longer due")
	}
	src.fail = map[tuiTab]string{tabTasks: "502 bad gateway"}
	m = draw(t, m, m.loadOf(m.due(now)))
	if !m.due(now).has(tabTasks) {
		t.Fatal("the read of the change failed, and the tasks are no longer due")
	}
	src.fail = nil
	m = draw(t, m, m.loadOf(m.due(now)))
	if m.due(now).has(tabTasks) {
		t.Fatal("the change was read and drawn, and the tasks are still due")
	}
}

// THE SCREEN KEEPS READING whichever round is drawn first: the tick starts with the first round,
// not when it lands, so a round of the data going ahead of it does not leave the screen still.
func TestTheTUITicksFromTheStart(t *testing.T) {
	m := newTUI(twoQuestions(), "user:you@example.com", time.Millisecond)
	if !hasTick(m.Init()) {
		t.Fatal("Init starts no tick")
	}
}

// watching is m with both watches open and quiet, every section read at now.
func watching(m tuiModel, now time.Time) tuiModel {
	m.watch = &tuiWatch{}
	m.watch.tasks.live.Store(true)
	m.watch.questions.live.Store(true)
	for tb := range m.lastRead {
		m.lastRead[tb] = now
	}
	return m
}

// draw is m with the round cmd reads drawn on it (a record read with it is not).
func draw(t *testing.T, m tuiModel, cmd tea.Cmd) tuiModel {
	t.Helper()
	msg := cmd()
	if b, ok := msg.(tea.BatchMsg); ok {
		for _, c := range b {
			if s, ok := c().(snapMsg); ok {
				msg = s
			}
		}
	}
	next, _ := m.Update(msg.(snapMsg))
	return next.(tuiModel)
}

// hasTick is whether cmd, or one it batches, is the tick.
func hasTick(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tickMsg:
		return true
	case tea.BatchMsg:
		for _, c := range msg {
			if hasTick(c) {
				return true
			}
		}
	}
	return false
}

// A SECTION A ROUND DID NOT READ KEEPS ITS ROWS, and its last error, until it reads again.
func TestTheTUIKeepsWhatARoundDidNotRead(t *testing.T) {
	src := twoQuestions()
	m := loaded(t, src)
	failed := src.snapshot("", readAll)
	failed.errs = map[tuiTab]string{tabCharters: "403 no grant"}
	next, _ := m.Update(snapMsg{snap: failed, seq: askSeq.Add(1)})
	m = next.(tuiModel)
	partial := src.snapshot("", readsOf(tabRunners, tabData))
	partial.questions, partial.tasks = nil, nil
	next, _ = m.Update(snapMsg{snap: partial, seq: askSeq.Add(1)})
	m = next.(tuiModel)
	if len(m.snap.questions) != 2 || len(m.snap.tasks) == 0 {
		t.Fatalf("a round that did not read them left %d questions, %d tasks", len(m.snap.questions), len(m.snap.tasks))
	}
	if m.snap.errs[tabCharters] != "403 no grant" {
		t.Fatalf("the charters' last error is gone: %q", m.snap.errs)
	}
	if !m.lastRead[tabRunners].After(m.lastRead[tabTasks]) {
		t.Fatal("lastRead moved for a section the round did not read")
	}
}

// THE WATCH COUNTS A TASK'S CHANGE: a record written under mirror.task.instance is counted, once
// the watch is open.
func TestTheTUIWatchesTheTasks(t *testing.T) {
	store := serve(t)
	rootCmd()
	g.Addr = testAddr
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := startTUIWatch(ctx, apiClient())
	eventually(t, "the watches to open", func() bool { return w.tasks.live.Load() && w.questions.live.Load() })
	opened := w.tasks.gen.Load()
	if opened == 0 {
		t.Fatal("an opening is not counted as a change")
	}
	if _, _, err := store.Write(entity.Write{At: mirror.Ref("t1").At(), Value: entity.Obj{"status": "running"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the change to be counted", func() bool { return w.tasks.gen.Load() > opened })
	cancel()
	eventually(t, "the watch to close", func() bool { return !w.tasks.live.Load() })
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}
