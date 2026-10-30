package versed

import (
	"bufio"
	"context"
	"fmt"
	"github.com/signadot/verse/runner/runnerd/runnerdtest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// lagging is a store whose watches on the perms mirror deliver each commit a little late, as a
// watch on a busy store does: tens of milliseconds, ~12 measured for a question's record.
type lagging struct{ *entity.LogdStore }

func (l lagging) WatchSubtreeProgress(depth int, prefix ...string) (<-chan entity.Commit, func(), func(context.Context) (int64, error), error) {
	in, unsub, progress, err := l.LogdStore.WatchSubtreeProgress(depth, prefix...)
	if err != nil || len(prefix) < 2 || prefix[0] != entity.VerseRoot || prefix[1] != entity.PermsSystem {
		return in, unsub, progress, err
	}
	out := make(chan entity.Commit, 256)
	go func() {
		defer close(out)
		for c := range in {
			time.Sleep(20 * time.Millisecond)
			out <- c
		}
	}()
	return out, unsub, progress, nil
}

// A RECORD THE SYSTEM WRITES HOLDS AT THE ROUTES FROM ITS COMMIT. The system establishes a node's
// record through an attribution of its own — the engine's, posing a question — and a reader
// through `GET /entity` or `GET /watch` asks the server's view, which hears of the record from its
// watch on the mirror. A reader arriving in between was answered from before the record: refused
// a question held out to them, or here, read a node held out to nobody.
func TestARecordTheSystemEstablishesHoldsAtTheRoutesAtOnce(t *testing.T) {
	addr := entitytest.Addr(t)
	srv := serverWith(t, runnerdtest.Scripted, lagging{entitytest.StoreAt(t, addr)})
	t.Cleanup(srv.stopAllIOs)
	if err := entity.Track(t.Context(), srv.Store); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	system := entity.As(entitytest.StoreAt(t, addr), entity.SystemDefault)
	const stranger = "stranger@example.com"

	// The watch first, from now, at a place something already stands.
	entitytest.Set(t, system, entity.RefAt("demo", "start"), entity.Obj{"v": 0}.Payload())
	frames, stop := watchAs(t, stranger, ts.URL+"/watch?at=demo.*")
	defer stop() // before the server closes, which waits for the stream

	private := map[string]bool{}
	admitted := 0
	const n = 30
	for i := range n {
		node := entity.RefAt("demo", fmt.Sprint("q", i))
		if err := entity.Establish(system, node, "user:alice@example.com", entity.Ref{}, []entity.Grant{}); err != nil {
			t.Fatal(err)
		}
		entitytest.Set(t, system, node, entity.Obj{"secret": i}.Payload())
		private[node.String()] = true
		resp := getAs(t, stranger, ts.URL+"/entity?at="+node.String())
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			admitted++
			t.Logf("%s answered the stranger %s", node, resp.Status)
		}
	}
	if admitted > 0 {
		t.Errorf("GET /entity answered a stranger %d of %d nodes whose record holds nothing out", admitted, n)
	}

	end := entity.RefAt("demo", "end")
	entitytest.Set(t, system, end, entity.Obj{"v": 1}.Payload())
	delivered := 0
	for ref := range frames(end.String()) {
		if private[ref] {
			delivered++
		}
	}
	if delivered > 0 {
		t.Errorf("GET /watch delivered the stranger %d of %d nodes whose record holds nothing out", delivered, n)
	}
}

// watchAs attaches to /watch as a principal and answers a function that reads the stream's
// frames up to the sentinel address, yielding the address each one is at, and the stop that
// ends the stream (see sseData).
func watchAs(t *testing.T, as, url string) (func(sentinel string) func(func(string) bool), func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set(api.PrincipalHeader, as)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("watch: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		resp.Body.Close()
		t.Fatalf("watch: %s", resp.Status)
	}
	f, err := codec.Parse(resp.Header.Get(codec.HeaderFormat))
	if err != nil {
		cancel()
		resp.Body.Close()
		t.Fatalf("stream format: %v", err)
	}
	lines := make(chan string, 1024)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				lines <- d
			}
		}
	}()
	// Cancelling kills the connection rather than returning it to the pool (see sseData).
	stop := func() {
		cancel()
		for range lines {
		}
		resp.Body.Close()
	}
	return func(sentinel string) func(func(string) bool) {
		return func(yield func(string) bool) {
			for data := range lines {
				var frame struct {
					Ref string `tony:"field=at"`
				}
				if err := codec.Unmarshal([]byte(data), &frame, f); err != nil {
					t.Fatalf("decoding a frame: %v\n%s", err, data)
				}
				if frame.Ref == sentinel {
					return
				}
				if !yield(frame.Ref) {
					return
				}
			}
			t.Fatalf("the stream ended before %s", sentinel)
		}
	}, stop
}
