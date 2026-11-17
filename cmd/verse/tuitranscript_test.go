package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/signadot/verse/pkg/task"
)

// onTask is the model with the cursor on the first task's row.
func onTask(t *testing.T, src *fakeTUISource) tuiModel {
	t.Helper()
	m := loaded(t, src)
	m.tab, m.onHeader = tabTasks, false
	m.cursor[tabTasks] = 0
	return m
}

func counting() *fakeTUISource {
	src := twoQuestions()
	src.events = map[string][]task.Event{"pkg:work@a.step.one": {
		{Kind: task.EventBoundary, Seq: 1},
		{Kind: task.EventDecision, Action: "sleep 3 && echo 1", By: "auto", Gate: "autonomy[0]", Proceed: true},
		{Kind: task.EventExec, Text: "exit: 0\nstdout:\n1"},
		{Kind: task.EventSay, Text: "one, so far"},
		{Kind: task.EventSay, By: "user:you@example.com", Text: "which number?"},
		{Kind: task.EventPending, Text: "May it push?"},
		{Kind: task.EventStatus, Text: "done"},
	}}
	return src
}

// A TASK HAS TWO VIEWS, its record and its transcript: tab goes to the transcript, which opens the
// task's stream and draws what it says, an action with what came of it, the agent's words and a
// person's, a question it stops on, and how it ended.
func TestTheTUIAttachesToATask(t *testing.T) {
	src := counting()
	m := press(t, onTask(t, src), "tab")
	if m.dview != viewTranscript || len(src.attached) != 1 || src.attached[0] != "pkg:work@a.step.one" {
		t.Fatalf("view %v, attached %q", m.dview, src.attached)
	}
	v := ansi.Strip(m.View())
	for _, want := range []string{"[ TRANSCRIPT ]", "▸ sleep 3 && echo 1", "auto · autonomy[0]", "stdout:", "one, so far",
		"user:you@example.com: which number?", "waiting on an answer: May it push?", "done"} {
		if !strings.Contains(v, want) {
			t.Errorf("the transcript does not show %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "following") || m.tr.ended != "done" {
		t.Errorf("a task that ended is still followed (ended %q):\n%s", m.tr.ended, v)
	}
	// Back on the record, the stream is closed, and the record is what shows.
	m = press(t, m, "tab")
	if m.dview != viewRecord || m.tr.id != "" || strings.Contains(ansi.Strip(m.View()), "[ TRANSCRIPT ]") {
		t.Errorf("view %v, transcript of %q", m.dview, m.tr.id)
	}
}

// :attach is the same view by name, from the record, and only on a task.
func TestTheTUIAttachCommand(t *testing.T) {
	src := counting()
	m := press(t, onTask(t, src), ":", "attach", "enter")
	if m.dview != viewTranscript || len(src.attached) != 1 || !strings.Contains(ansi.Strip(m.View()), "one, so far") {
		t.Fatalf("view %v, attached %q", m.dview, src.attached)
	}
	// On another row it is that row's task that is attached to, and the first one's lines go.
	m = press(t, m, "h", "j")
	if m.tr.id != "pkg:work@a.step.two" || len(m.tr.lines) != 0 || len(src.attached) != 2 {
		t.Errorf("transcript of %q with %d lines, attached %q", m.tr.id, len(m.tr.lines), src.attached)
	}
	q := press(t, loaded(t, src), ":", "attach", "enter")
	if q.dview == viewTranscript || !strings.Contains(q.status, "attach is to a task") {
		t.Errorf("attach on a question: view %v, status %q", q.dview, q.status)
	}
}

// A stream the verse refuses is said in the pane, not left as an empty transcript.
func TestTheTUISaysWhyATranscriptDidNotOpen(t *testing.T) {
	src := counting()
	src.streamErr = errors.New("this task has no transcript to attach to")
	m := press(t, onTask(t, src), "tab")
	if v := ansi.Strip(m.View()); !strings.Contains(v, "✗ this task has no transcript to attach to") {
		t.Errorf("the refusal is not shown:\n%s", v)
	}
}

// A place's views are not a task's: coming to the data from a transcript shows the record.
func TestTheTUIKeepsAViewTheRowHas(t *testing.T) {
	src := counting()
	src.paths = []string{"repos"}
	m := press(t, onTask(t, src), "tab")
	m.tab, m.onHeader = tabData, false
	m = press(t, m, "r")
	if m.dview != viewRecord || m.tr.id != "" {
		t.Errorf("on a place: view %v, transcript of %q", m.dview, m.tr.id)
	}
}
