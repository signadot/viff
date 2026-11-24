package main

import (
	"bytes"
	"crypto/rsa"
	"errors"
	"fmt"
	"go.viff.dev/viff/internal/viffd/viffdtest"
	"go.viff.dev/viff/pkg/runner/runnerd/runnerdtest"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/scott-cotton/cli"

	"go.viff.dev/viff/internal/viffd"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

// testAddr is where serve() put the viff this test talks to. rootCmd() resolves the
// address from the config layers every time it is built, so it has to be re-pointed after.
var testAddr string

// runCLI drives the real command tree against a real viff, in task: the args a person
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
		// A flag is not a subcommand. `viff runner --probe` descends to `runner`, which HAS
		// children (its verbs, see dispatch) — so without this the walk would look up
		// "--probe" as one and fail a command that works perfectly in a shell.
		if strings.HasPrefix(args[0], "-") {
			break
		}
		sub := cmd.FindSub(cc, args[0])
		if sub == nil {
			break // not a subcommand: it is an argument, and the command decides what it means
		}
		cmd, args = sub, args[1:]
	}
	// Run FIRST: `return out.String(), cmd.Run(...)` evaluates its operands left to right,
	// so it would snapshot the buffer before the command had written a thing.
	err := cmd.Run(cc, args)
	return out.String(), err
}

// runCLIStreams is runCLI keeping the two streams apart, for a command that SAYS something
// beside what it answers — `viff charter` notes on stderr where the rules are when baseline
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
			break // not a subcommand: it is an argument, and the command decides what it means
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

// serve stands up a viff over a real store and points the client at it.
func serve(t *testing.T) entity.Store {
	t.Helper()
	store := entitytest.Store(t)
	srv := viffdtest.Server(t, runnerdtest.Scripted, store)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	// An issuer at its own address, as `viff up` is where its config names none.
	if err := srv.SetIssuer(ts.URL, testIssuerKey(), "memory"); err != nil {
		t.Fatal(err)
	}
	testAddr = strings.TrimPrefix(ts.URL, "http://")
	return store
}

