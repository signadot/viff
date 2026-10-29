package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/systems/systemstest"
	"github.com/signadot/verse/task"
)

// runnerFace is a real runner for the client to install: the same runnerd handler verse's own
// in-process runner is, so the install probe has something truthful to find.
//
// It serves the verse serve started, behind the door runnerd puts in front of a runner, and
// holds a static token: tokenFile is where, for `verse runner add -token-file`.
func runnerFace(t *testing.T) (face *httptest.Server, tokenFile string) {
	t.Helper()
	build := func(l runner.Launch, _ *task.Registry) *task.Task {
		sup := systemstest.Opened(task.NewSupervisor(l.Environment, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{}), l.Environment)
		return task.NewTask(l.ID, sup, doneAfter{}, okExec{})
	}
	tokenFile = runnerTokenFile(t)
	g, err := runnerd.NewGuard(runnerd.Verse{URL: "http://" + testAddr}, tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(g.Wrap(offeringAll(runnerd.NewInProcessHandler(build)).HTTPHandler()))
	t.Cleanup(srv.Close)
	return srv, tokenFile
}

// runnerTokenFile is a file holding a fresh static token, long enough to be named by its hash.
func runnerTokenFile(t *testing.T) string {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(hex.EncodeToString(b[:])+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// offeringAll is h describing itself as a runner verse registers must: offering scripts, and
// building every network.
func offeringAll(h *runnerd.InProcessHandler) *runnerd.InProcessHandler {
	h.Describe(runner.Description{Version: runner.DescriptionVersion, Offers: []runner.Offer{{Script: true,
		Systems: map[string]runner.System{"network": {Builds: systems.Ceiling{{Attributes: map[string][]string{
			"mode": {runner.NetworkOpen, runner.NetworkNone, runner.NetworkAllow}}}}}}}}})
	return h
}

type doneAfter struct{}

func (doneAfter) Next(_ context.Context, _ task.StepResult) (task.ToolUse, bool, error) {
	return task.ToolUse{}, false, nil
}

type okExec struct{}

func (okExec) Execute(_ context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	return task.StepResult{Use: tu}, nil, nil
}

// `verse runner add` reaches the route, through the client the way a person types it.
//
// This exists because it did not. The flags rode in a path string — "/runners?probe=false" —
// and urlFor ESCAPES the path, so the request went to `/runners%3Fprobe=false` and 404d. Every
// test of the feature drove the HTTP layer directly, so all of them passed while the command
// an operator would run reached nothing at all. The lesson is the coverage, not the escaping: a
// client is a layer, and a layer with no test is a layer that does not work.
func TestRunnerAddThroughTheClient(t *testing.T) {
	store := serve(t)
	face, tok := runnerFace(t)

	// A runner is never taken at its word: an add says how verse recognises it.
	if _, err := runCLI(t, "runner", "add", "demo", face.URL); err == nil || !strings.Contains(err.Error(), "-token-file") {
		t.Fatalf("an add naming no subject = %v, want it refused naming -subject and -token-file", err)
	}
	out, err := runCLI(t, "runner", "add", "-token-file", tok, "demo", face.URL)
	if err != nil {
		t.Fatalf("runner add: %v", err)
	}
	if !strings.Contains(out, "verse.runners.demo") {
		t.Fatalf("add printed %q, want the ref it installed", out)
	}
	// The probe ran and said what it found — which is the half of `add` that only happens on
	// the way through the server.
	if !strings.Contains(out, "a runner") {
		t.Fatalf("add printed %q, want the probe's verdict", out)
	}
	if payloadOf(t, store, entity.VerseRef("runners", "demo")) == nil {
		t.Fatal("nothing was installed")
	}

	// …and --no-probe, which is the flag that was silently unreachable.
	dead := httptest.NewServer(http.NewServeMux()) // answers, and is not a runner
	defer dead.Close()
	if _, err := runCLI(t, "runner", "add", "-token-file", tok, "nope", dead.URL); err == nil {
		t.Fatal("a url that answers as something else installed; the probe did not run")
	}
	if _, err := runCLI(t, "runner", "add", "--no-probe", "-token-file", runnerTokenFile(t), "nope", dead.URL); err != nil {
		t.Fatalf("--no-probe: %v — the flag never reached the server", err)
	}
	if payloadOf(t, store, entity.VerseRef("runners", "nope")) == nil {
		t.Fatal("--no-probe did not install")
	}
}

// The rest of the verbs, over the client: what each one says is what the operator sees.
func TestRunnerVerbsThroughTheClient(t *testing.T) {
	store := serve(t)
	face, tok := runnerFace(t)
	if _, err := runCLI(t, "runner", "add", "-token-file", tok, "demo", face.URL); err != nil {
		t.Fatalf("add: %v", err)
	}

	if out, err := runCLI(t, "runner", "show", "demo"); err != nil ||
		!strings.Contains(out, face.URL) {
		t.Fatalf("show = %q, %v; want the url", out, err)
	}
	if out, err := runCLI(t, "runner", "check", "verse.runners.demo"); err != nil ||
		!strings.Contains(out, "a runner") {
		t.Fatalf("check = %q, %v; want a live verdict", out, err)
	}
	// The bare list records nothing about health; --probe adds it, and is the second flag
	// that has to survive the trip.
	if out, err := runCLI(t, "runners"); err != nil || strings.Contains(out, "reach") {
		t.Fatalf("runners = %q, %v; want no reach column without --probe", out, err)
	}
	if out, err := runCLI(t, "runners", "--probe"); err != nil ||
		!strings.Contains(out, "a runner") {
		t.Fatalf("runners --probe = %q, %v; want the live column", out, err)
	}

	// Removal, with work on it: refused until told which, and the message names both flags.
	writeInstance(t, store, "w1", entity.Obj{
		"status": "running", "runner": face.URL, "runnerName": "verse.runners.demo",
	})
	_, err := runCLI(t, "runner", "remove", "demo")
	if err == nil {
		t.Fatal("removed a runner with work on it without being told what to do with the work")
	}
	for _, want := range []string{"--cancel", "--leave"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal = %q, want it to name %q", err, want)
		}
	}
	if out, err := runCLI(t, "runner", "remove", "demo", "--leave"); err != nil ||
		!strings.Contains(out, "verse.runners.demo") {
		t.Fatalf("remove --leave = %q, %v; want it to succeed and say so", out, err)
	}
	if payloadOf(t, store, entity.VerseRef("runners", "demo")) != nil {
		t.Fatal("--leave did not remove the entity")
	}
}

func writeInstance(t *testing.T, s entity.Store, id string, rec entity.Obj) {
	t.Helper()
	if _, _, err := s.Write(entity.Write{At: mirror.Ref(id).At(), Value: rec.Payload()}); err != nil {
		t.Fatalf("writing the task:instance for %s: %v", id, err)
	}
}

// Extra positionals get a message about the arguments, not about a file.
//
// `runner add <ref> <url>` needs exactly two, and the fall-through reads args[0] as a FILENAME —
// so one extra argument produced "open pool:code: no such file or directory", which names a file
// the person never mentioned. The way it happens is a pasted command with a trailing `#` comment:
// interactive zsh does not strip those, so four more arguments arrive.
func TestRunnerAddNamesExtraArgumentsRatherThanAMissingFile(t *testing.T) {
	_, err := runCLI(t, "runner", "add", "code", "http://localhost:9802",
		"#", "prints:", "reach:", "a runner")
	if err == nil {
		t.Fatal("extra arguments were accepted")
	}
	if strings.Contains(err.Error(), "no such file") {
		t.Errorf("the failure still points at a file nobody named: %v", err)
	}
	for _, want := range []string{"arguments", "zsh"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not mention %q: %v", want, err)
		}
	}
}
