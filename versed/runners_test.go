package versed

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/systems/systemstest"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// a runner face an operator can actually install: the verse-facing contract, served by the
// same runnerd handler the local runner calls in process, proving itself to the verse at
// verseURL.
func runnerFace(t *testing.T, verseURL string) *face {
	t.Helper()
	h, release := runnerFaceHandler()
	return provenFace(t, verseURL, h, release)
}

// bareFace is a runner face with no door, answering no token: one registered unprobed, or one
// that cannot prove itself.
func bareFace(t *testing.T) *httptest.Server {
	t.Helper()
	h, release := runnerFaceHandler()
	return faceServing(t, h, release)
}

// runnerFaceHandler is the face's handler, and what releases the work on it, for a face served
// behind something (a runner's door, runnerauth_test.go) as well as bare.
func runnerFaceHandler() (http.Handler, func()) {
	// Released at cleanup, so the work on this face ENDS before the face does. Without it a
	// task that never finishes holds the mirror's event stream open, and httptest.Close
	// waits on outstanding requests — the test hangs on its own teardown rather than failing.
	release := make(chan struct{})
	build := func(l runner.Launch, _ *task.Registry) *task.Task {
		sup := systemstest.Opened(task.NewSupervisor(l.Environment, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{}), l.Environment)
		return task.NewTask(l.ID, sup, &blockedDriver{release: release}, okExecutor{})
	}
	return offeringAll(runnerd.NewInProcessHandler(build), "fake").HTTPHandler(), func() { close(release) }
}

// faceServing serves h as a runner face for the test, releasing its work before it closes.
func faceServing(t *testing.T, h http.Handler, release func()) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(func() {
		release()
		srv.Close()
	})
	return srv
}

// offeringAll is h describing itself as a runner verse registers must: offering the agents
// named, each holding a conversation and intercepting every system with nothing over it, and
// scripts; every offer building every network.
func offeringAll(h *runnerd.InProcessHandler, agents ...string) *runnerd.InProcessHandler {
	held := map[string]runner.System{}
	for _, doc := range systems.All() {
		if doc.Name == "network" {
			// What it builds, every network, and no ceiling: an unwritten network ceiling
			// bounds nothing, where Open would allow no connection.
			held[doc.Name] = builds(runner.NetworkOpen, runner.NetworkNone, runner.NetworkAllow)
			continue
		}
		held[doc.Name] = runner.System{Ceiling: systems.Open()}
	}
	d := runner.Description{Version: runner.DescriptionVersion}
	for _, a := range agents {
		d.Offers = append(d.Offers, runner.Offer{Agent: &runner.AgentOffer{Name: a, Settings: map[string]runner.Setting{"model": {}, "effort": {}}},
			Converse: true, Systems: held})
	}
	h.Describe(runner.Description{Version: d.Version, Offers: append(d.Offers, runner.Offer{Script: true, Systems: held})})
	return h
}

// builds is the network as a runner holds it that builds these modes and has no ceiling over it.
func builds(modes ...string) runner.System {
	return runner.System{Builds: systems.Ceiling{{Attributes: map[string][]string{"mode": modes}}}}
}

// a driver that never finishes on its own, so a task stays live long enough to be work that
// is ON a runner when the runner is removed.
//
// It keeps asking for another step rather than blocking inside Next, because a cancel is
// noticed at the loop's BOUNDARY: a driver parked in Next is one the loop never gets back to,
// so cancelling it would produce no terminal at all. This is the shape of the real thing — work
// that is going, and stops at the next hinge.
type blockedDriver struct{ release chan struct{} }

func (d *blockedDriver) Next(ctx context.Context, _ task.StepResult) (task.ToolUse, bool, error) {
	select {
	case <-d.release:
		return task.ToolUse{}, false, nil // the face is going away; finish
	case <-ctx.Done():
	case <-time.After(5 * time.Millisecond):
	}
	return task.ToolUse{Name: "noop"}, true, nil
}

type okExecutor struct{}

func (okExecutor) Execute(_ context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	return task.StepResult{Use: tu}, nil, nil
}

