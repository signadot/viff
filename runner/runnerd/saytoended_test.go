package runnerd_test

import (
	"errors"
	"testing"
	"time"

	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/task"
)

// WORDS TO AN ENDED TASK ARE REFUSED OVER THE WIRE AS WHAT THEY ARE: runnerd answers 410, and the
// remote handle reads it as task.ErrEnded rather than as an answer somebody else gave first.
func TestWordsToAnEndedHostedTaskAreErrEnded(t *testing.T) {
	d, srv := hosting(t)
	launchOne(t, srv.URL, "run-said")
	tk := d.Lookup("run-said")
	if tk == nil {
		t.Fatal("the task is not hosted")
	}
	if err := tk.Cancel("by hand", ""); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	for i := 0; ; i++ {
		if in, ok := d.SnapshotOf("run-said"); ok && in.Status.Terminal() {
			break
		}
		if i == 200 {
			t.Fatal("the task never ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	err := client.HTTP(srv.URL).Attach("run-said").Steer("still there?", "user:alice@example.com", "")
	if !errors.Is(err, task.ErrEnded) {
		t.Fatalf("words to an ended task = %v, want task.ErrEnded", err)
	}
}