// testIssuerKey is the key every test viff signs with: one per package, since making one is
// slow.
var testIssuerKey = sync.OnceValue(func() *rsa.PrivateKey {
	key, err := viffd.NewIssuerKey()
	if err != nil {
		panic(err)
	}
	return key
})

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

	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", `{stage: open, env: prod}`); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := payloadOf(t, store, pr1); got["stage"] != "open" || got["env"] != "prod" {
		t.Fatalf("put: %+v", got)
	}

	// A second put naming one field leaves the rest alone.
	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", `{stage: reviewed}`); err != nil {
		t.Fatalf("merge put: %v", err)
	}
	if got := payloadOf(t, store, pr1); got["stage"] != "reviewed" || got["env"] != "prod" {
		t.Fatalf("merge did not preserve the rest: %+v", got)
	}

	// ONE ADDRESS, all the way down: the path writes inside the payload, which is what
	// `--at labels.urgent` used to spell.
	if _, err := runCLI(t, "data", "demo", "pr", "1", "labels", "urgent", "--put", "true"); err != nil {
		t.Fatalf("deep put: %v", err)
	}
	labels, _ := payloadOf(t, store, pr1)["labels"].(map[string]any)
	if labels["urgent"] != true {
		t.Fatalf("deep put did not land: %+v", payloadOf(t, store, pr1))
	}

	// and a path takes one field away, leaving the entity.
	if _, err := runCLI(t, "data", "demo", "pr", "1", "labels", "urgent", "--rm"); err != nil {
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
	if _, err := runCLI(t, "data", "demo", "pr", "1", "--rm"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if got := payloadOf(t, store, pr1); got != nil {
		t.Fatalf("entity survived rm: %+v", got)
	}
}

// What a put PRINTS is the entity as it now stands, not an echo of the request — the two
// differ the moment a write merges, which is the whole point. docs/tutorial/data.md shows this
// output, so it is pinned here rather than left to drift.
func TestEntityPutPrintsTheEntity(t *testing.T) {
	serve(t)
	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", `{stage: open}`); err != nil {
		t.Fatalf("put: %v", err)
	}
	out, err := runCLI(t, "data", "demo", "pr", "1", "--put", `{owner: ana}`)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	for _, want := range []string{`at: 'demo.pr."1"'`, "owner: ana", "stage: open"} {
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

	if _, err := runCLI(t, "data", "--update", "demo", "pr", "1", "--put", `{stage: open}`); err == nil {
		t.Fatal("--update on an absent entity should have been refused")
	}
	if got := payloadOf(t, store, pr1); got != nil {
		t.Fatalf("a refused --update wrote anyway: %+v", got)
	}

	if _, err := runCLI(t, "data", "--create", "demo", "pr", "1", "--put", `{stage: open}`); err != nil {
		t.Fatalf("--create on an absent entity: %v", err)
	}
	if _, err := runCLI(t, "data", "--create", "demo", "pr", "1", "--put", `{stage: hijacked}`); err == nil {
		t.Fatal("--create on an existing entity should have been refused")
	}
	if got := payloadOf(t, store, pr1); got["stage"] != "open" {
		t.Fatalf("a refused --create wrote anyway: %+v", got)
	}

	if _, err := runCLI(t, "data", "--expect", `{stage: open}`, "demo", "pr", "1", "--put", `{stage: reviewed}`); err != nil {
		t.Fatalf("--if on a matching entity: %v", err)
	}
	if _, err := runCLI(t, "data", "--expect", `{stage: open}`, "demo", "pr", "1", "--put", `{stage: deployed}`); err == nil {
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
	_, err := runCLI(t, "data", "--create", "--update", "demo", "pr", "1", "--put", `{stage: open}`)
	if err == nil {
		t.Fatal("--create --update should be a usage error")
	}
	if !strings.Contains(err.Error(), "opposites") {
		t.Fatalf("error should say why: %v", err)
	}
}

// A REFUSED REMOVAL IS NOT A REMOVAL, and `data --rm` said "removed" whatever came back.
//
// The case that matters is viff's own half: the entity routes refuse a write under `viff:*`
// with 403 — those records are written by the verbs that own them — and the CLI printed
// "removed mirror.task.instance:<id>" while the record stood there untouched. An operator
// cleaning up a store was told the opposite of the truth, and the store agreed with neither.
func TestStateRmReportsARefusal(t *testing.T) {
	serve(t)
	out, err := runCLI(t, "data", "mirror", "task", "instance", "run-nope", "--rm")
	if err == nil {
		t.Fatalf("a removal in viff's own half was reported as done: %q", out)
	}
	if !strings.Contains(err.Error(), "viff's own") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	if strings.Contains(out, "removed") {
		t.Errorf("it printed a removal anyway: %q", out)
	}
}

// And an ordinary removal still reads as one — including of something that was not there, which
// is the same state afterwards and the same sentence.
func TestStateRmStillRemoves(t *testing.T) {
	serve(t)
	if _, err := runCLI(t, "data", "demo", "rmme", "1", "--put", "{x: 1}"); err != nil {
		t.Fatalf("put: %v", err)
	}
	out, err := runCLI(t, "data", "demo", "rmme", "1", "--rm")
	if err != nil || !strings.Contains(out, "removed") {
		t.Fatalf("rm: %q %v", out, err)
	}
	if out, err := runCLI(t, "data", "demo", "rmme", "404", "--rm"); err != nil || !strings.Contains(out, "removed") {
		t.Fatalf("rm of an absent entity: %q %v", out, err)
	}
}

// --grants ON A --put CREATES THE NODE HOLDING OUT WHAT IT SAYS, in the list `viff perms
// --grants` takes: bob, whom the open viff would admit, reads nothing of alice's note, carol
// whom it names reads it, and a node that stands is refused and told where its grants change.
// Beside a condition about a node that stands, or with no --put, it is a usage error and nothing
// is sent.
func TestEntityPutWithGrantsCreatesTheNodeHeldOut(t *testing.T) {
	store := serve(t)
	note := entity.RefAt("spaces", "alice", "note")
	t.Setenv("VIFF_USER", "alice@example.com")
	if _, err := runCLI(t, "data", "spaces", "alice", "note", "--put", `{n: 1}`, "--grants", `[{read: [carol@example.com]}]`); err != nil {
		t.Fatalf("put --grants: %v", err)
	}
	t.Setenv("VIFF_USER", "bob@example.com")
	if _, err := runCLI(t, "data", "spaces", "alice", "note"); err == nil {
		t.Error("bob read a note held out to carol alone")
	}
	if out, err := runCLI(t, "data", "spaces", "alice"); err != nil || strings.Contains(out, "note") {
		t.Errorf("bob's read of the place above answered the note: %v\n%s", err, out)
	}
	t.Setenv("VIFF_USER", "carol@example.com")
	if out, err := runCLI(t, "data", "spaces", "alice", "note"); err != nil || !strings.Contains(out, "n: 1") {
		t.Errorf("carol cannot read the note held out to her: %v\n%s", err, out)
	}

	t.Setenv("VIFF_USER", "alice@example.com")
	_, err := runCLI(t, "data", "spaces", "alice", "note", "--put", `{n: 2}`, "--grants", `[]`)
	if err == nil || !strings.Contains(err.Error(), "viff perms") {
		t.Errorf("--grants on a note that stands: %v, want a refusal naming viff perms", err)
	}
	if got := payloadOf(t, store, note); fmt.Sprint(got["n"]) != "1" {
		t.Errorf("the refused write changed the note: %+v", got)
	}

	for _, args := range [][]string{
		{"data", "spaces", "alice", "other", "--put", `{n: 1}`, "--grants", `[]`, "--update"},
		{"data", "spaces", "alice", "other", "--put", `{n: 1}`, "--grants", `[]`, "--expect", `{n: 1}`},
		{"data", "spaces", "alice", "other", "--rm", "--grants", `[]`},
		{"data", "spaces", "alice", "other", "--grants", `[]`},
	} {
		if _, err := runCLI(t, args...); !errors.Is(err, cli.ErrUsage) {
			t.Errorf("%q: %v, want a usage error", args, err)
		}
	}
	if got := payloadOf(t, store, entity.RefAt("spaces", "alice", "other")); got != nil {
		t.Errorf("a usage error wrote: %+v", got)
	}
}

// A VALUE THAT DOES NOT READ IS NOT A MISUSE OF THE COMMAND: a payload that does not parse is said
// as what it is, naming the payload, where flags that contradict each other are a usage error.
func TestAPayloadThatDoesNotReadIsNotAUsageError(t *testing.T) {
	serve(t)
	_, err := runCLI(t, "data", "demo", "pr", "1", "--put", "{stage: ")
	if err == nil || errors.Is(err, cli.ErrUsage) || !strings.HasPrefix(err.Error(), "data --put: payload: ") {
		t.Errorf("a payload that does not read = %v, want it said as one, and no usage error", err)
	}
	_, err = runCLI(t, "data", "demo", "pr", "1", "--put", "1", "--create", "--update")
	if !errors.Is(err, cli.ErrUsage) || !strings.Contains(err.Error(), "--create: create and update are opposites") {
		t.Errorf("--create with --update = %v, want a usage error naming --create", err)
	}
}