// An operator can install one, list it, and read it back. The address is the one they typed.
func TestRunnerAddListShow(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	face := runnerFace(t, ts.URL)
	defer ts.Close()

	resp := postTony(t, ts.URL+"/runners", map[string]any{
		"name": "gpu", "url": face.URL, "subject": face.Subject,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s, want 201", resp.Status)
	}
	var got map[string]any
	decodeResp(t, resp, &got)
	if got["at"] != "verse.runners.gpu" {
		t.Fatalf("at = %v, want runners.gpu", got["at"])
	}
	// The probe ran and said what it found, rather than the install being a silent write.
	if got["reach"] != "a runner" {
		t.Fatalf("reach = %v, want \"a runner\" — a live runner face read as something else", got["reach"])
	}

	lr, err := ts.Client().Get(ts.URL + "/runners")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var list []map[string]any
	decodeResp(t, lr, &list)
	if len(list) != 1 || list[0]["at"] != "verse.runners.gpu" || list[0]["url"] != face.URL {
		t.Fatalf("list = %+v, want the one installed runner with its url", list)
	}

	sr, err := ts.Client().Get(ts.URL + "/runners/gpu")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	var one map[string]any
	decodeResp(t, sr, &one)
	if one["url"] != face.URL {
		t.Fatalf("show = %+v, want the url", one)
	}
}

// The checks that are real from the first commit: an address that cannot be addressed, and a
// url that is not one. Each refusal quotes what it refused, so the operator sees their typo.
func TestRunnerAddRefusesWhatItCannotHonour(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, tc := range []struct {
		name string
		body map[string]any
		want string
	}{
		{"no url", map[string]any{"name": "gpu"}, "url is required"},
		{"url does not parse", map[string]any{
			"name": "gpu", "url": "gpu-pool:9797",
		}, "gpu-pool:9797"},
		// A runner's address is its name. A manifest that still files it under a kind is told
		// which field it is, rather than installing with the field dropped in silence.
		{"a kind", map[string]any{"name": "gpu", "kind": "pool", "url": "http://x:1"}, `no field "kind"`},
		{"slash in name", map[string]any{
			"name": "a/b", "url": "http://x:1",
		}, "may not contain /"},
		{"unknown field", map[string]any{
			"name": "gpu", "ulr": "http://x:1",
		}, "has no field"},
		// A runner is never taken at its word.
		{"no subject", map[string]any{"name": "gpu", "url": "http://x:1"}, "never taken at its word"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := postTony(t, ts.URL+"/runners?probe=false", tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("add = %s, want 400", resp.Status)
			}
			msg := readBody(t, resp)
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("refusal = %q, want it to name %q", msg, tc.want)
			}
		})
	}
}

// The probe's two dispositions, and the reason they differ: whether waiting could change the
// answer. A url that answers as something ELSE is answering now, while a url nothing answers is the ordinary state of a runner deployed
// alongside the entity that names it.
func TestRunnerAddProbeRefusesOnlyWhatWaitingCannotFix(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Answering, and not as a runner: a mux with no GET /tasks.
	worker := httptest.NewServer(http.NewServeMux())
	defer worker.Close()
	resp := postTony(t, ts.URL+"/runners", map[string]any{
		"name": "wrong", "url": worker.URL, "subject": someSubject(t),
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("add of a non-runner url = %s, want 400", resp.Status)
	}
	if msg := readBody(t, resp); !strings.Contains(msg, "not a runner") {
		t.Fatalf("refusal = %q, want it to say what it found", msg)
	}

	// Nothing answering: installed, with the warning on the response — the operator running
	// the command is the one who can act on it.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	resp = postTony(t, ts.URL+"/runners", map[string]any{
		"name": "pending", "url": deadURL, "subject": someSubject(t),
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add of an unreachable url = %s, want 201 — refusing it would make deployment "+
			"order-dependent for a fact that fixes itself", resp.Status)
	}
	var got map[string]any
	decodeResp(t, resp, &got)
	if got["reach"] != "unreachable" || got["warning"] == nil {
		t.Fatalf("add = %+v, want it installed and SAYING it could not be reached", got)
	}

	// And --no-probe installs the one the probe would have refused.
	resp = postTony(t, ts.URL+"/runners?probe=false", map[string]any{
		"name": "anyway", "url": worker.URL, "subject": someSubject(t),
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("--no-probe add = %s, want 201: a check with no way past it is a trap", resp.Status)
	}
}

