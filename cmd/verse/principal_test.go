package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/entity"
)

// The config's `user:` is who the CLI acts as: what it writes is theirs, and the verse lists
// them as a user. VERSE_USER is the environment's spelling of the same line.
func TestTheConfiguredUserOwnsWhatTheyPut(t *testing.T) {
	store := serve(t)
	t.Setenv("VERSE_USER", "alice@example.com")

	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", `{stage: open}`); err != nil {
		t.Fatalf("put: %v", err)
	}
	// The record stands at the place the put brought into being, which is `demo`: the row
	// beneath it is the configured user's through that.
	rec, ok, err := store.Get(entity.PermsRef(entity.RefAt("demo")))
	if err != nil || !ok {
		t.Fatalf("no owner record for demo (ok=%v err=%v)", ok, err)
	}
	if got := entity.Str(rec.Payload, entity.OwnerField); got != "user:alice@example.com" {
		t.Fatalf("owner = %q, want the configured user", got)
	}

	// And they are on the list, readable the way any of verse's own records is.
	out, err := runCLI(t, "data", "verse", "principal")
	if err != nil {
		t.Fatalf("data verse principal: %v", err)
	}
	if !strings.Contains(out, "alice@example.com") {
		t.Fatalf("`verse data verse principal` does not list the user:\n%s", out)
	}
	// The mirror, though, is the system's: a person reading it is answered with nothing of it.
	if perms, _ := runCLI(t, "data", "verse", "perms"); strings.Contains(perms, "alice@example.com") {
		t.Fatalf("`verse data verse perms` shows a person the mirror:\n%s", perms)
	}
}

// A principal is spelled with its kind: a worker is `runner:<identity>`, and a bare word is
// refused with the grammar rather than guessed to be one.
func TestABareWordIsNotAPrincipal(t *testing.T) {
	serve(t)
	_, err := runCLI(t, "task", "approve", "t1", "d1", "--as", "verse-runner")
	if err == nil || !strings.Contains(err.Error(), entity.Grammar()) {
		t.Fatalf("--as verse-runner: %v", err)
	}
	t.Setenv("VERSE_USER", "smoke")
	if _, err := runCLI(t, "data", "demo", "pr", "1"); err == nil || !strings.Contains(err.Error(), "config user") {
		t.Fatalf("a bare word as the configured user: %v", err)
	}
}

// A steer is attributed to whoever makes it: `task say` and `flow say` send the principal as
// `by`, so no server has a name to invent.
func TestASayCarriesItsSpeaker(t *testing.T) {
	bodies := map[string]string{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies[r.URL.Path] = string(b)
		if r.URL.Path == "/flow/say" {
			w.Write([]byte(`{subject: demo.pr.1, verb: say}`))
			return
		}
		if r.URL.Path == "/tasks/t1/say" { // words to a task are an ask to it, answered with its id
			w.Write([]byte(`{id: say-1}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	testAddr = strings.TrimPrefix(ts.URL, "http://")
	t.Setenv("VERSE_USER", "ana@example.com")
	if _, err := runCLI(t, "task", "say", "t1", "carry", "on"); err != nil {
		t.Fatalf("task say: %v", err)
	}
	if _, err := runCLI(t, "flow", "say", "--subject", "demo.pr.1", "carry", "on"); err != nil {
		t.Fatalf("flow say: %v", err)
	}
	for _, path := range []string{"/tasks/t1/say", "/flow/say"} {
		if !strings.Contains(bodies[path], "user:ana@example.com") {
			t.Errorf("%s was sent without its speaker: %s", path, bodies[path])
		}
	}
}
