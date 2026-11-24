package main

import (
	"go.viff.dev/viff/internal/viffd/viffdtest"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd/stock"
)

// serveKeyless is serve as a `viff up` with no model credential: the local runner runs
// scripts, offers no agent, and says so, as up publishes it.
func serveKeyless(t *testing.T) {
	t.Helper()
	cfg, secrets := stock.Config{Script: &stock.Script{}}, stock.Secrets{}
	srv := viffdtest.Offering(t, stock.New(cfg, secrets, t.TempDir(), io.Discard).Build, cfg.Describe([]string{runner.NetworkOpen}), entitytest.Store(t))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(ts.CloseClientConnections)
	testAddr = strings.TrimPrefix(ts.URL, "http://")
}

// AN ATTACHED SCRIPT RUNS: a conversation is an agent's, so a script run with --attach asks for
// none, and the runner, which holds none with a script, takes it. The local runner builds an
// open network alone, and the spawn says so.
func TestAnAttachedScriptRuns(t *testing.T) {
	serveKeyless(t)
	out, err := runCLI(t, "task", "run", "-f", spawnFile(t, `{script: "echo hi", environment: {systems: {network: {mode: open}}}}`), "--attach", "--as", "user:t")
	if err != nil {
		t.Fatalf("task run --attach: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hi") {
		t.Fatalf("the attached script's output is not in the stream:\n%s", out)
	}
}

// A PROMPT IS FOR VIFF'S DEFAULT AGENT when the run names none, and where no runner offers it
// the refusal names it, as it names an agent the run names.
func TestAPromptIsForViffsDefaultAgent(t *testing.T) {
	serveKeyless(t)
	_, err := runCLI(t, "task", "run", "-f", spawnFile(t, `{prompt: "fix the typo", environment: {systems: {network: {mode: open}}}}`), "--as", "user:t")
	if err == nil || !strings.Contains(err.Error(), "no runner offers the agent viff") {
		t.Fatalf("a prompt on a keyless viff: %v, want it refused naming the default agent", err)
	}
	_, err = runCLI(t, "task", "run", "-f", spawnFile(t, `{prompt: "fix the typo", agent: {name: codex}, environment: {systems: {network: {mode: open}}}}`), "--as", "user:t")
	if err == nil || !strings.Contains(err.Error(), "no runner offers the agent codex") {
		t.Fatalf("an agent codex on a keyless viff: %v, want it refused naming it", err)
	}
}

// THE SPAWN MEANS WHAT IT SAYS: a network it does not name is none, as in a charter, and the
// local runner, which builds open alone, refuses it and any other mode by name.
func TestTaskRunsNetworkIsTheSpawns(t *testing.T) {
	serveKeyless(t)
	for _, c := range []struct{ env, says string }{
		{"", "mode none: this runner builds open only"},
		{`environment: {systems: {network: {mode: none}}}`, "mode none: this runner builds open only"},
		{`environment: {systems: {network: {mode: allow, tcp-client: {host: api.github.com, port: 443}}}}`, "mode allow: this runner builds open only"},
		{openNet, ""},
	} {
		spec := "script: \"echo hi\"\n" + c.env + "\n"
		out, err := runCLI(t, "task", "run", "-f", spawnFile(t, spec), "--wait", "--as", "user:t")
		switch {
		case c.says == "" && err != nil:
			t.Errorf("%q: %v\n%s", c.env, err, out)
		case c.says != "" && (err == nil || !strings.Contains(err.Error()+out, c.says)):
			t.Errorf("%q: %v\n%s\nwant it refused saying %q", c.env, err, out, c.says)
		}
	}
}

// openNet is the environment of a run on the host's network, which the local runner builds.
const openNet = `environment: {systems: {network: {mode: open}}}`

// AN ASK'S THEN RUNS ON `viff up`: what follows a yes is the asker's run, a spawn as `task run`
// takes one, here on the host's network, which is the one the local runner builds.
func TestAnAsksThenRunsOnTheLocalRunner(t *testing.T) {
	cfg, secrets := stock.Config{Script: &stock.Script{}}, stock.Secrets{}
	store := entitytest.Store(t)
	srv := viffdtest.Offering(t, stock.New(cfg, secrets, t.TempDir(), io.Discard).Build, cfg.Describe([]string{runner.NetworkOpen}), store)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(ts.CloseClientConnections)
	testAddr = strings.TrimPrefix(ts.URL, "http://")

	id := askAs(t, victor, "land", "-q", "go?", "--to", victor, "--on", `[{match: {verdict: proceed}, then: {spawn: {script: "echo landed", environment: {systems: {network: {mode: open}}}}}}]`)
	ref := mirror.AskRef(id)
	if out, err := runCLI(t, "gate", questionOn(t, store, ref), "--answer", "proceed"); err != nil {
		t.Fatalf("answer: %v\n%s", err, out)
	}
	waitFieldCLI(t, store, ref, "ran.task")
	task := atPathCLI(payloadOf(t, store, ref), "ran.task").(string)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if why := atPathCLI(payloadOf(t, store, ref), "ran.failed"); why != nil {
			t.Fatalf("the then did not start: %v", why)
		}
		switch rec := payloadOf(t, store, mirror.Ref(task)); rec["status"] {
		case "done":
			return
		case "failed":
			t.Fatalf("the then's task failed: %v", rec["err"])
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the then's task %s never finished: %v", task, payloadOf(t, store, mirror.Ref(task)))
}

// A POLICY CHANGE IS A WHOLE POLICY, held as the runner holds a launch: on the local runner,
// `task autonomy <id> <rows>` replaces the run's autonomy and is taken, and a policy with a row that
// matches nothing the run's environment gives is refused by the runner, as a runner reached by
// url refuses it, and leaves the policy that stands. There is no dial: a bare `ask` is not a policy.
func TestAPolicyChangeIsAWholePolicy(t *testing.T) {
	serveKeyless(t)
	out, err := runCLI(t, "task", "run", "-f", spawnFile(t, `{script: "sleep 30", environment: {systems: {network: {mode: open}}}}`), "--as", "user:t")
	if err != nil {
		t.Fatalf("task run: %v\n%s", err, out)
	}
	id := strings.TrimSpace(out)
	t.Cleanup(func() { runCLI(t, "task", "cancel", id) })
	if out, err := runCLI(t, "task", "autonomy", id, "[{then: ask}]", "--as", "user:t"); err != nil {
		t.Fatalf("task autonomy: %v\n%s", err, out)
	}
	// The snapshot follows the task's stream, which takes the change a moment after the PUT.
	var got string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if got, err = runCLI(t, "task", "get", id); err == nil && strings.Contains(got, "then: ask") {
			break
		}
	}
	if err != nil || !strings.Contains(got, "then: ask") {
		t.Fatalf("after the change the policy should be the one given: %v\n%s", err, got)
	}
	bad := t.TempDir() + "/bad.tony"
	if err := os.WriteFile(bad, []byte("- {match: {system: commands, verb: run}, then: deny}\n- {then: allow}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "task", "autonomy", id, "--file", bad, "--as", "user:t"); err == nil ||
		!strings.Contains(err.Error(), "matches nothing the environment gives") {
		t.Fatalf("a policy naming commands.run, which the task's environment does not give: %v, want the runner to refuse it\n%s", err, out)
	}
	if out, err := runCLI(t, "task", "get", id); err != nil || !strings.Contains(out, "then: ask") {
		t.Fatalf("after a refused change the policy should be the one that stands: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "task", "autonomy", id, "allow", "--as", "user:t"); err == nil {
		t.Fatalf("a bare dial was taken as a policy:\n%s", out)
	}
	// A misspelled key is refused, not dropped: `mtch` dropped would make the row a default
	// that allows everything.
	if out, err := runCLI(t, "task", "autonomy", id, "[{mtch: {system: commands}, then: allow}]", "--as", "user:t"); err == nil {
		t.Fatalf("a row with a misspelled match was taken:\n%s", out)
	}
}