// check reports the three apart, on demand, without recording any of it.
func TestRunnerCheckReportsAndRecordsNothing(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	face := runnerFace(t, ts.URL)
	defer ts.Close()

	postTony(t, ts.URL+"/runners", map[string]any{
		"name": "gpu", "url": face.URL, "subject": face.Subject,
	}).Body.Close()

	resp, err := ts.Client().Get(ts.URL + "/runners/gpu/check")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	var got map[string]any
	decodeResp(t, resp, &got)
	if got["reach"] != "a runner" {
		t.Fatalf("check = %+v, want reach \"a runner\"", got)
	}
	// Answered live, printed, discarded: the entity is what it was.
	e, ok, _ := srv.Store.Get(runnerEntityRef("gpu"))
	if !ok {
		t.Fatal("the runner entity vanished")
	}
	rec := entity.ToObj(e.Payload)
	if _, stored := rec["reach"]; stored {
		t.Fatalf("the store recorded health: %+v — a reachability column that is remembered is "+
			"one that can be stale in the direction that costs", rec)
	}
}

// Removal never guesses what to do with running work. It refuses until told which, and the
// message names both flags — because the difference is an intent verse cannot read from a
// delete, and the operator is present.
func TestRunnerRemoveRefusesUntilToldWhich(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	face := runnerFace(t, ts.URL)
	defer ts.Close()

	postTony(t, ts.URL+"/runners", map[string]any{
		"name": "gpu", "url": face.URL, "subject": face.Subject,
	}).Body.Close()

	// A task running ON it. Spawned through the runner face and mirrored the way a spawn
	// does, which is what makes it work this removal has to answer for.
	p := client.HTTP(face.Bare)
	p.Launch(runner.Launch{ID: "w1", Script: "long work"})
	setInstance(t, srv, "w1", entity.Obj{
		"status": "running", "runner": face.URL, "runnerName": "verse.runners.gpu",
	})

	resp, err := ts.Client().Do(mustReq(t, http.MethodDelete, ts.URL+"/runners/gpu"))
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("remove with work on it = %s, want 409", resp.Status)
	}
	msg := readBody(t, resp)
	for _, want := range []string{"--cancel", "--leave", "verse.runners.gpu"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("refusal = %q, want it to name %q", msg, want)
		}
	}

	// --leave removes it and the work keeps running, still reachable on the url stamped on it.
	resp, err = ts.Client().Do(mustReq(t, http.MethodDelete, ts.URL+"/runners/gpu?leave=true"))
	if err != nil {
		t.Fatalf("remove --leave: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove --leave = %s, want 200 — --leave must always succeed, or a runner with "+
			"one long-lived task on it could never be removed", resp.Status)
	}
	if _, ok, _ := srv.Store.Get(runnerEntityRef("gpu")); ok {
		t.Fatal("--leave did not remove the entity")
	}
	if _, err := p.(task.Lister).TaskSnapshot("w1"); err != nil {
		t.Fatalf("the work stopped being reachable when the entity went: %v — the url is stamped "+
			"on the task precisely so removal cannot strand it", err)
	}
}

// And a removal with nothing running on it needs no flag: there is nothing to be wrong about.
func TestRunnerRemoveIdleNeedsNoFlag(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	face := runnerFace(t, ts.URL)
	defer ts.Close()

	postTony(t, ts.URL+"/runners", map[string]any{
		"name": "gpu", "url": face.URL, "subject": face.Subject,
	}).Body.Close()

	resp, err := ts.Client().Do(mustReq(t, http.MethodDelete, ts.URL+"/runners/gpu"))
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove of an idle runner = %s, want 200", resp.Status)
	}
}

