package main

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/mirror"
)

// A ROUND READS WHAT IS DUE: runners and the Data section's listing always; the tasks and the
// questions when their watch is down, says they changed, or tuiResync has passed; the charters
// while their section is open, else every tuiCharterEvery.
func TestTheTUIReadsWhatIsDue(t *testing.T) {
	m := loaded(t, twoQuestions())
	now := time.Now()
	if got := m.due(now); got != readAll {
		t.Fatalf("with no watch, a round reads %b, not every section", got)
	}
	m.watch = &tuiWatch{}
	m.tab = tabTasks
	for tb := range m.lastRead {
		m.lastRead[tb] = now
	}
	quiet := readsOf(tabRunners, tabData)
	if got := m.due(now); got != readsOf(tabRunners, tabData, tabTasks, tabQuestions) {
		t.Fatalf("with the watches down, a round reads %b", got)
	}
	m.watch.tasks.live.Store(true)
	m.watch.questions.live.Store(true)
	if got := m.due(now); got != quiet {
		t.Fatalf("with the watches quiet, a round reads %b, not %b", got, quiet)
	}
	m.watch.tasks.dirty.Store(true)
	if got := m.due(now); got != quiet|readsOf(tabTasks) {
		t.Fatalf("after a task changed, a round reads %b", got)
	}
	if got := m.due(now); got != quiet {
		t.Fatalf("the change was read, and the next round still reads %b", got)
	}
	if got := m.due(now.Add(tuiResync)); got != readAll {
		t.Fatalf("after tuiResync a round reads %b, not every section", got)
	}
	m.tab = tabCharters
	if got := m.due(now); got != quiet|readsOf(tabCharters) {
		t.Fatalf("with the charters open, a round reads %b", got)
	}
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

// THE WATCH SAYS A TASK CHANGED: a record written under verse.task.instance marks the tasks to
// be read, once the watch is open.
func TestTheTUIWatchesTheTasks(t *testing.T) {
	store := serve(t)
	rootCmd()
	g.Addr = testAddr
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := startTUIWatch(ctx, apiClient())
	eventually(t, "the watches to open", func() bool { return w.tasks.live.Load() && w.questions.live.Load() })
	w.tasks.due()
	if w.tasks.due() {
		t.Fatal("the tasks are due with nothing changed")
	}
	if _, _, err := store.Write(entity.Write{At: mirror.Ref("t1").At(), Value: entity.Obj{"status": "running"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the change to be said", func() bool { return w.tasks.dirty.Load() })
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
