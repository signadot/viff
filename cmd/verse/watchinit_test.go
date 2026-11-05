package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
)

// watchWithin runs a watch that is expected to end, and fails rather than hanging if it does not.
func watchWithin(t *testing.T, d time.Duration, args ...string) string {
	t.Helper()
	type res struct {
		out string
		err error
	}
	done := make(chan res, 1)
	go func() {
		out, err := runCLI(t, args...)
		done <- res{out, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("watch: %v (out=%q)", r.err, r.out)
		}
		return r.out
	case <-time.After(d):
		t.Fatalf("watch %v never ended", args)
		return ""
	}
}

// A WATCH OPENS WITH WHAT ALREADY STANDS, so `--until` over a state already reached ends at once
// rather than waiting for it to happen again.
func TestAWatchOpensWithWhatStands(t *testing.T) {
	store := serve(t)
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "merged"}.Payload())

	out := watchWithin(t, 10*time.Second, "watch", "demo", "pr", "*", "--until", `{to: {stage: merged}}`)
	if !strings.Contains(out, "demo.pr") || !strings.Contains(out, "merged") {
		t.Fatalf("the opening state was not printed: %q", out)
	}
}

// --no-init starts from the next change: what stood before the watch opened is not reported.
func TestNoInitStartsFromTheNextChange(t *testing.T) {
	store := serve(t)
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())
	go func() {
		time.Sleep(300 * time.Millisecond)
		entitytest.Set(t, store, entity.RefAt("demo", "pr", "2"), entity.Obj{"stage": "open"}.Payload())
	}()

	out := watchWithin(t, 10*time.Second, "watch", "--no-init", "demo", "pr", "*", "--until", `{to: {stage: open}}`)
	if strings.Contains(out, `"1"`) || !strings.Contains(out, `"2"`) {
		t.Fatalf("want only the change after the watch opened (pr 2): %q", out)
	}
}

// A cursor replays history in place of the opening state, so the default does not fight it: a
// `--since` watch with init left on is the history, as it was.
func TestACursorReplaysHistoryInsteadOfTheOpeningState(t *testing.T) {
	store := serve(t)
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "merged"}.Payload())

	out := watchWithin(t, 10*time.Second, "watch", "--since", "0", "demo", "pr", "*", "--until", `{to: {stage: merged}}`)
	if !strings.Contains(out, "stage: open") {
		t.Fatalf("the history before the opening state was not replayed: %q", out)
	}
}

// The route refuses `init` where it cannot mean it, rather than dropping it: beside a cursor,
// which has already said what comes first, and with no literal prefix, where the watch joins the
// verse-wide stream and has no opening state.
func TestTheRouteRefusesInitWhereItHasNoMeaning(t *testing.T) {
	store := serve(t)
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())
	for _, q := range []string{"init=true&since=0&at=demo", "init=true"} {
		resp, err := http.Get("http://" + testAddr + "/watch?" + q)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("/watch?%s: %s, want 400 (%s)", q, resp.Status, body)
		}
	}
}