// --cancel ends the work with a reason, and reports what it could NOT reach rather than
// counting it cancelled. An already-gone runner is why an operator removes the entity half the
// time, so that is the ordinary case: saying "cancelled 3" when three are unreachable is the
// same lie as recording a queued answer as delivered.
func TestRunnerRemoveCancelReportsWhatItCouldNotReach(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	face := runnerFace(t, ts.URL)
	defer ts.Close()

	postTony(t, ts.URL+"/runners", map[string]any{
		"name": "gpu", "url": face.URL, "subject": face.Subject,
	}).Body.Close()

	p := client.HTTP(face.Bare)
	h := p.Launch(runner.Launch{ID: "c1", Script: "long work"})
	setInstance(t, srv, "c1", entity.Obj{
		"status": "running", "runner": face.URL, "runnerName": "verse.runners.gpu",
	})
	ch, unsub := h.Subscribe()
	defer unsub()

	resp, err := ts.Client().Do(mustReq(t, http.MethodDelete,
		ts.URL+"/runners/gpu?cancel=gpu+pool+retired"))
	if err != nil {
		t.Fatalf("remove --cancel: %v", err)
	}
	var got map[string]any
	decodeResp(t, resp, &got)
	if ids, _ := got["cancelled"].([]any); len(ids) != 1 || ids[0] != "c1" {
		t.Fatalf("remove --cancel = %+v, want it to report c1 cancelled", got)
	}
	if _, claimed := got["unreachable"]; claimed {
		t.Fatalf("remove --cancel = %+v, want nothing reported unreachable", got)
	}
	// The work really ended, and its flow can see that it did.
	waitForStatus(t, ch, task.Cancelled)

	// Now the case that decides how honest this is: the runner is already gone. Its own url,
	// not the live one — two entities sharing a url are one physical runner, which is what
	// makes a rename an add and a remove of the same place.
	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	goneURL := gone.URL
	gone.Close()
	postTony(t, ts.URL+"/runners?probe=false", map[string]any{
		"name": "ghost", "url": goneURL, "subject": someSubject(t),
	}).Body.Close()
	setInstance(t, srv, "g1", entity.Obj{
		"status": "running", "runner": goneURL, "runnerName": "verse.runners.ghost",
	})

	resp, err = ts.Client().Do(mustReq(t, http.MethodDelete,
		ts.URL+"/runners/ghost?cancel=teardown"))
	if err != nil {
		t.Fatalf("remove --cancel of a gone runner: %v", err)
	}
	decodeResp(t, resp, &got)
	if ids, _ := got["unreachable"].([]any); len(ids) != 1 || ids[0] != "g1" {
		t.Fatalf("remove --cancel of a gone runner = %+v, want g1 reported as unreachable "+
			"rather than counted as cancelled", got)
	}
	if ids, _ := got["cancelled"].([]any); len(ids) != 0 {
		t.Fatalf("remove --cancel = %+v, want nothing counted cancelled", got)
	}
}

func setInstance(t *testing.T, srv *Server, id string, rec entity.Obj) {
	t.Helper()
	if _, _, err := srv.Store.Write(entity.Write{
		At: mirror.Ref(id).At(), Value: rec.Payload(),
	}); err != nil {
		t.Fatalf("writing the task:instance for %s: %v", id, err)
	}
}

func mustReq(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set(api.PrincipalHeader, tester)
	return req
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	return string(b)
}

func waitForStatus(t *testing.T, ch <-chan task.Event, want task.Status) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatalf("the stream ended without a %s terminal", want)
			}
			if ev.Kind == task.EventStatus && ev.Text == string(want) {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a %s terminal — the work never learned it was ended", want)
		}
	}
}

