# restore: a charter that could not be read restores 'successfully' when any other reader lands in between

`LoadCharter` and `LoadStrata` read with `entity.ListUnder` and then ask `entity.ReadFailed(e.store)`
— the STORE-WIDE flag — whether that read happened (trigger/charter.go:210 and :232,
trigger/stratum.go:723 and :739). `LogdStore.readErr` is one field for every reader sharing the
store — the engine, the sources, and each request in flight — and "the next read that succeeds
clears it". So whether a restore notices its OWN failed read depends on who else read in between:

    LoadCharter (nothing else read)  → the charter could not be read, so nothing was restored from it: context deadline exceeded
    LoadCharter (a neighbour read)   → nil        ← the verse comes back running NO RULES, silently
    LoadStrata  (a neighbour read)   → nil

All three are from a store whose reads failed every time.

This is an earlier, previously reported defect at the two call sites that commit deliberately left asking
the store. The six handlers, `runner.List` and `source.List` moved to per-read errors in
`eb5af707`, and a restore was argued to be "asking about the store, not about one read". It is
not: its question is whether ITS read of the charter happened, which is exactly the per-read
question, and the same interleaving answers it wrong in both directions — a neighbour's success
hides this read's failure, and a neighbour's failure would condemn a restore whose read was fine.

WHAT MAKES IT LIKELIER THAN WHEN THAT WAS WRITTEN: a NOT-FOUND clears the flag too.
`ListErr`/`ListAtErr` call `noteRead(nil)` when the substrate says nothing stands at the path
(entity/logdstore.go:1078 and :1156) — right in itself, absence is not an outage — so the
neighbour no longer has to read anything successfully. An ordinary read of a path nobody has
written to now clears a real failure, and a verse coming up is full of those.

WHAT IT COSTS is the whole reason the guard is there, and its own comment says so: a restore that
answers nil restored nothing and said nothing. `Start` prints no error, the engine comes up with
an empty charter, and the verse runs no rules until somebody notices — under load, which is
exactly when the read fails.

THE FIX is the one `eb5af707` prescribes and already built: `entity.ListUnderErr` at both sites,
joining the error THAT read failed with. A store without the per-read capability falls back to
`List` plus `ReadFailed`, which is what these two have today, so adopting it is never worse.
`entity.ReadFailed` still has a caller after that — versed/server.go:602, versed/sources.go:762 —
and whether those are asking about the store or about a read of their own is the same question
asked again, worth settling in the same pass.

REPRO. Drop this in `trigger/` (it uses `errRead` and the `entitytest` store from the package's
existing `restoreread_test.go`) and run `go test ./trigger/ -run TestReproRestoreNeighbour -v`.
It FAILS today, on the second and third probes; the first passes, which is the point — the same
failed read is reported or swallowed depending only on the neighbour.

```go
package trigger

import (
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// flapping is a store whose reads FAIL, beside another reader on the same store whose read
// SUCCEEDS — the ordinary state of a verse coming up, where the engine, the sources and the
// first requests all read one LogdStore. `readErr` is ONE field for all of them.
type flapping struct {
	entity.Store
	readErr   error
	neighbour bool // somebody else reads between this read and the guard
}

func (f *flapping) read() []entity.Entity {
	f.readErr = errRead // this read did not happen…
	if f.neighbour {
		f.readErr = nil // …and somebody else's did, before the guard looked
	}
	return nil
}

func (f *flapping) List(func(entity.Ref) bool) []entity.Entity { return f.read() }
func (f *flapping) ListAt([]string, int, func(entity.Ref) bool) []entity.Entity {
	return f.read()
}
func (f *flapping) ReadError() error { return f.readErr }

func TestReproRestoreNeighbour(t *testing.T) {
	store := &flapping{Store: entitytest.Store(t)}
	eng := New(store, task.AutoResolver{})

	store.neighbour = false
	if err := eng.LoadCharter(); err == nil {
		t.Error("LoadCharter: a failed read restored 'successfully' (no neighbour)")
	} else {
		t.Logf("no neighbour → %v", err)
	}

	store.neighbour = true
	if err := eng.LoadCharter(); err == nil {
		t.Error("LoadCharter: a failed read restored 'successfully' — a verse comes back running no rules")
	}
	if err := eng.LoadStrata(); err == nil {
		t.Error("LoadStrata: a failed read restored 'successfully'")
	}
}
```

WHAT THIS DOES NOT ESTABLISH: that a restore has come back empty this way. The window
is between `ListUnder` returning and `ReadFailed` being asked, which is microseconds. What makes
it worth closing anyway is that these are the two survivors whose wrong answer is silent and
whole-verse rather than one request's, and that the not-found path has since widened who can
clear the flag.