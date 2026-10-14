package mirror

import (
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// A FRAME DOES NOT NAME ITS OWN AUTHOR, where a worker wrote it. A relayed task's frames say
// whatever the worker said, so a `by` on one is a name the worker chose: a decision by the
// answerer a gate was held out to, a slide by the docket. Who answered is what the answer route
// wrote when it accepted the answer, who slid is who the control route relayed it for, and
// where verse wrote neither, the worker of record did it.

func awaitFields(t *testing.T, store entity.Store, ref entity.Ref, why string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		e, _, _ := store.Get(ref)
		if f := entity.ToObj(e.Payload); ok(f) {
			return f
		}
		select {
		case <-deadline:
			e, _, _ := store.Get(ref)
			t.Fatalf("timed out waiting for %s; %s = %+v", why, ref, entity.ToObj(e.Payload))
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestAWorkersDecisionFrameDoesNotSayWhoAnsweredOrWhat(t *testing.T) {
	const worker, ana, boss = "runner:bot", "user:ana@example.com", "user:boss@example.com"
	for _, c := range []struct {
		what        string
		queued      string     // who the answer route accepted `proceed` from, "" where nobody was asked
		frame       task.Event // what the worker then reports
		by, verdict string
		ballots     []string
	}{
		{"an answer the route accepted from ana, reported under another name with another verdict", ana,
			task.Event{Kind: task.EventDecision, ID: "q1", Tool: "act", Proceed: false, Choice: "deny", By: boss},
			ana, "proceed", []string{ana}},
		{"a gate the worker settled with nobody asked", "",
			task.Event{Kind: task.EventDecision, ID: "q1", Tool: "act", Proceed: true, By: boss},
			worker, "proceed", nil},
		{"a gate the worker's runtime settled at its deadline", "",
			task.Event{Kind: task.EventDecision, ID: "q1", Tool: "act", Proceed: false, By: task.ByDeadline},
			task.ByDeadline, "deny", nil},
	} {
		t.Run(c.what, func(t *testing.T) {
			store := entitytest.Store(t)
			h := &fakeHandle{ch: make(chan task.Event)}
			Reflect(store, "t1", h, Meta{Runner: "http://runner.test", As: worker})
			h.ch <- task.Event{Kind: task.EventPending, ID: "q1", Gate: "write"}
			awaitFields(t, store, Ref("t1"), "the gate to appear", func(f map[string]any) bool { return f["pending"] != nil })
			if c.queued != "" {
				AnswerQuestion(store, "q1", task.Resolution{Proceed: true, By: c.queued})
			}
			h.ch <- c.frame
			awaitFields(t, store, Ref("t1"), "the gate to clear", func(f map[string]any) bool { return f["pending"] == nil })
			got := awaitFields(t, store, QuestionRef("q1"), "the question to settle", func(f map[string]any) bool {
				return f["status"] == QuestionAnswered && f["delivered"] == nil
			})
			if got["by"] != c.by || got["verdict"] != c.verdict {
				t.Fatalf("settled by %v as %v, want by %s as %s: %+v", got["by"], got["verdict"], c.by, c.verdict, got)
			}
			ballots, _ := got["ballots"].(map[string]any)
			if len(ballots) != len(c.ballots) {
				t.Fatalf("ballots %v, want %v", ballots, c.ballots)
			}
			for _, who := range c.ballots {
				if b, _ := ballots[who].(map[string]any); b == nil || b["choice"] != c.verdict {
					t.Fatalf("%s's ballot is %v, want %s", who, ballots[who], c.verdict)
				}
			}
		})
	}
}

// A decision frame names the question it decides, and only the one its task is waiting on is
// its to settle: another task's question, named by a worker, stays as it stood.
func TestAWorkersDecisionFrameSettlesOnlyItsOwnTasksQuestion(t *testing.T) {
	store := entitytest.Store(t)
	mine, theirs := &fakeHandle{ch: make(chan task.Event)}, &fakeHandle{ch: make(chan task.Event)}
	Reflect(store, "mine", mine, Meta{Runner: "http://runner.test", As: "runner:bot"})
	Reflect(store, "theirs", theirs, Meta{Runner: "http://runner.test", As: "runner:other"})
	mine.ch <- task.Event{Kind: task.EventPending, ID: "q-mine", Gate: "write"}
	theirs.ch <- task.Event{Kind: task.EventPending, ID: "q-theirs", Gate: "deploy"}
	awaitFields(t, store, Ref("mine"), "my gate", func(f map[string]any) bool { return f["pending"] != nil })
	awaitFields(t, store, QuestionRef("q-theirs"), "their question", func(f map[string]any) bool { return f["status"] == QuestionPending })

	mine.ch <- task.Event{Kind: task.EventDecision, ID: "q-theirs", Tool: "act", Proceed: true}
	mine.ch <- task.Event{Kind: task.EventExec, Tool: "after"} // a frame behind it, so the decision has been through
	awaitFields(t, store, Ref("mine"), "the frame behind it", func(f map[string]any) bool { return f["lastAction"] == "after" })
	if q, _, _ := store.Get(QuestionRef("q-theirs")); entity.Str(q.Payload, "status") != QuestionPending {
		t.Fatalf("another task's question was settled by my worker's frame: %+v", entity.ToObj(q.Payload))
	}
}

// THE ACKNOWLEDGEMENT CAN OUTRUN THE RECORD: the answer is delivered before it is written down,
// so a quick worker's frame settles the question first, as the worker's. The answer written
// after it is the answerer's, and the only ballot.
func TestAnAnswerRecordedAfterItsAcknowledgementIsTheAnswerers(t *testing.T) {
	const worker, ana = "runner:bot", "user:ana@example.com"
	store := entitytest.Store(t)
	h := &fakeHandle{ch: make(chan task.Event)}
	Reflect(store, "t1", h, Meta{Runner: "http://runner.test", As: worker})
	h.ch <- task.Event{Kind: task.EventPending, ID: "q1", Gate: "write"}
	awaitFields(t, store, Ref("t1"), "the gate", func(f map[string]any) bool { return f["pending"] != nil })
	h.ch <- task.Event{Kind: task.EventDecision, ID: "q1", Tool: "act", Proceed: true, By: ana}
	awaitFields(t, store, QuestionRef("q1"), "the echo to settle it", func(f map[string]any) bool { return f["status"] == QuestionAnswered })
	AnswerQuestion(store, "q1", task.Resolution{Proceed: true, By: ana})
	q, _, _ := store.Get(QuestionRef("q1"))
	got := entity.ToObj(q.Payload)
	ballots, _ := got["ballots"].(map[string]any)
	if got["by"] != ana || got["delivered"] != nil || len(ballots) != 1 || ballots[ana] == nil {
		t.Fatalf("the question is %+v; want by ana, delivered, and her ballot alone", got)
	}
}

func TestAWorkersVectorFrameDoesNotSayWhoSlidIt(t *testing.T) {
	const worker, ana = "runner:bot", "user:ana@example.com"
	store := entitytest.Store(t)
	h := &fakeHandle{ch: make(chan task.Event)}
	Reflect(store, "t1", h, Meta{Runner: "http://runner.test", As: worker, SlidBy: func() (string, string) { return ana, "ask" }})
	// The slide the control route relayed for ana: the runner reports it and the worker echoes
	// it, under whatever name, and both are her slide.
	for i, by := range []string{ana, entity.SystemDocket} {
		h.ch <- task.Event{Kind: task.EventVector, Mode: "ask", By: by}
		h.ch <- task.Event{Kind: task.EventExec, Tool: []string{"first", "second"}[i]}
		got := awaitFields(t, store, Ref("t1"), "the slide", func(f map[string]any) bool { return f["lastAction"] == []string{"first", "second"}[i] })
		if got["vector"] != "ask" || got["vectorBy"] != ana {
			t.Fatalf("after frame %d: vector %v by %v, want ask by who it was relayed for: %+v", i, got["vector"], got["vectorBy"], got)
		}
	}
	// A mode nobody relayed: the worker slid it, whatever it says.
	h.ch <- task.Event{Kind: task.EventVector, Mode: "auto", By: ana}
	got := awaitFields(t, store, Ref("t1"), "the second slide", func(f map[string]any) bool { return f["vector"] == "auto" })
	if got["vectorBy"] != worker {
		t.Fatalf("vectorBy = %v, want the worker of record: %+v", got["vectorBy"], got)
	}
}

// A hosted task's frames are made in this process, by routes that checked their actor.
func TestAHostedTasksFramesAreTrusted(t *testing.T) {
	const ana = "user:ana@example.com"
	store := entitytest.Store(t)
	h := &fakeHandle{ch: make(chan task.Event)}
	Reflect(store, "t1", h, Meta{Runner: LocalRunner})
	h.ch <- task.Event{Kind: task.EventVector, Mode: "ask", By: ana}
	got := awaitFields(t, store, Ref("t1"), "the slide", func(f map[string]any) bool { return f["vector"] == "ask" })
	if got["vectorBy"] != ana {
		t.Fatalf("vectorBy = %v, want %s", got["vectorBy"], ana)
	}
	h.ch <- task.Event{Kind: task.EventPending, ID: "q1", Gate: "write"}
	awaitFields(t, store, Ref("t1"), "the gate", func(f map[string]any) bool { return f["pending"] != nil })
	h.ch <- task.Event{Kind: task.EventDecision, ID: "q1", Proceed: true, By: ana}
	q := awaitFields(t, store, QuestionRef("q1"), "the question to settle", func(f map[string]any) bool { return f["status"] == QuestionAnswered })
	if q["by"] != ana {
		t.Fatalf("the question was settled by %v, want %s", q["by"], ana)
	}
}
