package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/task"
)

// A task the last verse left is subscribed to before the suspensions load: its lease has been
// running since that verse stopped asking, and a subscribe is what renews it. It is handed to
// its engine after them, so it is not paused for the table not having loaded.
func TestALeftTaskIsSubscribedToBeforeTheSuspensionsLoad(t *testing.T) {
	srv := testServer(t)
	var mu sync.Mutex
	var loadedAtSubscribe []bool
	pauses := 0
	release := make(chan struct{})
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/events"):
			mu.Lock()
			loadedAtSubscribe = append(loadedAtSubscribe, srv.susp.Loaded())
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
		case strings.HasSuffix(r.URL.Path, "/pause"):
			mu.Lock()
			pauses++
			mu.Unlock()
		}
	}))
	t.Cleanup(runner.Close)
	t.Cleanup(func() { close(release) })

	entitytest.Set(t, srv.Store, mirror.Ref("left"), entity.Obj{
		"status": string(task.Running), "runner": runner.URL, "flow": `demo.pr."1"`, "lease": "30m",
	}.Payload())

	srv.susp.Unload()
	ctx, down := context.WithCancel(t.Context())
	t.Cleanup(down)
	srv.Start(ctx)

	waitUntil(t, "the left task to be subscribed to", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(loadedAtSubscribe) > 0
	})
	mu.Lock()
	defer mu.Unlock()
	if loadedAtSubscribe[0] {
		t.Fatal("the task was first subscribed to after the suspensions had loaded, want before")
	}
	if pauses != 0 {
		t.Fatalf("the task was paused %d times at the start, and no suspension covers it", pauses)
	}
	if got := len(srv.Eng.Running()); got != 1 {
		t.Fatalf("the root package's engine tracks %d tasks after the start, want the one left", got)
	}
	e, _, _ := srv.Store.Get(mirror.Ref("left"))
	if got := entity.Str(e.Payload, "lease"); got != "30m" {
		t.Fatalf("the re-attached record's lease = %q, want the 30m it was launched with", got)
	}
}
