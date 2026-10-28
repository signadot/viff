package versed

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// asTask makes one request as a runner carrying it for a task: the runner's bearer, the task, and
// the principal beside them, where one is named.
func asTask(t *testing.T, method, url, token, taskID, presented, account string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set(api.TaskHeader, taskID)
	if presented != "" {
		req.Header.Set(api.PrincipalHeader, presented)
	}
	if account != "" {
		req.Header.Set(api.ForPrincipalHeader, account)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// spawnOn starts a script task as the tester, on the runner named, or on this verse's own runtime
// where none is.
func spawnOn(t *testing.T, ts string, id, runner string) {
	t.Helper()
	spawn := map[string]any{"script": "true"}
	if runner != "" {
		spawn["runner"] = runner
	}
	resp := postTony(t, ts+"/tasks", map[string]any{"id": id, "spawn": spawn})
	if out := readBody(t, resp); resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("spawn %s = %s: %s", id, resp.Status, out)
	}
}

// A RUNNER CARRIES A REQUEST FOR A TASK IT HOSTS, AS THE TASK'S OWNER, through the task's record:
// the record names the runner's registration and the subject it proved, and the task is running.
// Anything else is refused, naming what failed, and only a runner's bearer names a task.
func TestARunnerRelaysTheOwnerOfATaskItHosts(t *testing.T) {
	srv, ts, cluster := runnerServer(t)
	token := cluster.Token(iodAudience, runnerSub, nil)
	face := guardedFace(t, ts.URL, tokenFile(t, t.TempDir(), "gpu", token))
	resp := postTony(t, ts.URL+"/runners", map[string]any{"name": "gpu", "url": face.URL, "subject": runnerSub})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	spawnOn(t, ts.URL, "on-gpu", "verse.runners.gpu")
	waitTaskStatus(t, srv, "on-gpu", "running")
	spawnOn(t, ts.URL, "at-home", "")
	whoami := ts.URL + "/whoami?format=tony"

	code, body := asTask(t, http.MethodGet, whoami, token, "on-gpu", "", "")
	who := tonyOf(t, body)
	if code != http.StatusOK || who["principal"] != "user:"+tester || who["by"] != runnerProvider || who["via"] != "runner:gpu" {
		t.Fatalf("whoami carried for on-gpu = %d %s, want user:%s by %s via runner:gpu", code, body, tester, runnerProvider)
	}
	if code, body := asTask(t, http.MethodGet, whoami, token, "on-gpu", "user:"+tester, ""); code != http.StatusOK {
		t.Errorf("naming the task's owner beside it = %d %s", code, body)
	}

	for what, c := range map[string]struct{ task, presented, account, want string }{
		"another principal beside the task": {"on-gpu", "user:mallory@example.com", "", "made as the task's owner"},
		"a task that does not stand":        {"nobody-ran-this", "", "", "no task nobody-ran-this stands"},
		"a task on another runtime":         {"at-home", "", "", "this verse's own runtime"},
		"an account beside the task":        {"on-gpu", "", "github:alice", "never an account"},
	} {
		if code, body := asTask(t, http.MethodGet, whoami, token, c.task, c.presented, c.account); code != http.StatusForbidden || !strings.Contains(body, c.want) {
			t.Errorf("%s = %d %s, want 403 saying %q", what, code, body, c.want)
		}
	}
	if code, body := asTask(t, http.MethodGet, whoami, "", "on-gpu", "user:"+tester, ""); code != http.StatusUnauthorized || !strings.Contains(body, "no bearer") {
		t.Errorf("a task named with no bearer = %d %s, want 401", code, body)
	}

	// It carries what a task's tools do on verse, and nothing else: a spawn or an edit of an ask
	// carried for the task would be the owner's act unheld.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/tasks"}, {http.MethodPatch, "/asks/anything"}, {http.MethodGet, "/tasks"},
	} {
		if code, body := asTask(t, c.method, ts.URL+c.path, token, "on-gpu", "", ""); code != http.StatusForbidden || !strings.Contains(body, "carries only what a task's tools do") {
			t.Errorf("%s %s carried for on-gpu = %d %s, want 403", c.method, c.path, code, body)
		}
	}
	// A refusal at the door says it is the door's, so the runner tells its fault from a no.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/tasks", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(api.TaskHeader, "on-gpu")
	if resp, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	} else if resp.Body.Close(); !strings.Contains(resp.Header.Get("WWW-Authenticate"), "insufficient_scope") {
		t.Errorf("the door's 403 says WWW-Authenticate %q, want insufficient_scope", resp.Header.Get("WWW-Authenticate"))
	}

	// Ended, it is carried for nobody.
	if code, msg := control(t, ts, "/tasks/on-gpu/cancel", "", map[string]any{"reason": "done here"}); code != http.StatusNoContent {
		t.Fatalf("cancel = %d %s", code, msg)
	}
	waitTaskStatus(t, srv, "on-gpu", "cancelled")
	if code, body := asTask(t, http.MethodGet, whoami, token, "on-gpu", "", ""); code != http.StatusForbidden || !strings.Contains(body, "not running") {
		t.Errorf("an ended task = %d %s, want 403 not running", code, body)
	}
}

