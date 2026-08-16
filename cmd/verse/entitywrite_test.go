package main

import (
	"bytes"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/versed"
)

// testAddr is where serve() put the verse this test talks to. rootCmd() resolves the
// address from the config layers every time it is built, so it has to be re-pointed after.
var testAddr string

// runCLI drives the real command tree against a real verse, in task: the args a person
// would type, through flag parsing and the wire, onto a store. Returns stdout and the
// command's error.
//
// It walks to the leaf itself rather than calling Run on the root, because cli's Run
// os.Exit()s once a parent has dispatched — fine for a binary, fatal for a test task.
// The leaf parses its own flags, which is what is under test here.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cc := &cli.Context{
		Out: nopCloser{&out}, Err: nopCloser{&errOut},
		In: io.NopCloser(strings.NewReader("")), Go: t.Context(),
	}
	cmd := rootCmd()
	g.Addr = testAddr // after rootCmd, which re-reads it from the config layers
	for len(cmd.Children) > 0 && len(args) > 0 {
		// A flag is not a subcommand. `verse runner --probe` descends to `runner`, which HAS
		// children (its verbs, see dispatch) — so without this the walk would look up
		// "--probe" as one and fail a command that works perfectly in a shell.
		if strings.HasPrefix(args[0], "-") {
			break
		}
		sub := cmd.FindSub(cc, args[0])
		if sub == nil {
			t.Fatalf("no such command: %q", args[0])
		}
		cmd, args = sub, args[1:]
	}
	// Run FIRST: `return out.String(), cmd.Run(...)` evaluates its operands left to right,
	// so it would snapshot the buffer before the command had written a thing.
	err := cmd.Run(cc, args)
	return out.String(), err
}

// runCLIStreams is runCLI keeping the two streams apart, for a command that SAYS something
// beside what it answers — `verse trigger` notes on stderr where the rules are when baseline
// has none, and the note must not reach the document on stdout.
func runCLIStreams(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cc := &cli.Context{
		Out: nopCloser{&out}, Err: nopCloser{&errOut},
		In: io.NopCloser(strings.NewReader("")), Go: t.Context(),
	}
	cmd := rootCmd()
	g.Addr = testAddr
	for len(cmd.Children) > 0 && len(args) > 0 {
		if strings.HasPrefix(args[0], "-") {
			break
		}
		sub := cmd.FindSub(cc, args[0])
		if sub == nil {
			t.Fatalf("no such command: %q", args[0])
		}
		cmd, args = sub, args[1:]
	}
	err := cmd.Run(cc, args)
	return out.String(), errOut.String(), err
}

// nopCloser makes a buffer usable as the context's output, which is closable because a
// real one is a file.
type nopCloser struct{ *bytes.Buffer }

func (nopCloser) Close() error { return nil }

// serve stands up a verse over a real store and points the client at it.
func serve(t *testing.T) entity.Store {
	t.Helper()
	store := entitytest.Store(t)
	srv := versed.NewWithStore(versed.ScriptedBuilder, store)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	testAddr = strings.TrimPrefix(ts.URL, "http://")
	return store
}

func payloadOf(t *testing.T, s entity.Store, ref entity.Ref) map[string]any {
	t.Helper()
	e, ok, err := s.Get(ref)
	if err != nil {
		t.Fatalf("get %s: %v", ref, err)
	}
	if !ok {
		return nil
	}
	return entity.ToObj(e.Payload)
}

var pr1 = entity.RefAt("demo", "pr", "1")

