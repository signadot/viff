package main

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/versed"
)

// unreadable is a store whose reads do not happen — the shape of a store under load, where a
// read exceeds the session budget and List answers nil. `on` is what makes it fail, so the same
// store serves the healthy half of the comparison.
type unreadable struct {
	entity.Store
	on bool
}

func (u *unreadable) List(pred func(entity.Ref) bool) []entity.Entity {
	if u.on {
		return nil
	}
	return u.Store.List(pred)
}

func (u *unreadable) ListAt(at []string, depth int, pred func(entity.Ref) bool) []entity.Entity {
	if u.on {
		return nil
	}
	return u.Store.ListAt(at, depth, pred)
}

func (u *unreadable) ReadError() error {
	if u.on {
		return errors.New("context deadline exceeded")
	}
	return nil
}

// serveUnreadable stands up a verse over a store that can be made to stop answering, and points
// the client at it.
func serveUnreadable(t *testing.T) *unreadable {
	t.Helper()
	store := &unreadable{Store: entitytest.Store(t)}
	srv := versed.NewWithStore(versed.ScriptedBuilder, store)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	testAddr = strings.TrimPrefix(ts.URL, "http://")
	return store
}

// AN UNREADABLE STORE IS NOT AN EMPTY VERSE, on the form a SCRIPT reads.
//
// `--paths` prints one address per line and nothing else, so a store that could not be read
// presents as zero records — the exact answer a verse holding nothing gives, and the failure
// the long form's refusal exists to prevent (`--paths | wc -l` reporting 0 where the truth was
// "the store could not answer"). The two forms of the one read must not disagree about that:
// whatever `verse data <path>` refuses, `verse data <path> --paths` refuses too.
func TestPathsRefusesWhatTheDocumentFormRefuses(t *testing.T) {
	store := serveUnreadable(t)
	store.on = true
	for _, args := range [][]string{
		{"git"},
		{"git", "--paths"},
		{"--paths"},
	} {
		out, err := runState(t, args...)
		if err == nil {
			t.Errorf("verse data %s answered on an unreadable store", strings.Join(args, " "))
		}
		if out != "" {
			t.Errorf("verse data %s printed %q on an unreadable store", strings.Join(args, " "), out)
		}
	}
	if _, _, err := runCLIStreams(t, "charter", "--paths"); err == nil {
		t.Error("charter --paths answered on an unreadable store")
	}
}

// …AND AN EMPTY VERSE IS NOT AN UNREADABLE STORE, which is the half that makes the refusal above
// worth anything: a listing of nothing is a true answer, and it succeeds. The two cases differ
// in the exit status, because on this form they cannot differ in the output.
func TestPathsAnswersNothingForAVerseHoldingNothing(t *testing.T) {
	serveUnreadable(t) // healthy: `on` is false
	out, err := runState(t, "git", "--paths")
	if err != nil {
		t.Fatalf("verse data git --paths on an empty verse: %v", err)
	}
	if out != "" {
		t.Errorf("an empty verse listed %q", out)
	}
}
