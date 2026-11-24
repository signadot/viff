package mirror

import (
	"sync"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
)

// A RUNNER'S QUESTION IS POSED UNDER THE CHARTER'S CLOCK. The runner reads a gate's duration and
// nothing else, and a runner that is not runnerd may send none: the question still stands as
// long as the charter said, settles on what it said, and says about itself what the gate says.
func TestARunnersQuestionIsPosedUnderTheChartersClock(t *testing.T) {
	store := entitytest.Store(t)
	var mu sync.Mutex
	armed := map[string]string{}
	h := &fakeHandle{ch: make(chan task.Event)}
	Reflect(store, "t1", h, Meta{Runner: "http://runner.test", As: "runner:bot",
		Autonomy: task.Autonomy{{Name: "release", Match: &task.Match{System: "git", Verb: "push"}, Then: task.Then{Mode: task.ThenAsk, Ask: &task.Ask{Within: "4h", Default: task.Deny, Labels: map[string]string{"slack": "C1"}}}}, {Then: task.Then{Mode: task.ThenAsk}}},
		Arm:      func(id, within string) { mu.Lock(); armed[id] = within; mu.Unlock() },
	})
	h.ch <- task.Event{Kind: task.EventPending, ID: "q1", Gate: "release"} // no deadline on the frame
	q := awaitFields(t, store, QuestionRef("q1"), "the question", func(f map[string]any) bool { return f["status"] == QuestionPending })
	rec, _, _ := store.Get(QuestionRef("q1"))
	if within, dflt, _ := DeadlineOf(rec.Payload); within != "4h" || dflt != task.Deny {
		t.Fatalf("the question's clock is %q/%q, want the gate's 4h/deny: %+v", within, dflt, q)
	}
	if LabelsOf(rec.Payload)["slack"] != "C1" {
		t.Errorf("the question's labels are %v, want the gate's", LabelsOf(rec.Payload))
	}
	mu.Lock()
	defer mu.Unlock()
	if armed["q1"] != "4h" {
		t.Errorf("armed %v, want the question's clock started at the gate's 4h", armed)
	}
}

// A FRAME CANNOT LOOSEN ITS ROW'S ASK: a worker's frame that sets a shorter clock, a softer
// default or fewer approvals is posed under the row's, which is the charter's.
func TestAFrameCannotLoosenItsRowsAsk(t *testing.T) {
	store := entitytest.Store(t)
	var mu sync.Mutex
	armed := map[string]string{}
	h := &fakeHandle{ch: make(chan task.Event)}
	Reflect(store, "t1", h, Meta{Runner: "http://runner.test", As: "runner:bot",
		Autonomy: task.Autonomy{{Name: "release", Match: &task.Match{System: "git", Verb: "push"}, Then: task.Then{Mode: task.ThenAsk, Ask: &task.Ask{Need: 2, Within: "4h", Default: task.Deny}}}, {Then: task.Then{Mode: task.ThenAsk}}},
		Arm:      func(id, within string) { mu.Lock(); armed[id] = within; mu.Unlock() },
	})
	h.ch <- task.Event{Kind: task.EventPending, ID: "q1", Gate: "release",
		Deadline: &task.Deadline{Within: "1s", Default: task.Proceed}, Options: []task.Option{{ID: task.Proceed, Need: 1}, {ID: task.Deny, Need: 1}}}
	awaitFields(t, store, QuestionRef("q1"), "the question", func(f map[string]any) bool { return f["status"] == QuestionPending })
	rec, _, _ := store.Get(QuestionRef("q1"))
	if within, dflt, _ := DeadlineOf(rec.Payload); within != "4h" || dflt != task.Deny {
		t.Errorf("the question's clock is %q/%q, want the row's 4h/deny", within, dflt)
	}
	if opts := OptionsOf(rec.Payload); len(opts) == 0 || opts[0].ID != task.Proceed || opts[0].Need != 2 {
		t.Errorf("the question's options are %+v, want proceed needing the row's 2", opts)
	}
	mu.Lock()
	defer mu.Unlock()
	if armed["q1"] != "4h" {
		t.Errorf("armed %v, want the row's 4h", armed)
	}
}

// A QUESTION ON RECORD STANDS AS IT WAS POSED: its frame replayed after a restart, under a
// policy replaced since, does not re-pose it under the new row's ask, nor hold it out to the new
// row's answerers.
func TestAReplayedQuestionKeepsItsAsk(t *testing.T) {
	store := entitytest.Store(t)
	var mu sync.Mutex
	policy := task.Autonomy{{Name: "release", Match: &task.Match{System: "git", Verb: "push"}, Then: task.Then{Mode: task.ThenAsk, Ask: &task.Ask{Need: 2, Within: "4h", Default: task.Deny,
		Answerers: []string{"user:a", "user:b"}, Labels: map[string]string{"slack": "C1"}}}}, {Then: task.Then{Mode: task.ThenAsk}}}
	h := &fakeHandle{ch: make(chan task.Event)}
	Reflect(store, "t1", h, Meta{Runner: "http://runner.test", As: "runner:bot", Autonomy: policy,
		AutonomyNow: func() task.Autonomy { mu.Lock(); defer mu.Unlock(); return policy }})
	frame := task.Event{Kind: task.EventPending, ID: "q1", Gate: "release"}
	h.ch <- frame
	awaitFields(t, store, QuestionRef("q1"), "the question", func(f map[string]any) bool { return f["status"] == QuestionPending })
	mu.Lock()
	policy = task.Autonomy{{Name: "release", Match: &task.Match{System: "git", Verb: "push"}, Then: task.Then{Mode: task.ThenAsk, Ask: &task.Ask{
		Answerers: []string{"user:c"}, Labels: map[string]string{"slack": "C2", "team": "x"}}}}, {Then: task.Then{Mode: task.ThenAsk}}}
	mu.Unlock()
	h.ch <- frame
	h.ch <- task.Event{Kind: task.EventPending, ID: "q2", Gate: "release"} // after the replay is handled
	awaitFields(t, store, QuestionRef("q2"), "the next question", func(f map[string]any) bool { return f["status"] == QuestionPending })
	rec, _, _ := store.Get(QuestionRef("q1"))
	if opts := OptionsOf(rec.Payload); len(opts) == 0 || opts[0].Need != 2 {
		t.Errorf("the replayed question offers %+v, want proceed needing the 2 it was posed with", opts)
	}
	if within, dflt, _ := DeadlineOf(rec.Payload); within != "4h" || dflt != task.Deny {
		t.Errorf("the replayed question's clock is %q/%q, want the 4h/deny it was posed with", within, dflt)
	}
	if who := GranteesTo(rec.Payload); len(who) != 2 || who[0] != "user:a" {
		t.Errorf("the replayed question is held out to %v, want the user:a and user:b it was posed to", who)
	}
	if l := LabelsOf(rec.Payload); l["slack"] != "C1" || l["team"] != "" {
		t.Errorf("the replayed question's labels are %v, want the slack: C1 it was posed with", l)
	}
}

