package versed

import (
	"errors"
	"fmt"
	"github.com/signadot/verse/runner/runnerd/runnerdtest"
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
	"github.com/signadot/verse/schedule"
	"github.com/signadot/verse/trigger"
)

// A request presents user:, agent: or runner:, or a bare email for a person. A name versed makes
// — via: a rule, or the system's own principal — and a name the grammar does not read are refused
// at the door, before any check runs, and write nothing.
func TestTheDoorAdmitsOnlyWhatARequestMayPresent(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for _, p := range []string{"verse.principal.group.release", "verse.trigger.rule.x",
		"via:verse.trigger.rule.x", entity.SystemDefault, "verse-runner", "group:release"} {
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
	// A runner presents its token, and its name alone is refused (runnerauth.go).
	if resp := putAs(t, "runner:verse-runner", ts.URL, "demo4.pr.4", entity.Obj{"stage": "open"}.Payload()); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a request as runner:verse-runner with no token: %s %s, want 401", resp.Status, readBody(t, resp))
	}
	// A PLACE EACH: what a request creates includes the place it brought into being, so two
	// principals writing into one `demo` would be the first of them owning the other's rows.
	for i, p := range []string{"walter@example.com", "agent:harness"} {
		at := entity.RefAt(fmt.Sprintf("demo%d", i+2), "pr", fmt.Sprint(i+2))
		resp := putAs(t, p, ts.URL, at.String(), entity.Obj{"stage": "open"}.Payload())
		body := readBody(t, resp)
		if resp.StatusCode/100 != 2 {
			t.Fatalf("a request as %q: %s %s", p, resp.Status, body)
		}
	}
	if got := owner(t, srv, entity.RefAt("demo2")); got != "user:walter@example.com" {
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
	// The record stands at the place the put made, and the row is the agent's through it.
	if got := owner(t, srv, entity.RefAt("demo")); got != "agent:claude" {
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
	spawnAs(t, "alice@example.com", ts.URL, "run-1", `change.pr."1"`)
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
	if code := say("alice@example.com"); code != http.StatusOK {
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
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {patch: {stage: seen}},
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.SystemCharter = path
	srv.Start(t.Context())

	rule := trigger.CharterRef("system-watch")
	if _, ok, err := srv.Store.Get(rule); err != nil || !ok {
		t.Fatalf("the system charter was not installed (ok=%v err=%v)", ok, err)
	}
	// ITS OWN RECORD, the system's. A write by the system records none, and the seed holds
	// `owner` beneath verse.trigger.rule out to everyone so that anybody may install a rule:
	// with no record to stop at, that grant reaches the system's rules too, and whoever rewrites
	// one has written what fires as the system.
	if got := owner(t, srv, rule); !entity.IsSystem(got) {
		t.Fatalf("the rule record is owned by %q, want the system's own record on it", got)
	}
	if ok, _ := entity.Holds(srv.Store, entity.Via(rule), entity.OwnerLevel, entity.PermsRef(rule)); !ok {
		t.Fatal("a firing of the system's rule does not act as the system")
	}
	if ok, _ := entity.Holds(srv.Store, "user:mallory@example.com", entity.ReadWrite, rule); ok {
		t.Fatal("a stranger holds read-write on the system's rule, and what she writes there fires as the system")
	}
	strangerMakesNothingIn(t, srv, rule)
	// …and the rules in the binary's own base charter are taken the same way.
	if ok, _ := entity.Holds(srv.Store, "user:mallory@example.com", entity.ReadWrite, trigger.CharterRef("calendar")); ok {
		t.Fatal("a stranger holds read-write on the base charter's calendar rule")
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

// strangerMakesNothingIn says the rows' `child-owner` line stops AT what the system made: a
// stranger may create neither the node nor anything beneath it, asked and then tried. The line
// reaches any depth beneath its row, and a node that stands is only safe from being TAKEN — so
// without a record at the node to stop the line, a child added inside it is a creation like any
// other, and is its creator's.
func strangerMakesNothingIn(t *testing.T, srv *Server, made entity.Ref) {
	t.Helper()
	const mallory = "user:mallory@example.com"
	inside := entity.RefAt(append(made.Segs(), "added-by-a-stranger")...)
	for _, at := range []entity.Ref{made, inside} {
		if ok, err := entity.MayCreate(srv.Store, mallory, at); err != nil || ok {
			t.Errorf("a stranger may create %s: %v %v", at, ok, err)
		}
	}
	_, _, err := entity.As(srv.Store, mallory).Write(entity.Write{
		At: inside.At(), Value: entity.Obj{"x": 1}.Payload(), Principal: mallory,
	})
	if !errors.Is(err, entity.ErrForbidden) {
		t.Errorf("a stranger's write inside %s: %v, want refused", made, err)
	}
	if who := owner(t, srv, inside); who != "" {
		t.Errorf("%s is recorded as %q's", inside, who)
	}
}

// WHAT THE SYSTEM MAKES IN ITS OWN HALF CARRIES THE SYSTEM'S RECORD. A write by the system
// records no owner, and the rows above hold `child-owner` beneath them out to everyone, at any
// depth — so a task the system's rule spawns and the calendar it derives would each be open
// INSIDE: not to be taken, since they stand, but to be added to, by anybody. The record at each
// stops the line there. They are read by the default the record copies, and by the row's own
// line.
func TestWhatTheSystemMakesInItsOwnHalfIsNotAnybodysToTake(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.tony")
	if err := os.WriteFile(path, []byte(`{
		name: system-spawn,
		condition: {subject: {path: "demo.sys.*", value: {stage: open}}},
		action: {spawn: {agent: {name: verse}, prompt: "sleep 5"}, patch: {stage: spawned}},
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t), WithPerms(Perms{ChildGrants: entity.Levels{}}))
	srv.SystemCharter = path
	srv.Zone = time.UTC
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	mallory := "user:mallory@example.com"

	resp := putAs(t, "alice@example.com", ts.URL, "demo.sys.1", entity.Obj{"stage": "open"}.Payload())
	resp.Body.Close()
	var taskRef entity.Ref
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(taskRef.Segs()) == 0 {
		ents, _ := entity.ListAtErr(srv.Store, []string{entity.VerseRoot, mirror.System, mirror.InstanceKind}, 4, nil)
		for _, e := range ents {
			taskRef = e.Ref
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(taskRef.Segs()) == 0 {
		t.Fatal("the system's rule spawned nothing")
	}
	if got := owner(t, srv, taskRef); !entity.IsSystem(got) {
		t.Errorf("the task the system's rule spawned carries %q's record, want the system's own", got)
	}
	for _, l := range []entity.Level{entity.ReadWrite, entity.Control} {
		if ok, _ := entity.Holds(srv.Store, mallory, l, taskRef); ok {
			t.Errorf("a stranger holds %s on the system's task", l)
		}
	}
	strangerMakesNothingIn(t, srv, taskRef)
	if err := entity.Claim(entity.As(srv.Store, mallory), taskRef, mallory); err == nil {
		if got := owner(t, srv, taskRef); got == mallory {
			t.Error("a stranger took the system's task")
		}
	}

	// The calendar: the system's, and everybody's to read even under a root that holds out
	// nothing. `time` is derived at the first minutely tick, which the start backfills.
	clock := entity.VerseRef(schedule.System, "time")
	if err := srv.tickAt("minutely", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("ticking the minute: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok, _ := srv.Store.Get(clock); ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok, _ := srv.Store.Get(clock); !ok {
		t.Fatal("the base charter's clock rule derived nothing from the minute's tick")
	}
	if got := owner(t, srv, clock); !entity.IsSystem(got) {
		t.Errorf("the clock carries %q's record, want the system's own", got)
	}
	strangerMakesNothingIn(t, srv, clock)
	if err := entity.Claim(entity.As(srv.Store, mallory), clock, mallory); err == nil {
		t.Error("a stranger took the clock")
	}
	if ok, _ := entity.Holds(srv.Store, mallory, entity.ReadWrite, clock); ok {
		t.Error("a stranger writes the clock")
	}
	if ok, err := entity.Holds(srv.Store, mallory, entity.Read, clock); err != nil || !ok {
		t.Errorf("the clock is not everybody's to read under a private default: %v %v", ok, err)
	}
}

// A NAME THE SYSTEM HAS NOT USED YET CANNOT BE RESERVED. `verse perms` is the one verb that puts
// a record at an address of the caller's choosing, and in verse's half a record for a node
// nothing stands at is a reservation: whatever the system makes there afterwards — the calendar
// the base charter derives at the hour, a kind nobody has installed into, the task a rule spawns
// under an id that is a function of what fired — would be the reserver's. And an id that does
// stand as somebody's is theirs: the system's rule does not run its task over it.
func TestANameTheSystemHasNotUsedYetCannotBeReserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.tony")
	if err := os.WriteFile(path, []byte(`{
		name: system-spawn,
		condition: {subject: {path: "demo.sys.*", value: {stage: open}}},
		action: {spawn: {agent: {name: verse}, prompt: "sleep 5"}, patch: {stage: spawned}},
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.SystemCharter = path
	srv.Zone = time.UTC
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	bob := "bob@example.com"

	for _, at := range []string{"verse.schedule.date", "verse.iods.cluster", "verse.resolvers.review", `verse.task.instance."some-rule@demo.pr.1"`} {
		resp := putBody(t, bob, ts.URL+"/perms", fmt.Sprintf(`{at: %q, grants: []}`, at))
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("bob's PUT /perms at %s, where nothing stands: %s, want 403", at, resp.Status)
		}
		ref, err := entity.ParseAddr(at)
		if err != nil {
			t.Fatal(err)
		}
		if got := owner(t, srv, ref); got != "" {
			t.Errorf("%s carries a record, %q's", at, got)
		}
	}
	// The world's half is where a place is taken ahead of what goes in it.
	resp := putBody(t, bob, ts.URL+"/perms", `{at: "bobs.place", grants: []}`)
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Errorf("bob taking a place of his own in the world's half: %s", resp.Status)
	}

	// The id a system rule's task takes is a function of what fired: learn it from one firing…
	tasks := func() map[string]entity.Entity {
		out := map[string]entity.Entity{}
		ents, _ := entity.ListAtErr(srv.Store, []string{entity.VerseRoot, mirror.System, mirror.InstanceKind}, 4, nil)
		for _, e := range ents {
			out[e.Ref.Name()] = e
		}
		return out
	}
	resp = putAs(t, "alice@example.com", ts.URL, "demo.sys.1", entity.Obj{"stage": "open"}.Payload())
	resp.Body.Close()
	var first string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && first == ""; time.Sleep(20 * time.Millisecond) {
		for id := range tasks() {
			first = id
		}
	}
	if !strings.Contains(first, "demo.sys.1") {
		t.Skipf("the rule's task id %q is not a function of its subject here; nothing to run under first", first)
	}
	// …and run a task under the next one before the rule does.
	second := strings.Replace(first, "demo.sys.1", "demo.sys.2", 1)
	resp = postAs(t, bob, ts.URL+"/tasks", "application/tony", fmt.Sprintf(`{id: %q, spawn: {script: "true"}}`, second))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("bob running a task under an id of his choosing: %s", resp.Status)
	}
	taskRef := mirror.Ref(second)
	if got := owner(t, srv, taskRef); got != "user:"+bob {
		t.Fatalf("the fixture is wrong: bob's task is %q's", got)
	}
	// ENDED, so that nothing but the record stands at the id: the runtime refuses a second task
	// under an id it is still running, and what is left once it has forgotten is the record.
	resp = postAs(t, bob, ts.URL+"/tasks/"+second+"/cancel", "application/tony", "")
	resp.Body.Close()
	var began string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && began == ""; time.Sleep(10 * time.Millisecond) {
		if e := tasks()[second]; entity.Str(e.Payload, "ended") != "" {
			began = entity.Str(e.Payload, "startedAt")
		}
	}
	if began == "" {
		t.Fatal("the fixture is wrong: bob's task never ended")
	}
	resp = putAs(t, "alice@example.com", ts.URL, "demo.sys.2", entity.Obj{"stage": "open"}.Payload())
	resp.Body.Close()
	time.Sleep(500 * time.Millisecond) // long enough for the firing to have spawned, had it
	if e := tasks()[second]; began == "" || entity.Str(e.Payload, "startedAt") != began {
		t.Errorf("the system's rule ran its task over bob's record, which began at %q: %v", began, entity.ToObj(e.Payload))
	}
	if got := owner(t, srv, taskRef); got != "user:"+bob {
		t.Errorf("after the rule fired, the task at bob's id is %q's", got)
	}
	// And bob, for his part, does not run one under the system's.
	resp = postAs(t, bob, ts.URL+"/tasks", "application/tony", fmt.Sprintf(`{id: %q, spawn: {prompt: "true"}}`, first))
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("bob running a task under the system's task's id: %s, want 409", resp.Status)
	}
}
