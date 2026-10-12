package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/task"
)

// cuttingProxy relays a runner's HTTP faithfully and severs the event stream after the first
// n bytes of it — a connection that dies mid-task, which is the ordinary way a stream ends
// when nothing is wrong with the work.
func cuttingProxy(t *testing.T, upstream string, cutAfter int) *httptest.Server {
	t.Helper()
	var seen atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up, err := http.Get(upstream + r.URL.Path + "?" + r.URL.RawQuery)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer up.Body.Close()
		for k, v := range up.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(up.StatusCode)
		flush, _ := w.(http.Flusher)
		buf := make([]byte, 128)
		for {
			n, err := up.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if flush != nil {
					flush.Flush()
				}
				// Only the FIRST stream is cut. A reconnect gets a whole one, which is what
				// makes this a test about recovery rather than about failing forever.
				if seen.Add(int64(n)) > int64(cutAfter) && seen.Load() < int64(cutAfter)*100 {
					seen.Store(int64(cutAfter) * 100)
					return
				}
			}
			if err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func remoteRuntime(t *testing.T, steps int) *httptest.Server {
	t.Helper()
	d := runnerd.New(func(l task.Launch, reg *task.Registry) *task.Task {
		sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, reg)
		return task.NewTask(l.ID, sup, &pacedDriver{n: steps}, noopExecutor{})
	})
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// pacedDriver takes n steps slowly enough that a stream can be cut partway through the run.
type pacedDriver struct{ n int }

func (d *pacedDriver) Next(ctx context.Context, _ task.StepResult) (task.ToolUse, bool, error) {
	if d.n <= 0 {
		return task.ToolUse{}, false, nil
	}
	d.n--
	select {
	case <-time.After(20 * time.Millisecond):
	case <-ctx.Done():
	}
	return task.ToolUse{Name: "noop"}, true, nil
}

// A stream that dies mid-task is reconnected, and the entity ends up telling the truth.
//
// It did not. A closed channel meant both "the task ended" and "your connection died", so
// Reflect returned on the second as though it were the first: the remote finished, and the
// entity said `running` permanently — with the goroutine and the HTTP connection behind it
// leaked for good measure.
func TestTheMirrorReconnectsAStreamThatDies(t *testing.T) {
	remote := remoteRuntime(t, 40)
	proxy := cuttingProxy(t, remote.URL, 300)

	runner.HTTP(remote.URL).Spawn(task.Launch{ID: "p", Prompt: "work"})
	store := entitytest.Store(t)
	mirror.Reflect(store, "p", runner.HTTP(proxy.URL).Attach("p"), mirror.Meta{Runner: proxy.URL})

	deadline := time.After(20 * time.Second)
	for {
		e, _, _ := store.Get(mirror.Ref("p"))
		got := entity.ToObj(e.Payload)
		if task.Status(entity.Str(e.Payload, "status")).Terminal() {
			// And it is not still claiming to be disconnected: the reconnect that carried
			// it to the terminal cleared that on its first frame.
			if got["observed"] == false {
				t.Fatalf("finished, but the entity still reports the stream broken: %+v", got)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the remote task finished and the entity still says %v", got["status"])
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// While it is disconnected, the entity says so.
//
// A mirror that is retrying is not a mirror that is current, and an index that reads as
// confident while it is merely stale is the difference between "this task is running" and
// "the last thing I heard was that it was running".
func TestADisconnectedMirrorSaysSo(t *testing.T) {
	remote := remoteRuntime(t, 200) // long enough to still be running while we look
	runner.HTTP(remote.URL).Spawn(task.Launch{ID: "p", Prompt: "work"})

	// Mirrored through a runner that cannot be REACHED, which is distinct from one that
	// answers with an error: a rejected subscribe is deliberately reported as a failed
	// task on the read plane, while an unreachable one has nothing to report at all and
	// is exactly what the reconnect loop is for.
	gone := httptest.NewServer(http.NotFoundHandler())
	goneURL := gone.URL
	gone.Close()

	store := entitytest.Store(t)
	mirror.Reflect(store, "p", runner.HTTP(goneURL).Attach("p"), mirror.Meta{Runner: goneURL})

	deadline := time.After(10 * time.Second)
	for {
		e, _, _ := store.Get(mirror.Ref("p"))
		if entity.ToObj(e.Payload)["observed"] == false {
			return
		}
		select {
		case <-deadline:
			e, _, _ := store.Get(mirror.Ref("p"))
			t.Fatalf("a mirror that cannot reach its runner never said so: %+v", entity.ToObj(e.Payload))
		case <-time.After(20 * time.Millisecond):
		}
	}
}
