package trigger

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/launch"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/task"
)

// dropsStream is a remote task across a runner restart: its first stream closes with no frame at
// all, and every resume after that answers with `then` (the runner's 404 reads as a failed frame)
// or, with then empty, closes again with nothing.
type dropsStream struct {
	then string
	mu   sync.Mutex
	subs int
}

func (h *dropsStream) Subscribe() (<-chan task.Event, func()) {
	ch := make(chan task.Event)
	close(ch)
	return ch, func() {}
}

func (h *dropsStream) SubscribeSince(int64) (<-chan task.Event, func()) {
	h.mu.Lock()
	h.subs++
	h.mu.Unlock()
	ch := make(chan task.Event, 1)
	if h.then != "" {
		ch <- task.Event{Kind: task.EventStatus, Text: h.then}
	}
	close(ch)
	return ch, func() {}
}

func (h *dropsStream) resumes() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.subs
}

func (h *dropsStream) Pause(string) error                      { return nil }
func (h *dropsStream) Resume(string) error                     { return nil }
func (h *dropsStream) Cancel(string, string) error             { return nil }
func (h *dropsStream) Steer(string, string, string) error      { return nil }
func (h *dropsStream) SetAutonomy(task.Autonomy, string) error { return nil }
func (h *dropsStream) Result() string                          { return "" }
func (h *dropsStream) Err() string                             { return "" }
func (*dropsStream) Refused() map[string]int                   { return nil }
func (*dropsStream) Agent() (string, map[string]string)        { return "", nil }

const askRule = `
- name: ask
  condition:
    subject: {path: "demo.pr.*", value: !and [{stage: needs-you}, !not.has-path answer]}
  action:
    spawn:
      prompt: "ask"
      autonomy: [{then: allow}]
    onDone:
      patch: {answer: .[result]}
`

// droppedEngine runs askRule with every spawn handed a dropsStream answering then; it reports
// each spawn's id on the channel.
func droppedEngine(t *testing.T, then string, wait func(int) time.Duration) (*Engine, entity.Store, chan string, func() []*dropsStream) {
	t.Helper()
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.redialWait = wait
	spawned := make(chan string, 4)
	var mu sync.Mutex
	var handles []*dropsStream
	eng.SetSpawner(func(id string, _ launch.Spec) task.Handle {
		h := &dropsStream{then: then}
		mu.Lock()
		handles = append(handles, h)
		mu.Unlock()
		spawned <- id
		return h
	})
	if err := eng.AddTrigger(mustParseCharter(t, askRule)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng.ObserveScoped(ctx)
	return eng, store, spawned, func() []*dropsStream {
		mu.Lock()
		defer mu.Unlock()
		return append([]*dropsStream(nil), handles...)
	}
}

func waitSpawn(t *testing.T, spawned chan string, what string) string {
	t.Helper()
	select {
	case id := <-spawned:
		return id
	case <-time.After(5 * time.Second):
		t.Fatalf("no spawn: %s", what)
		return ""
	}
}

func waitClaimGone(t *testing.T, store entity.Store, claim entity.Ref) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if _, held, _ := store.Get(claim); !held {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the claim at %s stands after its task ended", claim)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A TASK WHOSE STREAM DROPS STILL RELEASES ITS SUBJECT. The stream closes with no terminal, as a
// remote task's does when its runner restarts; that is an outage, not the end of the task. The
// reaper resumes from its cursor, hears the runner say failed, and runs the hook path, which
// releases the spawn's claim, so the rule fires about the subject again when it matches anew.
// Read as "the task is gone", the claim stood and the rule never fired about the subject again.
func TestADroppedTaskStreamReleasesItsSubject(t *testing.T) {
	_, store, spawned, _ := droppedEngine(t, string(task.Failed), nil)
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "needs-you"}.Payload())
	id := waitSpawn(t, spawned, "the rule never spawned")
	entitytest.Set(t, store, mirror.Ref(id), entity.Obj{"status": "failed"}.Payload())

	waitClaimGone(t, store, mirror.ClaimRef(spawnID("", "ask", prRef("1"))))

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "checking"}.Payload())
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "needs-you"}.Payload())
	waitSpawn(t, spawned, "the rule did not fire about the subject again")
}

// A STREAM THAT NEVER COMES BACK ENDS THE TASK ORPHANED. The reaper gives up after its budget of
// resumes, writes the record orphaned, and still takes the hook path, so the claim comes off.
func TestATaskStreamThatNeverComesBackEndsOrphaned(t *testing.T) {
	_, store, spawned, handles := droppedEngine(t, "", func(int) time.Duration { return 5 * time.Millisecond })
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "needs-you"}.Payload())
	id := waitSpawn(t, spawned, "the rule never spawned")
	entitytest.Set(t, store, mirror.Ref(id), entity.Obj{"status": "running"}.Payload())

	waitClaimGone(t, store, mirror.ClaimRef(spawnID("", "ask", prRef("1"))))
	if n := handles()[0].resumes(); n != redialAttempts {
		t.Errorf("resumed %d times before giving up, want %d", n, redialAttempts)
	}
	waitField(t, store, mirror.Ref(id), "status", string(task.Orphaned))
}
