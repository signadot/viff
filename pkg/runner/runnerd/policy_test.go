package runnerd

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func send(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/tony")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// stream is what a task's event stream says within d: a running task's never ends by itself.
func stream(t *testing.T, url string, d time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body) // ends with the deadline; what came before it is the answer
	return string(b)
}

// A RUNNING TASK'S POLICY IS REPLACED with PUT /tasks/{id}/policy, checked as a launch's is, each
// row reaching the task's environment, which the run keeps, and the task's read plane says who
// replaced it. Once it
// has ended, it can't be.
func TestPutPolicyReplacesARunningTasksPolicy(t *testing.T) {
	srv := offering(t)
	tasks := srv.URL + "/tasks/"
	if code, msg := postLaunch(t, srv.URL, `{id: g1, agent: {name: viff}, prompt: go, environment: {systems: {files: {write: {}}}}, autonomy: [{match: {system: files, verb: write}, then: ask}, {then: ask}]}`); code != http.StatusCreated {
		t.Fatalf("launch = %d %s", code, msg)
	}
	for _, tc := range []struct {
		body string
		code int
		says string
	}{
		{`{autonomy: [{then: sometimes}]}`, http.StatusBadRequest, "not one of deny, ask, allow"},
		{`{autonomy: [{name: p, match: {system: git, verb: push}, then: ask}, {then: ask}]}`, http.StatusBadRequest, "autonomy[0]: matches nothing the environment gives (system=git verb=push)"},
		{`{autonomy: [{name: g, match: {system: files, verb: write, command: ls}, then: ask}, {then: ask}]}`, http.StatusBadRequest, "match.command: not a field"},
		{`{gates: []}`, http.StatusBadRequest, "unknown field"},
		{`{autonomy: [{name: migrations, match: {system: files, verb: write, path: !regexp "^db/"}, then: ask}, {then: allow}], by: "user:ana"}`, http.StatusNoContent, ""},
		// A verb no row names is still the environment's, and the default row decides it.
		{`{autonomy: [{then: allow}]}`, http.StatusNoContent, ""},
		{`{autonomy: [{match: {system: files, verb: write}, then: deny}, {then: allow}]}`, http.StatusNoContent, ""},
	} {
		code, msg := send(t, http.MethodPut, tasks+"g1/autonomy", tc.body)
		if code != tc.code || !strings.Contains(msg, tc.says) {
			t.Errorf("%s = %d %q, want %d saying %q", tc.body, code, msg, tc.code, tc.says)
		}
	}
	if code, _ := send(t, http.MethodPut, tasks+"nope/autonomy", `{}`); code != http.StatusNotFound {
		t.Errorf("an unknown task = %d, want 404", code)
	}
	events := stream(t, tasks+"g1/events?since=0", time.Second)
	if !strings.Contains(events, "kind: autonomy") || !strings.Contains(events, "user:ana") ||
		!strings.Contains(events, "2 row(s), default allow") {
		t.Errorf("the read plane doesn't say the policy changed:\n%s", events)
	}
}

// …and once the task has ended, its policy is refused with 410.
func TestPutPolicyOnAnEndedTaskIsGone(t *testing.T) {
	srv := offering(t)
	tasks := srv.URL + "/tasks/"
	postLaunch(t, srv.URL, `{id: g2, agent: {name: viff}, prompt: go}`)
	send(t, http.MethodPost, tasks+"g2/cancel", `{reason: done with it}`)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, snap := send(t, http.MethodGet, tasks+"g2", ""); strings.Contains(snap, "status: cancelled") {
			break
		}
	}
	if code, msg := send(t, http.MethodPut, tasks+"g2/autonomy", `{autonomy: [{then: allow}]}`); code != http.StatusGone {
		t.Errorf("an ended task = %d %q, want 410", code, msg)
	}
}

// A paused task says so on its snapshot, and running again once it is resumed.
func TestAPausedTaskSaysSo(t *testing.T) {
	srv := offering(t)
	tasks := srv.URL + "/tasks/"
	postLaunch(t, srv.URL, `{id: p1, agent: {name: viff}, prompt: go}`)
	status := func() string {
		_, snap := send(t, http.MethodGet, tasks+"p1", "")
		for _, line := range strings.Split(snap, "\n") {
			if s, ok := strings.CutPrefix(line, "status: "); ok {
				return s
			}
		}
		return ""
	}
	send(t, http.MethodPost, tasks+"p1/pause", "")
	if got := status(); got != "paused" {
		t.Fatalf("paused task's status = %q", got)
	}
	send(t, http.MethodPost, tasks+"p1/resume", "")
	if got := status(); got != "running" {
		t.Fatalf("resumed task's status = %q", got)
	}
}

// POST /tasks answers with the task's snapshot: 201 when it started it, 200 when it was already
// running.
func TestALaunchIsAnsweredWithTheTask(t *testing.T) {
	srv := offering(t)
	for _, want := range []int{http.StatusCreated, http.StatusOK} {
		code, body := postLaunch(t, srv.URL, `{id: l1, agent: {name: viff}, prompt: go}`)
		if code != want || !strings.Contains(body, "id: l1") || !strings.Contains(body, "status: running") {
			t.Fatalf("= %d\n%s\nwant %d and the task's snapshot", code, body, want)
		}
	}
}

// Pause, resume and cancel say who: on the task's events, and in a cancelled task's error.
func TestControlSaysWho(t *testing.T) {
	srv := offering(t)
	tasks := srv.URL + "/tasks/"
	postLaunch(t, srv.URL, `{id: w1, agent: {name: viff}, prompt: go}`)
	send(t, http.MethodPost, tasks+"w1/pause", `{by: "user:ana"}`)
	send(t, http.MethodPost, tasks+"w1/resume", `{by: "user:bob"}`)
	send(t, http.MethodPost, tasks+"w1/cancel", `{reason: enough, by: "user:cy"}`)
	events := stream(t, tasks+"w1/events?since=0", time.Second)
	for _, want := range []string{"text: paused", "user:ana", "text: running", "user:bob"} {
		if !strings.Contains(events, want) {
			t.Errorf("%q: not on the events:\n%s", want, events)
		}
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, snap := send(t, http.MethodGet, tasks+"w1", ""); strings.Contains(snap, "status: cancelled") {
			if !strings.Contains(snap, "cancelled by user:cy: enough") {
				t.Fatalf("the cancelled task's error:\n%s", snap)
			}
			return
		}
	}
	t.Fatal("the task wasn't cancelled")
}

// A cancel that gives no reason says so, rather than inventing one.
func TestACancelWithNoReasonSaysSo(t *testing.T) {
	srv := offering(t)
	tasks := srv.URL + "/tasks/"
	postLaunch(t, srv.URL, `{id: c0, agent: {name: viff}, prompt: go}`)
	send(t, http.MethodPost, tasks+"c0/cancel", "")
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, snap := send(t, http.MethodGet, tasks+"c0", ""); strings.Contains(snap, "status: cancelled") {
			if !strings.Contains(snap, `cancelled: no reason given`) {
				t.Fatalf("the cancelled task's error:\n%s", snap)
			}
			return
		}
	}
	t.Fatal("the task wasn't cancelled")
}
