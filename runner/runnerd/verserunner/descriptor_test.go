package verserunner

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// Work that did not leave is FINDABLE, and the descriptor is where you find it.
//
// Staging made workspaces disposable, which removed the thing an operator used to `cd` into.
// Replacing it with a log line would have traded a lost result for an unfindable one — the mirror
// is a directory named by a hash of a remote url, so "it is in the mirror" is not an answer. The
// record carries the command.
func TestUndeliveredWorkIsListedWithHowToGetItBack(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, &Config{Default: "e", Environments: []Environment{{Name: "e"}}})

	// Nothing held yet.
	if got := r.staged(); len(got) != 0 {
		t.Fatalf("a fresh runner claims to hold %v", got)
	}
	repos, err := workspace.FromSpecs(rw(origin))
	if err != nil {
		t.Fatal(err)
	}
	ws, err := r.mirrors.Open(context.Background(), "s9", repos, t.TempDir(), workspace.Author{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root, "svc", "work.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := ws.Salvage(context.Background(), task.Launch{ID: "s9"}); d.Err != nil {
		t.Fatal(d.Err)
	}

	got := r.staged()
	if len(got) != 1 {
		t.Fatalf("staged = %+v, want one entry", got)
	}
	w := got[0]
	if w.Task != "s9" || w.Repo != "svc" || w.Ref != workspace.StagedRef(workspace.StageHeld, "s9", "svc", "") {
		t.Errorf("entry does not identify the work: %+v", w)
	}
	if !strings.Contains(w.Why, "authorized") {
		t.Errorf("held work does not say why it is held: %q", w.Why)
	}
	// The recovery command names the mirror by its real path, because a hash of a url is not
	// something anybody can be expected to reconstruct.
	if !strings.Contains(w.Fetch, r.mirrors.Dir(origin)) || !strings.Contains(w.Fetch, w.Ref) {
		t.Errorf("the fetch command cannot be pasted: %q", w.Fetch)
	}
	// And the descriptor carries it, since that is the document an operator reads before
	// removing a runner — which would throw this away.
	rec := httptest.NewRecorder()
	r.Descriptor(rec, httptest.NewRequest("GET", "/runner", nil))
	if body := rec.Body.String(); !strings.Contains(body, "s9") || !strings.Contains(body, "undelivered") {
		t.Errorf("GET /runner does not report undelivered work:\n%s", body)
	}
}

// The descriptor publishes the sandbox THIS deployment runs tasks in, and the networks it can
// build, not the weakest one's.
func TestTheDescriptorReportsTheSandboxItWasStartedWith(t *testing.T) {
	cfg := &Config{Default: "e", Environments: []Environment{{Name: "e"}}}
	r := testRunner(t, cfg)
	r.h.Sandbox = runnerd.BwrapAt("/usr/bin/bwrap")

	rec := httptest.NewRecorder()
	r.Descriptor(rec, httptest.NewRequest("GET", "/runner", nil))
	body := rec.Body.String()
	// A bwrap runner builds a restricted network; reporting the host's floor here understated it
	// in the one document an operator reads before pointing a charter at it.
	for _, want := range []string{"sandbox: bwrap", "restricted"} {
		if !strings.Contains(body, want) {
			t.Errorf("a bwrap runner's descriptor lacks %q:\n%s", want, body)
		}
	}
}

// A runner configured with no environments still answers for itself: the descriptor is how an
// operator finds out what a runner is and holds, and a runner that has nothing holds nothing.
func TestTheDescriptorAnswersOnARunnerWithNoEnvironments(t *testing.T) {
	r := testRunner(t, &Config{})
	rec := httptest.NewRecorder()
	r.Descriptor(rec, httptest.NewRequest("GET", "/runner", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /runner = %d:\n%s", rec.Code, rec.Body.String())
	}
	if got := r.staged(); len(got) != 0 {
		t.Fatalf("a runner with no environments claims to hold %v", got)
	}
}
