package main

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
)

// The config's `user:` is who the CLI acts as: what it writes is theirs, and the verse lists
// them as a user. VERSE_USER is the environment's spelling of the same line.
func TestTheConfiguredUserOwnsWhatTheyPut(t *testing.T) {
	store := serve(t)
	t.Setenv("VERSE_USER", "alice@example.com")

	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", `{stage: open}`); err != nil {
		t.Fatalf("put: %v", err)
	}
	rec, ok, err := store.Get(entity.PermsRef(entity.RefAt("demo", "pr", "1")))
	if err != nil || !ok {
		t.Fatalf("no owner record for demo.pr.1 (ok=%v err=%v)", ok, err)
	}
	if got := entity.Str(rec.Payload, entity.OwnerField); got != "alice@example.com" {
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
