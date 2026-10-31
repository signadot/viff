package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/trigger"
)

// taskCharterFile is a package whose rule spawns as spawn says, with a second rule beside it.
func taskCharterFile(spawn string) string {
	return `at: repos.r.task
rules:
- name: build
  condition: {subject: {path: "repos.r.task.pr.*", value: {stage: open}}}
  action:
    patch: {stage: started}
    spawn: ` + spawn + `
- name: spare
  condition: {subject: {path: "repos.r.task.spare.*"}}
  action:
    patch: {seen: true}
`
}

// fired writes a pull request the rule build fires on, as the tester, and answers the record of
// the task it spawned for it.
func fired(t *testing.T, srv *Server, id string) entity.Ref {
	t.Helper()
	at := entity.RefAt("repos", "r", "task", "pr", id)
	if _, _, err := entity.As(srv.Store, "user:"+tester).Write(entity.Write{
		At: at.At(), Value: entity.Obj{"stage": "open"}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	return taskFor(t, srv, "build", at.String()).Ref
}

// launchedFor waits for verse to launch the task at ref: hosted, as only a spawn that passed
// every check before its launch is.
func launchedFor(t *testing.T, srv *Server, ref entity.Ref) bool {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if srv.hostedOf(ref.Name()).launched != nil {
			return true
		}
	}
	return false
}

// refusedFor waits for the task at ref to fail, and answers why, or "" where it did not.
func refusedFor(t *testing.T, srv *Server, ref entity.Ref) string {
	t.Helper()
	if taskStatus(t, srv, ref) != "failed" {
		return ""
	}
	rec, _, _ := srv.Store.Get(ref)
	return entity.Str(rec.Payload, "err")
}

// A TASK INSTALLS A CHARTER AS ITS OWNER, HELD WITHIN THE TASK, carried by its runner
// (verse-charter-install, -remove): each spawn among its rules within the task's environment less
// charter-install, under autonomy no looser, spending the task's credential, refused by the
// install and the check alike; and at each firing, bound, within the task as its record says it
// was launched, after the task has ended too. The owner installing the rule over it holds it to
// nothing the task was.
func TestATaskInstallsACharterHeldWithinTheTask(t *testing.T) {
	srv, ts, cluster := runnerServer(t)
	token := cluster.Token(iodAudience, runnerSub, nil)
	face := guardedFace(t, ts.URL, tokenFile(t, t.TempDir(), "gpu", token))
	resp := postTony(t, ts.URL+"/runners", map[string]any{"name": "gpu", "url": face.URL, "subject": runnerSub})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	resp = postTony(t, ts.URL+"/tasks", map[string]any{"id": "installer", "spawn": map[string]any{
		"script": "true",
		"environment": map[string]any{"systems": map[string]any{
			"verse":   map[string]any{},
			"network": map[string]any{"mode": "open"},
		}},
	}})
	if out := readBody(t, resp); resp.StatusCode/100 != 2 {
		t.Fatalf("spawn installer = %s: %s", resp.Status, out)
	}
	waitTaskStatus(t, srv, "installer", "running")
	if _, _, err := entity.As(srv.Store, "user:"+tester).Write(entity.Write{
		At: entity.RefAt("acme", "keys", "other").At(), Value: entity.Obj{"key": "vk-other"}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}

	for what, c := range map[string]struct{ spawn, want string }{
		"a system the task does not reach": {`{script: "true", environment: {systems: {commands: {run: {}}}}}`,
			"action.spawn.environment.systems.commands"},
		"installing charters": {`{script: "true", environment: {systems: {verse: {}}}}`, "charter-install"},
		"another network": {`{script: "true", environment: {systems: {network: {mode: none}}}}`,
			"action.spawn.environment.systems.network"},
		"looser autonomy":    {`{script: "true", autonomy: [{then: allow}]}`, "action.spawn.autonomy"},
		"another credential": {`{prompt: "go", virtualKey: acme.keys.other}`, "action.spawn.virtualKey"},
		"a hook beyond":      {`{script: "true"}` + "\n    onDone: {spawn: {script: \"true\", environment: {systems: {commands: {run: {}}}}}}", "action.onDone.spawn.environment"},
	} {
		file := taskCharterFile(c.spawn)
		if code, body := carryFor(t, http.MethodPost, ts.URL+"/charter?format=tony", token, "installer", file); code != http.StatusForbidden ||
			!strings.Contains(body, c.want) || strings.Contains(body, "vk-") {
			t.Errorf("the task installing a spawn on %s = %d %s, want 403 naming %s, and no key", what, code, body, c.want)
		}
		code, body := carryFor(t, http.MethodPost, ts.URL+"/charter/check?at=repos.r&format=tony", token, "installer", file)
		if code != http.StatusOK || !strings.Contains(body, "refused") || !strings.Contains(body, c.want) {
			t.Errorf("the task checking a spawn on %s = %d %s, want it refused naming %s", what, code, body, c.want)
		}
		if got := checkAt(t, ts, "repos.r", file); got.Verdict != "installs" {
			t.Errorf("its owner checking a spawn on %s = %+v, want it to install", what, got)
		}
	}

	file := taskCharterFile(`{script: "true", environment: {systems: {network: {mode: open}}}}`)
	if code, body := carryFor(t, http.MethodPost, ts.URL+"/charter?format=tony", token, "installer", file); code != http.StatusCreated {
		t.Fatalf("the task's install within it = %d %s", code, body)
	}
	rule := trigger.CharterRefAt("repos.r.task", "build")
	if rec, _, _ := srv.Store.Get(rule); entity.Str(rec.Payload, "task") != "installer" || entity.Get(rec.Payload, "taskEnvironment") == nil {
		t.Fatalf("the rule's record = %v, want it to name the task that installed it, and keep its hold", rec.Payload)
	}
	resp = getAs(t, "user:"+tester, ts.URL+"/charter?at=repos.r.task&format=tony")
	if read := readBody(t, resp); !strings.Contains(read, "task: installer") {
		t.Errorf("the charter reads %q, want its rule marked with the task that installed it", read)
	}
	if o, _ := entity.OwnerOf(srv.Store, rule); o != "user:"+tester {
		t.Errorf("the rule the task installed is %q's, want its owner's", o)
	}
	if code, body := carryFor(t, http.MethodDelete, ts.URL+"/charter?path=repos.r.task.rules.spare", token, "installer", ""); code/100 != 2 {
		t.Fatalf("the task's removal = %d %s", code, body)
	}
	if _, ok, _ := srv.Store.Get(trigger.CharterRefAt("repos.r.task", "spare")); ok {
		t.Error("the rule stands after the task removed it")
	}

	// The task ends; its rule stands, held within what it recorded at the install.
	if code, msg := control(t, ts, "/tasks/installer/cancel", "", map[string]any{"reason": "done here"}); code != http.StatusNoContent {
		t.Fatalf("cancel = %d %s", code, msg)
	}
	waitTaskStatus(t, srv, "installer", "cancelled")
	if !launchedFor(t, srv, fired(t, srv, "one")) {
		t.Error("a firing within the task was not launched")
	}
	// The task's record gone, as a scoped task's goes with its scope: the rule keeps its own hold,
	// and fires within it as before.
	if _, _, err := srv.Store.Write(entity.Write{At: mirror.Ref("installer").At()}); err != nil {
		t.Fatal(err)
	}
	if !launchedFor(t, srv, fired(t, srv, "two")) {
		t.Error("a firing within the task was not launched once the task's record was gone")
	}

	installCharter(t, ts, "", file)
	if rec, _, _ := srv.Store.Get(rule); entity.Get(rec.Payload, "task") != nil {
		t.Fatalf("installed by its owner, the rule's record = %v, still naming the task", rec.Payload)
	}
	if !launchedFor(t, srv, fired(t, srv, "three")) {
		t.Error("the owner's rule firing was not launched")
	}
}

// A FIRING IS HELD WITHIN WHAT ITS RULE RECORDED OF THE TASK AT THE INSTALL, bound: a repository
// the firing binds that the task never reached is refused, though written as `$…` it installed,
// and charter-install is not given whatever the task was. No record of the task is read.
func TestARulesFiringIsHeldWithinTheTaskThatInstalledIt(t *testing.T) {
	srv := testServer(t)
	node, err := codec.ParseNode([]byte(`{systems: {git: {repo: github.com/acme/app, fetch: {}}, verse: {}}}`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	var env systems.Environment
	if err := env.FromTonyIR(node); err != nil {
		t.Fatal(err)
	}
	hands := (&launched{environment: env}).handsOn()
	rule := trigger.Trigger{Name: "build", Task: "installer", TaskEnvironment: &hands}
	held := func(rule trigger.Trigger, spec launch.Spec) error { return srv.heldByItsTask(rule, spec) }
	spawn := func(systems string) launch.Spec {
		t.Helper()
		node, err := codec.ParseNode([]byte(`{script: "true", environment: {systems: `+systems+`}}`), codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		var spec launch.Spec
		if err := codec.FromNode(node, &spec); err != nil {
			t.Fatal(err)
		}
		return spec
	}
	if err := held(rule, spawn(`{git: {repo: github.com/acme/app, fetch: {}}, verse: {data-get: {}}}`)); err != nil {
		t.Errorf("a firing within the task: %v", err)
	}
	for what, c := range map[string]struct{ systems, want string }{
		"another repository":  {`{git: {repo: github.com/other/app, fetch: {}}}`, "environment.systems.git"},
		"installing charters": {`{verse: {charter-install: {}}}`, "charter-install"},
	} {
		if err := held(rule, spawn(c.systems)); err == nil || !strings.Contains(err.Error(), c.want) ||
			!strings.Contains(err.Error(), "installed by task installer") {
			t.Errorf("a firing on %s: %v, want it refused naming %s and the task", what, err, c.want)
		}
	}
	if err := held(trigger.Trigger{Name: "build"}, spawn(`{git: {repo: github.com/other/app, fetch: {}}}`)); err != nil {
		t.Errorf("a rule no task installed: %v", err)
	}

	// A rule that names a task and keeps nothing of it holds nothing, and is refused.
	if err := held(trigger.Trigger{Name: "build", Task: "installer"}, spawn(`{git: {repo: github.com/acme/app, fetch: {}}}`)); err == nil ||
		!strings.Contains(err.Error(), "keeps nothing") {
		t.Errorf("a rule naming a task and keeping no hold: %v, want it refused", err)
	}
}

// A TASK'S CREDENTIAL IS HELD WHERE A SPAWN SPENDS ONE: a spawn with a prompt names the task's,
// and a script, which spends none, may name none, in what a task's charter spawns and in what
// follows its ask alike.
func TestATasksCredentialIsHeldWhereASpawnSpendsOne(t *testing.T) {
	srv := testServer(t)
	srv.host("keyed", hostedTask{scope: "dev", launched: &launched{
		environment: systems.Environment{Systems: systems.Systems{"verse": {{}}}}, virtualKey: "acme.keys.k",
	}})
	hold, err := srv.taskCharterFor("keyed")
	if err != nil {
		t.Fatal(err)
	}
	docs, err := codec.ParseNodes([]byte(taskCharterFile(`{script: "true"}`)), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, code, err := srv.installCharterPruning(entity.As(srv.Store, "user:"+tester), "", "", "", docs, hold, 0); err != nil {
		t.Fatalf("the install = %d %v", code, err)
	}
	if rec, _, _ := srv.Store.Get(trigger.CharterRefAt("repos.r.task", "spare")); entity.Str(rec.Payload, "task") != "keyed" ||
		entity.Get(rec.Payload, "taskEnvironment") == nil {
		t.Errorf("the rule's record = %v, want the task and what it holds the rule within", rec.Payload)
	}
	bound := srv.boundOf("keyed")
	for what, c := range map[string]struct {
		spawn string
		ok    bool
	}{
		"a script naming none":       {`{script: "true"}`, true},
		"a script naming the task's": {`{script: "true", virtualKey: acme.keys.k}`, true},
		"a script naming another":    {`{script: "true", virtualKey: acme.keys.other}`, false},
		"a prompt naming the task's": {`{prompt: "go", virtualKey: acme.keys.k}`, true},
		"a prompt naming none":       {`{prompt: "go"}`, false},
	} {
		node, err := codec.ParseNode([]byte(c.spawn), codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		var spec launch.Spec
		if err := codec.FromNode(node, &spec); err != nil {
			t.Fatal(err)
		}
		rule := trigger.Trigger{Name: "r", Action: trigger.Action{Spawn: &spec}}
		if err := hold.holds(rule); (err == nil) != c.ok {
			t.Errorf("a charter's spawn, %s: %v, want held %v", what, err, c.ok)
		}
		if err := bound.holds(spec); (err == nil) != c.ok {
			t.Errorf("an ask's spawn, %s: %v, want held %v", what, err, c.ok)
		}
	}
}

// WHAT FOLLOWS A TASK'S ASK INSTALLS NO CHARTER, on a yes or a no, nor anything an ask it raises
// on a yes does: each spawn is held within the task less charter-install, as a task's charter's
// is, and what else of verse the task reaches it may still hand on.
func TestATasksAskHandsOnNoCharterInstall(t *testing.T) {
	srv, ts, cluster := runnerServer(t)
	token := cluster.Token(iodAudience, runnerSub, nil)
	face := guardedFace(t, ts.URL, tokenFile(t, t.TempDir(), "gpu", token))
	resp := postTony(t, ts.URL+"/runners", map[string]any{"name": "gpu", "url": face.URL, "subject": runnerSub})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	resp = postTony(t, ts.URL+"/tasks", map[string]any{"id": "raiser", "spawn": map[string]any{
		"script":      "true",
		"environment": map[string]any{"systems": map[string]any{"verse": map[string]any{}}},
	}})
	if out := readBody(t, resp); resp.StatusCode/100 != 2 {
		t.Fatalf("spawn raiser = %s: %s", resp.Status, out)
	}
	waitTaskStatus(t, srv, "raiser", "running")
	const alice = "user:alice@example.com"
	spawn := func(verse string) string {
		return `{spawn: {script: "true", environment: {systems: {verse: ` + verse + `}}}}`
	}

	for what, follow := range map[string]string{
		"a then that reaches every verb": `then: ` + spawn(`{}`),
		"a then that installs":           `then: ` + spawn(`{charter-install: {}}`),
		"an else that installs":          `else: ` + spawn(`{charter-install: {}}`),
		"an ask's then that installs":    `then: {ask: {question: "And then?", to: [` + alice + `], then: ` + spawn(`{}`) + `}}`,
	} {
		code, body := raiseFor(t, ts.URL, token, "raiser", `{name: more, question: "More?", to: [`+alice+`], `+follow+`}`)
		if code != http.StatusBadRequest || !strings.Contains(body, "charter-install") {
			t.Errorf("%s = %d %s, want 400 naming charter-install", what, code, body)
		}
	}
	code, body := raiseFor(t, ts.URL, token, "raiser", `{name: rest, question: "The rest?", to: [`+alice+`], then: `+
		spawn(`{data-get: {}, charter-remove: {}}`)+`, else: `+spawn(`{ask-create: {}}`)+`}`)
	if code != http.StatusCreated {
		t.Errorf("a then and an else with the rest of verse = %d %s, want it raised", code, body)
	}
}

// A CHECK FOR A TASK VERSE HOLDS NO LAUNCH OF IS REFUSED AS A VERDICT, as every refusal of a check
// is, never as an error.
func TestACheckForATaskWithNoLaunchIsAVerdict(t *testing.T) {
	srv := testServer(t)
	r := httptest.NewRequest(http.MethodPost, "/charter/check?at=repos.r&format=tony", strings.NewReader(checkedPackage))
	r.Header.Set("Content-Type", "application/tony")
	r = relayingTask(r, "ghost")
	w := httptest.NewRecorder()
	srv.checkCharter(w, r)
	if body := w.Body.String(); w.Code != http.StatusOK || !strings.Contains(body, "refused") || !strings.Contains(body, "no launch") {
		t.Errorf("the check = %d %s, want a refused verdict naming the launch", w.Code, body)
	}
}
