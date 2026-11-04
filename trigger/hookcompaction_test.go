package trigger

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// longRunning is a driver that works until it is let go, then finishes with its answer: a task that
// runs for as long as the test needs it to — past the store's cutoff, here.
type longRunning struct {
	release <-chan struct{}
	r       string
}

func (d longRunning) Next(ctx context.Context, _ task.StepResult) (task.ToolUse, bool, error) {
	select {
	case <-d.release:
	case <-ctx.Done():
	}
	return task.ToolUse{}, false, nil
}
func (d longRunning) Result() string { return d.r }

// heldEngine is an engine over a store whose history the test can compact, spawning the rule's
// own task (chain `fix`, or the first element of its sequence) as a held one that answers answer,
// and anything else — a hook's own task — as one done at once. Each is mirrored the way versed's
// spawner does it. Every spec it is handed is sent on specs.
func heldEngine(t *testing.T, release <-chan struct{}, answer string) (*Engine, *entity.LogdStore, func(), chan launch.Spec) {
	t.Helper()
	store, compact := entitytest.CompactingStore(t)
	eng := New(store, task.AutoResolver{})
	specs := make(chan launch.Spec, 8)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		specs <- spec
		sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
		var d task.Driver = longRunning{release: release, r: answer}
		if spec.Chain != "fix" && spec.Chain != "fix.seq[0]" {
			d = immediate{}
		}
		p := task.NewTask(id, sup, d, noExec{})
		mirror.Reflect(store, id, p, mirror.Meta{Subject: spec.Subject, Runner: "local", Chain: spec.Chain,
			Prompt: spec.Prompt, Occurrence: spec.Occurrence})
		go p.Run(context.Background())
		return p
	})
	// Scoped, as the other hook tests are, rather than Start: Start's follow loop re-renders the
	// installed trigger while firings and hooks read its nodes, a race (present before this
	// change) that `go test -race` reports and that these tests do not need the follow loop for.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng.ObserveScoped(ctx)
	return eng, store, compact, specs
}

// pastTheCutoff waits for the rule's task to be running with its record written, then erases the
// store's history so the firing's commit can no longer be read there — as a store compacting at
// one hour does to a task that runs for two. It answers the firing's commit.
func pastTheCutoff(t *testing.T, store *entity.LogdStore, compact func(), specs <-chan launch.Spec) int64 {
	t.Helper()
	var spec launch.Spec
	select {
	case spec = <-specs:
	case <-time.After(5 * time.Second):
		t.Fatal("the rule never spawned")
	}
	id := spawnID("", chainID(spec.Chain), prRef("1"))
	deadline := time.After(5 * time.Second)
	for {
		if e, ok, _ := store.Get(mirror.Ref(id)); ok && entity.Str(e.Payload, "status") == "running" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("task %s never reached its record", id)
		case <-time.After(10 * time.Millisecond):
		}
	}
	compact()
	// The condition under test, checked rather than assumed: the firing's world is gone from
	// the store.
	if _, err := store.AsOf(spec.Occurrence, []string{"demo", "pr"}); !errors.Is(err, entity.ErrReplayTooOld) {
		t.Fatalf("after compaction a read at the firing's commit %d = %v; want ErrReplayTooOld", spec.Occurrence, err)
	}
	return spec.Occurrence
}

// A COMPLETION HOOK OF A TASK THAT RAN PAST THE CUTOFF READS THE SUBJECT. The rule moves the
// subject to `fixing` and spawns; the store compacts the firing's commit away while the task runs;
// the task finishes `done`, and its onDone reads `subject.value` — the subject as it is when the
// task ends, after the rule's own patch. It used to read the store at the firing's commit, be
// refused (ErrReplayTooOld), and write nothing, leaving the subject at `fixing` with nothing on it
// saying why.
func TestAHookOfATaskPastTheCutoffReadsTheSubject(t *testing.T) {
	release := make(chan struct{})
	eng, store, compact, specs := heldEngine(t, release, "fixed it")
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: fix
  condition:
    subject: {path: "demo.pr.*", value: {stage: open}}
  action:
    patch: {stage: fixing}
    spawn:
      prompt: "fix it"
      autonomy: [{then: allow}]
    onDone:
      patch: {stage: fixed, round: .[subject.value.round], was: .[subject.value.stage]}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open", "round": 3}.Payload())
	pastTheCutoff(t, store, compact, specs)
	close(release)

	waitField(t, store, prRef("1"), "stage", "fixed")
	e, _, _ := store.Get(prRef("1"))
	if r := entity.Get(e.Payload, "round"); r == nil || r.Int64 == nil || *r.Int64 != 3 {
		t.Errorf("round = %v, want 3, the subject's round", r)
	}
	if w := entity.Str(e.Payload, "was"); w != "fixing" {
		t.Errorf("was = %q, want fixing: the subject as it is when the task ends, after the rule's own patch", w)
	}
}

