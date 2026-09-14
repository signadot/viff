package versed

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
)

// sseData attaches to an event stream and hands back its `data:` payloads on a channel, read on
// a goroutine of their own so a caller can put a deadline on a stream that has gone SILENT.
//
// The returned stop is how the read ends, and a caller defers it rather than closing the body.
// Closing the body while that goroutine is still inside Read races the client's post-Close
// drain (Go 1.27, golang/go#77370): the reader can catch the stream's EOF and park on the
// connection's shared end-of-body handoff, the connection goes back to the pool anyway, and the
// next request to reuse it hangs forever in its own Close — no stall, no failure, just a test
// that never ends. Cancelling the request kills the connection instead, so nothing is pooled and
// the Read fails rather than parks; the body is closed once the reader has gone.
func sseData(t *testing.T, cl *http.Client, url string) (*http.Response, <-chan string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		t.Fatalf("attach %s: %v", url, err)
	}
	resp, err := cl.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("attach %s: %v", url, err)
	}
	lines := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				lines <- d
			}
		}
		close(lines)
	}()
	stop := func() {
		cancel()
		for range lines { // the reader may be parked on a full channel rather than in Read
		}
		resp.Body.Close()
	}
	return resp, lines, stop
}