// The write a person types: a whole entity, then one field of it, then a field removed —
// each without having to send the parts they are not talking about.
func TestEntityPutMergesAndAddressesAField(t *testing.T) {
	store := serve(t)

	if _, err := runCLI(t, "state", "put", "demo:pr:1", `{stage: open, env: prod}`); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := payloadOf(t, store, pr1); got["stage"] != "open" || got["env"] != "prod" {
		t.Fatalf("put: %+v", got)
	}

	// A second put naming one field leaves the rest alone.
	if _, err := runCLI(t, "state", "put", "demo:pr:1", `{stage: reviewed}`); err != nil {
		t.Fatalf("merge put: %v", err)
	}
	if got := payloadOf(t, store, pr1); got["stage"] != "reviewed" || got["env"] != "prod" {
		t.Fatalf("merge did not preserve the rest: %+v", got)
	}

	// ONE ADDRESS, all the way down: the path writes inside the payload, which is what
	// `--at labels.urgent` used to spell.
	if _, err := runCLI(t, "state", "put", "demo.pr.1.labels.urgent", "true"); err != nil {
		t.Fatalf("deep put: %v", err)
	}
	labels, _ := payloadOf(t, store, pr1)["labels"].(map[string]any)
	if labels["urgent"] != true {
		t.Fatalf("deep put did not land: %+v", payloadOf(t, store, pr1))
	}

	// and a path takes one field away, leaving the entity.
	if _, err := runCLI(t, "state", "rm", "demo.pr.1.labels.urgent"); err != nil {
		t.Fatalf("rm field: %v", err)
	}
	got := payloadOf(t, store, pr1)
	labels, _ = got["labels"].(map[string]any)
	if _, still := labels["urgent"]; still {
		t.Fatalf("field not removed: %+v", got)
	}
	if got["stage"] != "reviewed" {
		t.Fatalf("removing a field disturbed the entity: %+v", got)
	}

	// A path that stops at the entity removes the entity itself — and the ref spelling is that
	// same address, which is why it still works.
	if _, err := runCLI(t, "state", "rm", "demo:pr:1"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if got := payloadOf(t, store, pr1); got != nil {
		t.Fatalf("entity survived rm: %+v", got)
	}
}

// What a put PRINTS is the entity as it now stands, not an echo of the request — the two
// differ the moment a write merges, which is the whole point. docs/tutorial.md §2 shows this
// output, so it is pinned here rather than left to drift.
func TestEntityPutPrintsTheEntity(t *testing.T) {
	serve(t)
	if _, err := runCLI(t, "state", "put", "demo:pr:1", `{stage: open}`); err != nil {
		t.Fatalf("put: %v", err)
	}
	out, err := runCLI(t, "state", "put", "demo:pr:1", `{owner: ana}`)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	for _, want := range []string{"ref: demo:pr:1", "owner: ana", "stage: open"} {
		if !strings.Contains(out, want) {
			t.Errorf("put printed:\n%s\nwant it to contain %q", out, want)
		}
	}
}

// The guards: --create loses to whoever is already there, --update refuses to bring
// something into existence, and --if refuses when the world has moved on. A refused write
// is an error the person sees, and it leaves the entity as it was.
func TestEntityPutGuards(t *testing.T) {
	store := serve(t)

	if _, err := runCLI(t, "state", "put", "--update", "demo:pr:1", `{stage: open}`); err == nil {
		t.Fatal("--update on an absent entity should have been refused")
	}
	if got := payloadOf(t, store, pr1); got != nil {
		t.Fatalf("a refused --update wrote anyway: %+v", got)
	}

	if _, err := runCLI(t, "state", "put", "--create", "demo:pr:1", `{stage: open}`); err != nil {
		t.Fatalf("--create on an absent entity: %v", err)
	}
	if _, err := runCLI(t, "state", "put", "--create", "demo:pr:1", `{stage: hijacked}`); err == nil {
		t.Fatal("--create on an existing entity should have been refused")
	}
	if got := payloadOf(t, store, pr1); got["stage"] != "open" {
		t.Fatalf("a refused --create wrote anyway: %+v", got)
	}

	if _, err := runCLI(t, "state", "put", "--if", `{stage: open}`, "demo:pr:1", `{stage: reviewed}`); err != nil {
		t.Fatalf("--if on a matching entity: %v", err)
	}
	if _, err := runCLI(t, "state", "put", "--if", `{stage: open}`, "demo:pr:1", `{stage: deployed}`); err == nil {
		t.Fatal("--if should have been refused: the stage has moved on")
	}
	if got := payloadOf(t, store, pr1); got["stage"] != "reviewed" {
		t.Fatalf("a refused --if wrote anyway: %+v", got)
	}
}

// --create and --update are opposite answers to one question, so asking both is a
// contradiction — refused from the flags, before anything is sent.
func TestEntityPutRefusesContradictoryGuards(t *testing.T) {
	serve(t)
	_, err := runCLI(t, "state", "put", "--create", "--update", "demo:pr:1", `{stage: open}`)
	if err == nil {
		t.Fatal("--create --update should be a usage error")
	}
	if !strings.Contains(err.Error(), "opposites") {
		t.Fatalf("error should say why: %v", err)
	}
}