// A HOOK SPAWNING A SCRIPT THAT READS ONLY `$[data…]` LAUNCHES PAST THE CUTOFF. The spawn reads
// the subject for every hook spawn that has one, whether or not an expression reads
// `subject.value`; read at the firing's commit, a read the store refused failed the firing and
// nothing launched — a spawn that never needed the history lost to its loss.
func TestAHookSpawnPastTheCutoffLaunches(t *testing.T) {
	release := make(chan struct{})
	eng, store, compact, specs := heldEngine(t, release, `{round: 4}`)
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: fix
  condition:
    subject: {path: "demo.pr.*", value: {stage: open}}
  action:
    patch: {stage: fixing}
    spawn:
      prompt: "fix it"
      autonomy: [{then: allow}]
    onDone:
      spawn:
        script: "echo $ROUND"
        autonomy: [{then: allow}]
        environment:
          vars: {ROUND: $[data.round]}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	pastTheCutoff(t, store, compact, specs)
	close(release)

	select {
	case hook := <-specs:
		if hook.Chain != "fix.onDone" {
			t.Fatalf("spawned %q, want fix.onDone", hook.Chain)
		}
		if hook.Refusal != "" {
			t.Fatalf("the hook's spawn was refused: %s", hook.Refusal)
		}
		if got := hook.Environment.Vars["ROUND"]; got != "4" {
			t.Errorf("ROUND = %q, want 4, from the task's answer", got)
		}
	case <-time.After(5 * time.Second):
		e, _, _ := store.Get(mirror.Ref(spawnID("", "fix", prRef("1"))))
		t.Fatalf("the onDone never launched; the task's record: %v", e.Payload)
	}
}