// Two tasks in one verse, running in two places. What a drive could not do before was use its
// own runner for SOME of the work — a second runner meant a second verse, and then everything
// went there. An agent only gpu offers goes to gpu; a script, which both offer, goes to the one
// carrying less, this verse's own.
func TestTwoTasksRunInTwoPlaces(t *testing.T) {
	srv := testServer(t)
	srv.Embedded.Describe(offering()) // scripts, building no network
	ts := httptest.NewServer(srv.Handler())
	face := runnerFace(t, ts.URL)
	defer ts.Close()

	postTony(t, ts.URL+"/runners", map[string]any{
		"name": "gpu", "url": face.URL, "subject": face.Subject,
	}).Body.Close()

	named := spawnOK(t, ts, map[string]any{"id": "on-gpu", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "t"}})
	deflt := spawnOK(t, ts, map[string]any{"id": "on-default", "spawn": map[string]any{"script": "true"}})

	// It really went there: the runner face has it, and this verse's own runner does not.
	remote := client.HTTP(face.Bare).(task.Lister)
	if _, err := remote.TaskSnapshot(named); err != nil {
		t.Fatalf("the agent only gpu offers did not reach it: %v", err)
	}
	if _, err := remote.TaskSnapshot(deflt); err == nil {
		t.Fatal("the script went to gpu, which carries a task, over this verse's own runner, which carries none")
	}
	if _, ok := srv.Embedded.SnapshotOf(deflt); !ok {
		t.Fatal("the script did not go to this verse's own runner")
	}
	if _, ok := srv.Embedded.SnapshotOf(named); ok {
		t.Fatal("the spawn for gpu's agent ALSO ran in-process")
	}

	// And each task records where it went, by url and by the name that was written.
	rec := instanceOf(t, srv, named)
	if rec["runner"] != face.URL {
		t.Fatalf("runner = %v, want the resolved url — it is what a restart can act on", rec["runner"])
	}
	if rec["runnerName"] != "verse.runners.gpu" {
		t.Fatalf("runnerName = %v, want the registration it went to", rec["runnerName"])
	}
	if got := instanceOf(t, srv, deflt)["runnerName"]; got != nil {
		t.Fatalf("runnerName = %v on a task this verse's own runner hosts, want absent", got)
	}
}

// A SPAWN NO RUNNER TAKES IS REFUSED, on both spawn paths, rather than run somewhere that does not
// offer what it asks. And a spawn that names a runner is refused for that: where it runs is
// verse's to say.
func TestASpawnNoRunnerTakesIsRefused(t *testing.T) {
	srv := testServer(t)
	srv.NoLocalRunner = true // and nothing is registered
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// The ad-hoc path.
	resp := postTony(t, ts.URL+"/tasks", map[string]any{"id": "nope", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "t"}})
	if msg := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(msg, "no runner offers the agent fake") {
		t.Fatalf("a spawn no runner takes = %s %q, want 400 saying no runner offers its agent", resp.Status, msg)
	}
	if _, ok := srv.Embedded.SnapshotOf("nope"); ok {
		t.Fatal("the refused spawn ran on this verse's own runner anyway")
	}
	if _, ok, _ := srv.Store.Get(mirror.Ref("nope")); ok {
		t.Fatal("the refused spawn was mirrored — nothing was started, so there is nothing to mirror")
	}
	resp = postTony(t, ts.URL+"/tasks", map[string]any{"id": "named", "spawn": map[string]any{"script": "t", "runner": "verse.runners.gpu"}})
	if msg := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(msg, `unknown field "runner"`) {
		t.Fatalf("a spawn naming a runner = %s %q, want 400 refusing the field", resp.Status, msg)
	}

	// The charter path, where the refusal has to travel as a failed spawn rather than as an
	// HTTP status, because there is nobody to answer.
	h := srv.spawnerFor(srv.Store, "", srv.Eng)("fired", launch.Spec{Script: "t"})
	if h == nil {
		t.Fatal("a refused charter spawn returned no handle at all")
	}
	if e := h.Err(); !strings.Contains(e, "no runner offers scripts") {
		t.Fatalf("failure = %q, want it to say no runner offers scripts", e)
	}
	if _, ok := srv.Embedded.SnapshotOf("fired"); ok {
		t.Fatal("a charter spawn no runner takes ran on this verse's own runner instead")
	}
}

func spawnOK(t *testing.T, ts *httptest.Server, body map[string]any) string {
	t.Helper()
	resp := postTony(t, ts.URL+"/tasks", body)
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("spawn %v = %s: %s", body, resp.Status, readBody(t, resp))
	}
	var sp struct {
		ID string `tony:"field=id"`
	}
	decodeResp(t, resp, &sp)
	if sp.ID == "" {
		t.Fatalf("spawn %v returned no id", body)
	}
	return sp.ID
}

