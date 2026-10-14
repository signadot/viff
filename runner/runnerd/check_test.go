package runnerd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/signadot/verse/task"
)

// A hosted task's question that the host refuses is never posed: the action that asked it gets
// the refusal as its error, and the task ends on it rather than waiting on a question nobody was
// asked.
func TestARefusedQuestionIsNeverPosed(t *testing.T) {
	d := NewInProcessHandler(fakeBuild)
	refused := errors.New("bob may not read what it asks about")
	var checked atomic.Value
	var posted atomic.Bool
	d.OnCheck(func(l task.Launch, q task.GateQuestion) error {
		checked.Store(l.ID)
		return refused
	})
	d.OnQuestion(func(task.Launch, task.Pending) { posted.Store(true) })
	srv := httptest.NewServer(d.HTTPHandler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/tasks", "application/json", strings.NewReader(`{"id":"run-refused","prompt":"t"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	var got task.Snapshot
	for i := 0; i < 300; i++ {
		if in, ok := d.SnapshotOf("run-refused"); ok && in.Status.Terminal() {
			got = in
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if checked.Load() != "run-refused" {
		t.Fatalf("the check was told launch %v, want run-refused", checked.Load())
	}
	if posted.Load() {
		t.Fatal("a refused question was posed")
	}
	if !got.Status.Terminal() || !strings.Contains(got.Err, refused.Error()) {
		t.Fatalf("the task ended %q with %q, want the refusal", got.Status, got.Err)
	}
}
