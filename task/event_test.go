package task

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/signadot/verse/systems/systemstest"
)

// A run emits the loop-level events to a subscriber.
func TestEventBusRun(t *testing.T) {
	d := &scriptDriver{steps: []ToolUse{tool("a"), tool("b")}}
	p := NewTask("t", toolSup(allowAll(), AutoResolver{}), d, &recExec{})
	ch, unsub := p.Subscribe()
	defer unsub()

	seenCh := make(chan map[string]int, 1)
	go func() {
		seen := map[string]int{}
		for ev := range ch {
			seen[ev.Kind]++
			if ev.Kind == EventStatus { // run ended
				break
			}
		}
		seenCh <- seen
	}()

	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	select {
	case seen := <-seenCh:
		if seen[EventBoundary] == 0 || seen[EventExec] == 0 || seen[EventStatus] == 0 {
			t.Fatalf("missing expected events: %v", seen)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for events")
	}
}

// A subscriber that can't keep up drops events and gets a gap marker rather than
// blocking the loop.
func TestEventBusGapMarker(t *testing.T) {
	p := NewTask("t", toolSup(allowAll(), AutoResolver{}), &scriptDriver{}, &recExec{})
	ch, unsub := p.Subscribe()
	defer unsub()

	for i := 0; i < 300; i++ { // overflow the 256 buffer without draining → drops accrue
		p.emit(Event{Kind: EventBoundary, Seq: i})
	}
	for i := 0; i < 10; i++ { // free some slots, then emit → gap marker flushes
		<-ch
	}
	p.emit(Event{Kind: EventBoundary, Seq: 999})

	sawGap, dropped := false, 0
	for {
		select {
		case ev := <-ch:
			if ev.Kind == EventGap {
				sawGap, dropped = true, ev.Dropped
			}
		default:
			if !sawGap || dropped == 0 {
				t.Fatalf("expected a gap marker with dropped>0, got gap=%v dropped=%d", sawGap, dropped)
			}
			return
		}
	}
}

// EACH CALL'S EVENTS NAME THE CALL: the decision says which row decided it, and the exec that
// follows names the same step and params with its outcome, so a reader of the stream can tell
// what was called, under which row, and what came of it without matching tools to steps. A
// call the policy denies has a decision and no exec; an action that is no step names none.
func TestEachCallsEventsNameTheCall(t *testing.T) {
	sup := systemstest.Opened(NewSupervisor(anyEnv(), Autonomy{
		{Name: "no-deploy", Match: on("commands", "run", "command", "deploy"), Then: Then{Mode: ThenDeny}},
		{Then: Then{Mode: ThenAllow}},
	}, AutoResolver{}), anyEnv())
	sup.SetStepOf(func(tu ToolUse) (Step, bool) {
		if tu.Name == "read" {
			return Step{}, false
		}
		return Step{Name: StepCommandsRun, Fields: map[string]string{"command": tu.Name}}, true
	})
	p := NewTask("t", sup, &scriptDriver{steps: []ToolUse{tool("build"), tool("deploy"), tool("read")}}, &recExec{})
	ch, unsub := p.Subscribe()
	defer unsub()
	got := make(chan []Event, 1)
	go func() {
		var evs []Event
		for ev := range ch {
			if ev.Kind == EventDecision || ev.Kind == EventExec {
				evs = append(evs, ev)
			}
			if ev.Kind == EventStatus && Status(ev.Text).Terminal() {
				break
			}
		}
		got <- evs
	}()
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var evs []Event
	select {
	case evs = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for events")
	}
	type seen struct {
		kind, tool, gate, step, command string
		proceed                         bool
	}
	var have []seen
	for _, ev := range evs {
		have = append(have, seen{ev.Kind, ev.Tool, ev.Gate, ev.Step, ev.Fields["command"], ev.Proceed})
	}
	want := []seen{
		{EventDecision, "build", "autonomy[1]", StepCommandsRun, "build", true},
		{EventExec, "build", "", StepCommandsRun, "build", false},
		{EventDecision, "deploy", "no-deploy", StepCommandsRun, "deploy", false},
		{EventDecision, "read", "", "", "", true},
		{EventExec, "read", "", "", "", false},
	}
	if !slices.Equal(have, want) {
		t.Fatalf("events:\n%v\nwant:\n%v", have, want)
	}
}