// A SEQUENCE ELEMENT RESUMED PAST THE CUTOFF READS THE SUBJECT. The first element's task outlives
// the firing's commit in the store; the element after it reads `subject.value`, as it is when the
// task ends, and the run finishes.
func TestASequenceStepPastTheCutoffReadsTheSubject(t *testing.T) {
	release := make(chan struct{})
	eng, store, compact, specs := heldEngine(t, release, "fixed it")
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: fix
  condition:
    subject: {path: "demo.pr.*", value: {stage: open}}
  action:
    patch: {stage: fixing}
    seq:
    - spawn:
        prompt: "fix it"
        autonomy: [{then: allow}]
    - patch: {stage: fixed, was: .[subject.value.stage]}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	pastTheCutoff(t, store, compact, specs)
	close(release)

	waitField(t, store, prRef("1"), "stage", "fixed")
	e, _, _ := store.Get(prRef("1"))
	if w := entity.Str(e.Payload, "was"); w != "fixing" {
		t.Errorf("was = %q, want fixing: the subject as it is when the element's task ends", w)
	}
	// And the run concludes, which lands after the element's own write.
	deadline := time.After(5 * time.Second)
	for {
		e, _, _ := store.Get(prRef("1"))
		facts := entity.Get(entity.Get(entity.Get(e.Payload, VerseRoot), CharterKey), "fix")
		if d := entity.Get(facts, seqDone); d != nil && d.Bool {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the sequence did not conclude done: %v", entity.ToObj(facts))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A HOOK THAT FAILS SAYS SO ON ITS SUBJECT, under its rule's own bookkeeping, and the next clean
// hook of that rule about the subject takes it away. The task's record keeps its own `hook`
// record as before; the subject's fields the author owns are not touched.
func TestAFailedHookIsOnItsSubjectUntilTheNextCleanOne(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	answers := make(chan string, 2)
	answers <- `{nothing: here}`
	answers <- `{verdict: fixed}`
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
		p := task.NewTask(id, sup, structured{r: <-answers}, noExec{})
		mirror.Reflect(store, id, p, mirror.Meta{Subject: spec.Subject, Runner: "local", Chain: spec.Chain,
			Prompt: spec.Prompt, Occurrence: spec.Occurrence})
		go p.Run(context.Background())
		return p
	})
	// Scoped rather than Start, for the reason heldEngine gives.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: fix
  condition:
    subject: {path: "demo.pr.*", value: {stage: open}}
  action:
    patch: {stage: fixing}
    spawn:
      prompt: "fix it"
      autonomy: [{then: allow}]
    onDone:
      patch: {stage: .[data.verdict]}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	trouble := func() entity.Payload {
		e, _, _ := store.Get(prRef("1"))
		return entity.Get(entity.Get(entity.Get(entity.Get(e.Payload, VerseRoot), CharterKey), "fix"), hookTrouble)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	deadline := time.After(5 * time.Second)
	for trouble() == nil {
		select {
		case <-deadline:
			e, _, _ := store.Get(prRef("1"))
			t.Fatalf("the failed hook left nothing on its subject: %v", e.Payload)
		case <-time.After(10 * time.Millisecond):
		}
	}
	rec := trouble()
	id := spawnID("", "fix", prRef("1"))
	if c := entity.Str(rec, "chain"); c != "fix.onDone" {
		t.Errorf("the subject's record names chain %q, want fix.onDone", c)
	}
	if got := entity.Str(rec, "task"); got != id {
		t.Errorf("the subject's record names task %q, want %q", got, id)
	}
	if o := entity.Get(rec, "occurrence"); o == nil || o.Int64 == nil || *o.Int64 == 0 {
		t.Errorf("the subject's record names no occurrence: %v", rec)
	}
	if err := entity.Str(rec, "err"); !strings.Contains(err, "verdict") {
		t.Errorf("the subject's record says %q, want the reference that failed", err)
	}
	if e, _, _ := store.Get(prRef("1")); entity.Str(e.Payload, "stage") != "fixing" {
		t.Errorf("the subject's stage moved to %q; a failed hook writes nothing of the author's", entity.Str(e.Payload, "stage"))
	}
	if e, _, _ := store.Get(mirror.Ref(id)); entity.Get(e.Payload, "hook") == nil {
		t.Errorf("the task's own record lost its `hook` failure: %v", e.Payload)
	}

	// Again, and this time the hook runs cleanly: the trouble is gone with it. That the rule
	// fires at all says the failed hook released its claim on the subject.
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "stage", "fixed")
	deadline = time.After(5 * time.Second)
	for trouble() != nil {
		select {
		case <-deadline:
			t.Fatalf("a clean hook left the old failure on its subject: %v", trouble())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A SEQUENCE ELEMENT THAT CANNOT CONTINUE OFF A COMPLETION ENDS THE RUN AND SAYS WHY. The first
// element's task answers without the field the next element reads. That used to be a trace line:
// the run released, `step` stood still and nothing said why. Now it is written where a failed
// hook is, and the run is `ended`, with the reason.
func TestASequenceStepThatFailsOffACompletionEndsAndSaysWhy(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	var fired int64
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		fired = spec.Occurrence
		sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
		p := task.NewTask(id, sup, structured{r: `{nothing: here}`}, noExec{})
		mirror.Reflect(store, id, p, mirror.Meta{Subject: spec.Subject, Runner: "local", Chain: spec.Chain,
			Prompt: spec.Prompt, Occurrence: spec.Occurrence})
		go p.Run(context.Background())
		return p
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: fix
  condition:
    subject: {path: "demo.pr.*", value: {stage: open}}
  action:
    patch: {stage: fixing}
    seq:
    - spawn:
        prompt: "fix it"
        autonomy: [{then: allow}]
    - patch: {stage: .[data.verdict]}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())

	facts := func() entity.Payload {
		e, _, _ := store.Get(prRef("1"))
		return entity.Get(entity.Get(entity.Get(e.Payload, VerseRoot), CharterKey), "fix")
	}
	deadline := time.After(5 * time.Second)
	for entity.Str(facts(), seqEnded) == "" {
		select {
		case <-deadline:
			e, _, _ := store.Get(prRef("1"))
			t.Fatalf("the sequence never ended: %v", entity.ToObj(e.Payload))
		case <-time.After(10 * time.Millisecond):
		}
	}
	f := facts()
	if got := entity.Str(f, seqEnded); got != seqEndedError {
		t.Errorf("ended = %q, want %q", got, seqEndedError)
	}
	if r := entity.Str(f, seqReason); !strings.Contains(r, "verdict") {
		t.Errorf("reason = %q, want the reference that failed", r)
	}
	id := spawnID("", chainID("fix.seq[0]"), prRef("1"))
	rec := entity.Get(f, hookTrouble)
	if c := entity.Str(rec, "chain"); c != "fix.seq[1]" {
		t.Errorf("the subject's hook record names chain %q, want fix.seq[1]: %v", c, entity.ToObj(rec))
	}
	if got := entity.Str(rec, "task"); got != id {
		t.Errorf("the subject's hook record names task %q, want %q", got, id)
	}
	if o := entity.Get(rec, "occurrence"); o == nil || o.Int64 == nil || *o.Int64 != fired {
		t.Errorf("the subject's hook record names occurrence %v, want %d, the firing's", o, fired)
	}
	if err := entity.Str(rec, "err"); !strings.Contains(err, "verdict") {
		t.Errorf("the subject's hook record says %q, want the reference that failed", err)
	}
	if e, _, _ := store.Get(prRef("1")); entity.Str(e.Payload, "stage") != "fixing" {
		t.Errorf("stage = %q; an element that failed writes nothing of the author's", entity.Str(e.Payload, "stage"))
	}
	deadline = time.After(5 * time.Second)
	for {
		if e, _, _ := store.Get(mirror.Ref(id)); entity.Str(entity.Get(e.Payload, "hook"), "status") == "failed" {
			break
		}
		select {
		case <-deadline:
			e, _, _ := store.Get(mirror.Ref(id))
			t.Fatalf("the completed task's record has no failed `hook`: %v", entity.ToObj(e.Payload))
		case <-time.After(10 * time.Millisecond):
		}
	}
}