func instanceOf(t *testing.T, srv *Server, id string) map[string]any {
	t.Helper()
	e, ok, err := srv.Store.Get(mirror.Ref(id))
	if err != nil || !ok {
		t.Fatalf("no task:instance for %s (err=%v)", id, err)
	}
	return entity.ToObj(e.Payload)
}

// Work outlives the verse that started it, and outlives the ENTITY that named its runner.
//
// This is the step that decides whether the feature is safe. A restart reconstructs the runner
// from the url stamped on the task, so removing the runner entity — which is what a drive
// does when it tears its runner down, and what step 1's --leave explicitly allows — cannot make
// running work unreconstructible.
func TestRestartReattachesEvenAfterTheRunnerIsRemoved(t *testing.T) {
	store := entitytest.Store(t)

	// A verse that spawns onto a named runner, then dies.
	first := issuing(t, NewWithStore(ScriptedBuilder, store))
	ts := httptest.NewServer(first.Handler())
	face := runnerFace(t, ts.URL)
	postTony(t, ts.URL+"/runners", map[string]any{
		"name": "gpu", "url": face.URL, "subject": face.Subject,
	}).Body.Close()
	id := spawnOK(t, ts, map[string]any{"id": "survivor", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "long work"}})
	ts.Close()

	// The drive tears the runner down: the ENTITY goes, the work does not. This is what
	// step 1's --leave does, reached here directly because the verse that served it is gone.
	if _, _, err := store.Write(entity.Write{At: runnerEntityRef("gpu").At(), Value: nil}); err != nil {
		t.Fatalf("removing the runner entity: %v", err)
	}

	// "verse restart": a new server over the same store.
	second := issuing(t, NewWithStore(ScriptedBuilder, store))
	second.reconcileTasks()

	rec := instanceOf(t, second, id)
	if st, _ := rec["status"].(string); st == string(task.Orphaned) {
		t.Fatal("the restart orphaned work that is still running — the runner entity was gone, " +
			"but the url on the task is what a restart reconstructs from")
	}
	// And it still says which runner it was named for, so an operator can still select it.
	if rec["runnerName"] != "verse.runners.gpu" {
		t.Fatalf("runnerName = %v after restart, want it carried through the re-attach — "+
			"Reflect replaces the payload, so what is not handed back is forgotten", rec["runnerName"])
	}

	// A NEW spawn for what only the removed runner offered is refused, though: the work that is
	// already there is reachable, and no runner offers its agent now.
	second.NoLocalRunner = true
	ts2 := httptest.NewServer(second.Handler())
	defer ts2.Close()
	resp := postTony(t, ts2.URL+"/tasks", map[string]any{"id": "later", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "t"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("spawn for the removed runner's agent = %s, want 400", resp.Status)
	}
}