// THE RECORD CARRIES THE POLICY AS IT STANDS, viff's part with it: what a viff instance that restarts
// while the task runs elsewhere poses its questions under (PolicyOf), and what a slide changed.
func TestTheRecordCarriesThePolicyAsItStands(t *testing.T) {
	store := entitytest.Store(t)
	spawned := task.Autonomy{{Name: "release", Match: &task.Match{System: "git", Verb: "push", Params: map[string]*ir.Node{"ref": ir.FromString("refs/heads/main")}},
		Then: task.Then{Mode: task.ThenAsk, Ask: &task.Ask{Within: "4h", Default: task.Deny, Labels: map[string]string{"slack": "C1"}}}},
		{Then: task.Then{Mode: task.ThenAsk}}}
	var mu sync.Mutex
	now := spawned
	h := &fakeHandle{ch: make(chan task.Event)}
	Reflect(store, "t1", h, Meta{Runner: "http://runner.test", As: "runner:bot", Autonomy: spawned,
		AutonomyNow: func() task.Autonomy { mu.Lock(); defer mu.Unlock(); return now }})
	got := awaitFields(t, store, Ref("t1"), "the record", func(f map[string]any) bool { return f["autonomy"] != nil })
	e, _, _ := store.Get(Ref("t1"))
	back := AutonomyOf(e.Payload)
	if len(back) != 2 || back[0].Name != "release" || systems.Words(back[0].Match.Params["ref"]) != "refs/heads/main" || back[0].Then.Ask == nil ||
		back[0].Then.Ask.Default != task.Deny || back[0].Then.Ask.Labels["slack"] != "C1" || back[1].Then.Mode != task.ThenAsk {
		t.Fatalf("the record's policy reads back as %+v, want the spawn's whole: %+v", back, got)
	}

	mu.Lock()
	now = task.Autonomy{{Name: "writes", Match: &task.Match{System: "files", Verb: "write", Params: map[string]*ir.Node{"path": systems.Regexp("^db/")}}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenAllow}}}
	mu.Unlock()
	h.ch <- task.Event{Kind: task.EventAutonomy, Text: now.Summary()}
	awaitFields(t, store, Ref("t1"), "the slide", func(f map[string]any) bool {
		e, _, _ := store.Get(Ref("t1"))
		gs := AutonomyOf(e.Payload)
		return len(gs) == 2 && gs[0].Name == "writes"
	})
}

// WHAT THE RECORD SAYS OF ITS LAUNCH READS BACK (its repositories, its credential, which run it
// is, when it started), so a viff instance that re-attaches to a task after a restart writes them again rather than
// dropping them from the record it replaces.
func TestTheRecordsLaunchReadsBack(t *testing.T) {
	store := entitytest.Store(t)
	h := &fakeHandle{ch: make(chan task.Event)}
	Reflect(store, "t1", h, Meta{Runner: "http://runner.test", As: "runner:bot",
		Git:        []runner.Repo{{Repo: "acme/app", From: "main"}, {Repo: "ghe.corp/acme/lib"}},
		Credential: &Minted{Audience: "github", Sub: "rule:r", JTI: "j1", Expires: "2026-10-18T22:00:00Z"},
		Occurrence: 7, Started: "2026-10-18T19:00:00Z"})
	awaitFields(t, store, Ref("t1"), "the record", func(f map[string]any) bool { return f["git"] != nil })
	e, _, _ := store.Get(Ref("t1"))
	back := GitOf(e.Payload)
	if len(back) != 2 || back[0].Repo != "acme/app" || back[0].From != "main" || back[1].Repo != "ghe.corp/acme/lib" {
		t.Errorf("the record's repositories read back as %+v", back)
	}
	if c := CredentialOf(e.Payload); c == nil || *c != (Minted{Audience: "github", Sub: "rule:r", JTI: "j1", Expires: "2026-10-18T22:00:00Z"}) {
		t.Errorf("the record's credential reads back as %+v", c)
	}
	if o := OccurrenceOf(e.Payload); o != 7 {
		t.Errorf("the record's occurrence reads back as %d, want 7", o)
	}
	if s := entity.Str(e.Payload, "started"); s != "2026-10-18T19:00:00Z" {
		t.Errorf("the record says it started at %s, want the 19:00 it was carried with", s)
	}
}
