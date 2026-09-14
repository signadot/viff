package versed

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/trigger"
)

// A request presents user:, agent: or runner:, or a bare email for a person. A name versed makes
// — via: a rule, system: — and a name the grammar does not read are refused at the door, before
// any check runs, and write nothing.
func TestTheDoorAdmitsOnlyWhatARequestMayPresent(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for _, p := range []string{"verse.principal.group.release", "verse.trigger.rule.x",
		"via:verse.trigger.rule.x", entity.SystemDefault, "coderunner", "group:release"} {
		resp := putAs(t, p, ts.URL, "demo.pr.1", entity.Obj{"stage": "open"}.Payload())
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("a request as %q: %s %s", p, resp.Status, body)
		}
		if _, ok, _ := srv.Store.Get(entity.RefAt("demo", "pr", "1")); ok {
			t.Fatalf("the refused request as %q wrote", p)
		}
	}
	if refused, _ := entity.PolicyOf(srv.Store).Refusals(); len(refused) != 0 {
		t.Errorf("a request refused at the door reached the check: %v", refused)
	}
	for i, p := range []string{"walter@example.com", "agent:harness", "runner:coderunner"} {
		at := entity.RefAt("demo", "pr", fmt.Sprint(i+2))
		resp := putAs(t, p, ts.URL, at.String(), entity.Obj{"stage": "open"}.Payload())
		body := readBody(t, resp)
		if resp.StatusCode/100 != 2 {
			t.Fatalf("a request as %q: %s %s", p, resp.Status, body)
		}
	}
	if got := owner(t, srv, entity.RefAt("demo", "pr", "2")); got != "user:walter@example.com" {
		t.Fatalf("a bare email is recorded as %q", got)
	}
	if _, ok, _ := srv.Store.Get(entity.UserRef("walter@example.com")); !ok {
		t.Fatal("the person is not listed under user")
	}
}

// An operator agent is a principal of its own kind, which its name carries: listed under
// verse.principal.agent and not as a user, and owning what it creates under its own name.
func TestAnAgentIsAPrincipalOfItsOwnKind(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	put, _ := codec.Marshal(api.PutEntity{At: "demo.pr.1", Payload: entity.Obj{"stage": "open"}.Payload()}, codec.Tony)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/entity", strings.NewReader(string(put)))
	req.Header.Set("Content-Type", "application/tony")
	req.Header.Set(api.PrincipalHeader, "agent:claude")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("the agent's put: %s", resp.Status)
	}
	if got := owner(t, srv, entity.RefAt("demo", "pr", "1")); got != "agent:claude" {
		t.Fatalf("owned by %q, want the agent", got)
	}
	if _, ok, _ := srv.Store.Get(entity.PrincipalRef(entity.AgentKind, "claude")); !ok {
		t.Fatal("the agent is not listed under its kind")
	}
	if _, ok, _ := srv.Store.Get(entity.UserRef("claude")); ok {
		t.Fatal("the agent was listed as a user")
	}
}

// Control is held: the task's owner has it, a stranger does not.
func TestControlIsHeldByTheTasksOwner(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	pr(t, srv, "1", "payments")
	spawnAs(t, "alice@example.com", ts.URL, "run-1", `change.pr."1"`, "")
	waitQuestion(t, srv, "run-1") // running, and blocked on its gate: there is something to cancel

	say := func(as string) int {
		body, _ := codec.Marshal(api.Say{Instruction: "carry on", By: as}, codec.Tony)
		resp := postAs(t, as, ts.URL+"/tasks/run-1/say", "application/tony", string(body))
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := say("bob@example.com"); code != http.StatusForbidden {
		t.Fatalf("a stranger steered the task: %d", code)
	}
	if code := say("alice@example.com"); code != http.StatusNoContent {
		t.Fatalf("the owner could not steer their task: %d", code)
	}
	// Pause and resume are control verbs like say: the runtime mount serves them only after
	// the same check.
	verb := func(as, op string) int {
		resp := postAs(t, as, ts.URL+"/tasks/run-1/"+op, "application/tony", "")
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, op := range []string{"pause", "resume"} {
		if code := verb("bob@example.com", op); code != http.StatusForbidden {
			t.Fatalf("a stranger's %s of the task: %d, want 403", op, code)
		}
		if code := verb("alice@example.com", op); code/100 != 2 {
			t.Fatalf("the owner's %s of their task: %d", op, code)
		}
	}
	if ok, _ := entity.Holds(srv.Store, "user:bob@example.com", entity.Control, mirror.Ref("run-1")); ok {
		t.Fatal("bob holds control")
	}
}

// The charter the deployment names is installed at start as the system: its rules are the
// system's, so what they create is the system's, and no request could have installed it so.
func TestTheSystemCharterIsInstalledAtStartAsTheSystem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.tony")
	if err := os.WriteFile(path, []byte(`{
		name: system-watch,
		condition: {subject: {system: demo, kind: pr, value: {stage: open}}},
		action: {patch: {stage: seen}},
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.SystemCharter = path
	srv.Start(t.Context())

	rule := trigger.CharterRef("system-watch")
	if _, ok, err := srv.Store.Get(rule); err != nil || !ok {
		t.Fatalf("the system charter was not installed (ok=%v err=%v)", ok, err)
	}
	if got := owner(t, srv, rule); got != "" {
		t.Fatalf("the rule record is owned by %q; the system is one owner, and its records carry none of their own", got)
	}
	if ok, _ := entity.Holds(srv.Store, entity.Via(rule), entity.OwnerLevel, entity.PermsRef(rule)); !ok {
		t.Fatal("a firing of the system's rule does not act as the system")
	}

	// And it fires: the subject a person put is patched by a rule that is nobody's but the
	// system's, under enforcement.
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp := putAs(t, "alice@example.com", ts.URL, "demo.pr.1", entity.Obj{"stage": "open"}.Payload())
	resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok, _ := srv.Store.Get(entity.RefAt("demo", "pr", "1")); ok && entity.Str(e.Payload, "stage") == "seen" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the system's rule never fired on alice's pr")
}