// A REGISTRATION REWRITTEN UNDER THE SAME NAME WITH ANOTHER SUBJECT CARRIES NOTHING OF THE TASKS
// THE OLD ONE HOSTED: the task's record holds the subject the launch went to.
func TestARewrittenRegistrationRelaysNoTaskItDidNotHost(t *testing.T) {
	srv, ts, cluster := runnerServer(t)
	token := cluster.Token(iodAudience, runnerSub, nil)
	face := guardedFace(t, ts.URL, tokenFile(t, t.TempDir(), "gpu", token))
	resp := postTony(t, ts.URL+"/runners", map[string]any{"name": "gpu", "url": face.URL, "subject": runnerSub})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	spawnOn(t, ts.URL, "on-gpu", "verse.runners.gpu")
	waitTaskStatus(t, srv, "on-gpu", "running")

	const other = "system:serviceaccount:verse:other"
	body, err := codec.Marshal(map[string]any{"name": "gpu", "url": face.URL, "subject": other}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/runners?probe=false", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/tony")
	req.Header.Set(api.PrincipalHeader, "user:"+tester)
	rr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if out := readBody(t, rr); rr.StatusCode/100 != 2 {
		t.Fatalf("the registration could not be rewritten under its name (%s: %s)", rr.Status, out)
	}
	code, out := asTask(t, http.MethodGet, ts.URL+"/whoami?format=tony", cluster.Token(iodAudience, other, nil), "on-gpu", "", "")
	if code != http.StatusForbidden || !strings.Contains(out, "subject") {
		t.Errorf("the rewritten registration carrying on-gpu = %d %s, want 403 naming the subject", code, out)
	}
}