// An operator can answer "where is this running" and "what is this runner carrying" without
// reading a charter.
func TestOperatorCanSeeWhere(t *testing.T) {
	srv := testServer(t)
	srv.Embedded.Describe(offering()) // scripts: with gpu carrying a task, a script goes here
	ts := httptest.NewServer(srv.Handler())
	face := runnerFace(t, ts.URL)
	defer ts.Close()

	postTony(t, ts.URL+"/runners", map[string]any{
		"name": "gpu", "url": face.URL, "subject": face.Subject,
	}).Body.Close()
	named := spawnOK(t, ts, map[string]any{"id": "there", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "t"}})
	here := spawnOK(t, ts, map[string]any{"id": "here", "spawn": map[string]any{"script": "true"}})

	// ps names the runner per task, and this verse's own reads `local` rather than blank.
	byID := map[string]map[string]any{}
	for _, m := range psList(t, ts, "") {
		byID[m["id"].(string)] = m
	}
	if got := byID[named]["runnerName"]; got != "verse.runners.gpu" {
		t.Fatalf("runnerName = %v, want the registration it went to", got)
	}
	if got := byID[here]["runner"]; got != "local" {
		t.Fatalf("runner = %v for this verse's own, want \"local\" rather than blank", got)
	}

	// --runner selects, on the registration's name.
	only := psList(t, ts, "verse.runners.gpu")
	if len(only) != 1 || only[0]["id"] != named {
		t.Fatalf("ps --runner = %+v, want just the one on that runner", only)
	}
	if local := psList(t, ts, "local"); len(local) != 1 || local[0]["id"] != here {
		t.Fatalf("ps --runner local = %+v, want just the one this verse hosts", local)
	}

	// `verse runner` counts what it is carrying, from the store.
	one := runnersList(t, ts, false)[0]
	if one["running"] != int64(1) || one["total"] != int64(1) {
		t.Fatalf("counts = %+v, want one running and one total", one)
	}
	if one["lastSpawn"] == nil {
		t.Fatalf("runners = %+v, want lastSpawn, so a runner nobody names is visible as such", one)
	}
	if _, probed := one["reach"]; probed {
		t.Fatalf("runners = %+v, want no reach column without --probe: bare, the columns are "+
			"what verse RECORDED", one)
	}

	// --probe adds what the runner says now, and records none of it.
	probed := runnersList(t, ts, true)[0]
	if probed["reach"] != "a runner" {
		t.Fatalf("runners --probe = %+v, want a live reach column", probed)
	}
	e, _, _ := srv.Store.Get(runnerEntityRef("gpu"))
	if rec := entity.ToObj(e.Payload); rec["reach"] != nil {
		t.Fatalf("the store recorded health: %+v", rec)
	}
}

// The rows come from the store, never from a fan-out over installed runners. One dead runner
// must not take out the whole list — in a feature whose point is that runners come and go, that
// would be the answer getting worse with every runner added.
func TestPsDoesNotFanOutOverRunners(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	face := runnerFace(t, ts.URL)
	defer ts.Close()

	postTony(t, ts.URL+"/runners", map[string]any{
		"name": "gpu", "url": face.URL, "subject": face.Subject,
	}).Body.Close()
	live := spawnOK(t, ts, map[string]any{"id": "live", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "t"}})

	// A second installed runner that is not there at all, carrying work of its own.
	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	goneURL := gone.URL
	gone.Close()
	postTony(t, ts.URL+"/runners?probe=false", map[string]any{
		"name": "ghost", "url": goneURL,
	}).Body.Close()
	setInstance(t, srv, "stranded", entity.Obj{
		"status": "running", "agent": map[string]any{"name": "fake"}, "prompt": "t", "runner": goneURL, "runnerName": "verse.runners.ghost",
	})

	ids := map[string]bool{}
	for _, m := range psList(t, ts, "") {
		ids[m["id"].(string)] = true
	}
	if !ids[live] || !ids["stranded"] {
		t.Fatalf("ps = %v, want both — the dead runner cost detail, not rows", ids)
	}
	// And it still answers for the dead runner by name, which is when an operator most wants it.
	if only := psList(t, ts, "verse.runners.ghost"); len(only) != 1 || only[0]["id"] != "stranded" {
		t.Fatalf("ps --runner on a dead runner = %+v, want its work", only)
	}
	// `task get` finds work on a named runner too, rather than 404ing on work that is running.
	resp, err := ts.Client().Get(ts.URL + "/tasks/stranded")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get of work on a named runner = %s, want 200", resp.Status)
	}
}

func psList(t *testing.T, ts *httptest.Server, runnerName string) []map[string]any {
	t.Helper()
	u := ts.URL + "/tasks"
	if runnerName != "" {
		u += "?runner=" + url.QueryEscape(runnerName)
	}
	resp, err := ts.Client().Get(u)
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	var out []map[string]any
	decodeResp(t, resp, &out)
	return out
}

func runnersList(t *testing.T, ts *httptest.Server, probe bool) []map[string]any {
	t.Helper()
	u := ts.URL + "/runners"
	if probe {
		u += "?probe=true"
	}
	resp, err := ts.Client().Get(u)
	if err != nil {
		t.Fatalf("verse:runners: %v", err)
	}
	var out []map[string]any
	decodeResp(t, resp, &out)
	if len(out) == 0 {
		t.Fatal("no runners listed")
	}
	return out
}
