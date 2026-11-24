package viffd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
)

// A CANCEL IS RECORDED AS WHOEVER MADE IT, wherever the task runs. The local runner's own
// handler used to take a cancel's body as it stood, so its `by` was written down unchecked; a
// cancel goes through the task's handle now, as say and autonomy do, and a `by` naming anybody
// but the caller is refused.
func TestACancelIsRecordedAsWhoeverMadeIt(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	spawnScoped(t, ts, "run-1", "")

	if code, msg := control(t, ts, "/tasks/run-1/cancel", "", map[string]any{"reason": "done here", "by": "user:mallory@example.com"}); code != http.StatusBadRequest {
		t.Fatalf("a cancel by somebody else = %d %q, want 400", code, msg)
	}
	if code, msg := control(t, ts, "/tasks/run-1/cancel", "", map[string]any{"reason": "done here"}); code != http.StatusNoContent {
		t.Fatalf("a cancel = %d %q, want 204", code, msg)
	}
	deadline := time.After(3 * time.Second)
	for {
		e, ok, _ := srv.Store.Get(mirror.Ref("run-1"))
		if err := entity.Str(e.Payload, "err"); ok && err != "" {
			if !strings.Contains(err, "cancelled by user:"+tester) || !strings.Contains(err, "done here") {
				t.Fatalf("the record says %q, want the cancel by the caller with its reason", err)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("the task never recorded its cancel")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