// raiseFor is POST /asks as a runner carrying it for a task: the runner's bearer, the task, and
// the ask as tony.
func raiseFor(t *testing.T, ts, token, taskID, ask string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts+"/asks?format=tony", strings.NewReader(ask))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/tony")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(api.TaskHeader, taskID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// A TASK RAISES AN ASK AS ITS OWNER, carried by its runner (verse-ask-create): the ask is the
// owner's, naming the task beside them; what its `then` spawns is held within the task, on its
// runner and with its credential; once raised, the owner changes it as any of theirs; it stands
// after the task ends, since versed waits on it as on any person's ask; and its `then` runs as
// the owner on a yes.
func TestATaskRaisesAnAskForItsOwnerHeldWithinTheTask(t *testing.T) {
	srv, ts, cluster := runnerServer(t)
	token := cluster.Token(iodAudience, runnerSub, nil)
	face := guardedFace(t, ts.URL, tokenFile(t, t.TempDir(), "gpu", token))
	resp := postTony(t, ts.URL+"/runners", map[string]any{"name": "gpu", "url": face.URL, "subject": runnerSub})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	spawnOn(t, ts.URL, "raiser", "verse.runners.gpu")
	waitTaskStatus(t, srv, "raiser", "running")
	const alice = "user:alice@example.com"

	// Beyond the task: it was given no network, and the then's spawn asks for one.
	code, body := raiseFor(t, ts.URL, token, "raiser", `{name: wide, question: "Open it up?", to: [`+alice+`],
		then: {spawn: {script: "true", runner: verse.runners.gpu, environment: {systems: {network: {mode: open}}}}}}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "environment") {
		t.Fatalf("a then reaching beyond the task = %d %s, want 400 naming the environment", code, body)
	}

	// Beyond the task: elsewhere than its runner, or spending another credential.
	if _, _, err := entity.As(srv.Store, "user:"+tester).Write(entity.Write{
		At: entity.RefAt("acme", "keys", "other").At(), Value: entity.Obj{"key": "vk-other"}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	for what, c := range map[string]struct{ spawn, want string }{
		"this verse's own runtime": {`{script: "true"}`, ".runner"},
		"another credential":       {`{prompt: "go", runner: verse.runners.gpu, virtualKey: acme.keys.other}`, ".virtualKey"},
	} {
		code, body := raiseFor(t, ts.URL, token, "raiser", `{name: off, question: "Off it?", to: [`+alice+`], then: {spawn: `+c.spawn+`}}`)
		if code != http.StatusBadRequest || !strings.Contains(body, c.want) || strings.Contains(body, "vk-") {
			t.Errorf("a then on %s = %d %s, want 400 naming %s, and no key", what, code, body, c.want)
		}
	}

	// Raised, the ask is the owner's to change, beyond the task too.
	code, body = raiseFor(t, ts.URL, token, "raiser", `{name: mine, question: "Mine?", to: [`+alice+`],
		then: {spawn: {script: "true", runner: verse.runners.gpu}}}`)
	if code != http.StatusCreated {
		t.Fatalf("a raise to change = %d %s", code, body)
	}
	mine, _ := tonyOf(t, body)["id"].(string)
	posed(t, srv, mirror.AskRef(mine))
	if resp := change(t, "user:"+tester, ts.URL, mine, `{then: {spawn: {script: "true", environment: {systems: {network: {mode: open}}}}}}`); resp.StatusCode/100 != 2 {
		t.Errorf("its owner changing a task's raise = %s: %s", resp.Status, bodyOf(t, resp))
	}

	code, body = raiseFor(t, ts.URL, token, "raiser", `{name: land, question: "Land it?", to: [`+alice+`],
		then: {spawn: {script: "true", runner: verse.runners.gpu}}}`)
	if code != http.StatusCreated {
		t.Fatalf("a raise within the task = %d %s", code, body)
	}
	id, _ := tonyOf(t, body)["id"].(string)
	land := mirror.AskRef(id)
	posed(t, srv, land)
	rec, _, _ := srv.Store.Get(land)
	if from := entity.Get(rec.Payload, mirror.FromField); entity.Str(from, "principal") != "user:"+tester || entity.Str(from, "task") != "raiser" {
		t.Fatalf("the ask's from = %v, want the owner and the task", entity.ToObj(from))
	}

	// The task ends; the ask is the owner's, and stands.
	if code, msg := control(t, ts, "/tasks/raiser/cancel", "", map[string]any{"reason": "done here"}); code != http.StatusNoContent {
		t.Fatalf("cancel = %d %s", code, msg)
	}
	waitTaskStatus(t, srv, "raiser", "cancelled")
	if got := waitField(t, srv, land, "status"); got != mirror.QuestionPending {
		t.Fatalf("after its task ended the ask is %q, want still pending", got)
	}

	if resp := answerAs(t, alice, ts.URL, id); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alice's answer: %s: %s", resp.Status, bodyOf(t, resp))
	}
	then := thenTaskOf(t, srv, land)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if o, _ := entity.OwnerOf(srv.Store, then.Ref); o == "user:"+tester {
			break
		}
		if time.Now().After(deadline) {
			o, _ := entity.OwnerOf(srv.Store, then.Ref)
			t.Fatalf("its then is %q's, want the task's owner's", o)
		}
	}
}
