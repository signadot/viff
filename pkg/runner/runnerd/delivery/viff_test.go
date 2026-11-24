package delivery

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"go.viff.dev/viff/pkg/runner/runnerd/workspace"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
)

// A TASK'S RAISE IS CARRIED TO VIFF FOR THE TASK, after the task's autonomy and the runner's
// ceiling, each read again here: the raiser is handed the task's id and the ask as written, and
// what viff refuses the task reads in viff's words. A runner with no raiser says it reaches no
// viff.
func TestATasksRaiseIsCarriedToViffForTheTask(t *testing.T) {
	type call struct{ task, ask string }
	var saw []call
	answer := func(code int, said string) Viff {
		return func(_ context.Context, taskID, method, path string, ask []byte) (int, string, error) {
			if method != http.MethodPost || path != "/asks" {
				return http.StatusNotFound, method + " " + path, nil
			}
			saw = append(saw, call{taskID, string(ask)})
			return code, said, nil
		}
	}
	open := map[string]systems.Ceiling{"viff": systems.Open()}
	l := within(nil, "viff")
	l.Autonomy = task.Autonomy{{Match: &task.Match{System: "viff", Verb: "ask-create"}, Then: task.Then{Mode: task.ThenAllow}}, {Then: task.Then{Mode: task.ThenAllow}}}
	tk := Task{ID: "t7", Workspace: &workspace.Workspace{}, Launch: l}
	const ask = "{name: land, question: \"Land it?\"}"

	s := &Server{Log: io.Discard, Services: Services{Viff: answer(http.StatusCreated, "id: land-1a2b")}, Ceilings: open}
	if rep := s.answer(tk, frame(t, Request{Op: OpViffAskCreate, Fields: map[string]string{"ask": ask}})); !rep.OK || rep.Out != "raised question land-1a2b: Land it?" {
		t.Errorf("a raise: %+v", rep)
	}
	if len(saw) != 1 || saw[0] != (call{"t7", ask}) {
		t.Errorf("the raiser was handed %+v, want the task and the ask as written", saw)
	}

	s.Services.Viff = answer(http.StatusBadRequest, "then: spawn.environment.systems.network: beyond the task")
	if rep := s.answer(tk, frame(t, Request{Op: OpViffAskCreate, Fields: map[string]string{"ask": ask}})); !strings.Contains(rep.Refused, "the viff refused the ask: then: spawn.environment") {
		t.Errorf("a raise viff refuses: %+v", rep)
	}

	denied := tk
	dl := *l
	dl.Autonomy = task.Autonomy{{Name: "no-raise", Match: &task.Match{System: "viff", Verb: "ask-create"}, Then: task.Then{Mode: task.ThenDeny}}, {Then: task.Then{Mode: task.ThenAllow}}}
	denied.Launch = &dl
	saw = nil
	if rep := s.answer(denied, frame(t, Request{Op: OpViffAskCreate, Fields: map[string]string{"ask": ask}})); !strings.Contains(rep.Refused, "the autonomy's row no-raise") || len(saw) != 0 {
		t.Errorf("a raise the autonomy denies: %+v (carried %v)", rep, saw)
	}

	s.Ceilings = map[string]systems.Ceiling{}
	if rep := s.answer(tk, frame(t, Request{Op: OpViffAskCreate, Fields: map[string]string{"ask": ask}})); !strings.Contains(rep.Refused, "ceiling") || len(saw) != 0 {
		t.Errorf("a raise beyond the ceiling: %+v (carried %v)", rep, saw)
	}

	s.Services.Viff = nil
	if rep := s.answer(tk, frame(t, Request{Op: OpViffAskCreate, Fields: map[string]string{"ask": ask}})); !strings.Contains(rep.Error, "reaches no viff") {
		t.Errorf("a runner with no raiser: %+v", rep)
	}
}

// A TASK READS ONE OF ITS OWNER'S ASKS BACK, by its id or the name it was asked under, as `viff
// ask <name|id>` shows it: whether it was answered. The read is carried to viff for the task
// (GET /asks), after the task's autonomy and the runner's ceiling; a name two asks share is
// refused with their ids, rather than guessed between.
func TestATasksReadOfAnAskIsCarriedToViff(t *testing.T) {
	var saw []string
	asks := `- {id: land-1a2b, name: land, question: "Land it?", answer: yes, status: answered}
- {id: tea-1, name: tea, question: "Tea?", status: pending, waitingOn: ["user:dana@example.com"]}
- {id: tea-2, name: tea, question: "Tea again?", status: pending}
`
	viff := func(_ context.Context, taskID, method, path string, body []byte) (int, string, error) {
		saw = append(saw, taskID+" "+method+" "+path)
		return http.StatusOK, asks, nil
	}
	l := within(nil, "viff")
	l.Autonomy = task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}
	tk := Task{ID: "t7", Workspace: &workspace.Workspace{}, Launch: l}
	s := &Server{Log: io.Discard, Services: Services{Viff: viff}, Ceilings: map[string]systems.Ceiling{"viff": systems.Open()}}

	for _, arg := range []string{"land-1a2b", "land"} {
		rep := s.answer(tk, frame(t, Request{Op: OpViffAskGet, Fields: map[string]string{"ask": arg}}))
		if !rep.OK || !strings.Contains(rep.Out, "answer: yes") || !strings.Contains(rep.Out, "id: land-1a2b") {
			t.Errorf("the ask read by %q: %+v", arg, rep)
		}
	}
	if len(saw) != 2 || saw[0] != "t7 GET /asks?ask=land-1a2b" || saw[1] != "t7 GET /asks?ask=land" {
		t.Errorf("the reads were carried as %v, want the task's GET /asks naming the one ask", saw)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpViffAskGet, Fields: map[string]string{"ask": "tea-1"}})); !strings.Contains(rep.Out, "answer: pending") || !strings.Contains(rep.Out, "dana") {
		t.Errorf("a pending ask: %+v, want it pending and whom it waits on", rep)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpViffAskGet, Fields: map[string]string{"ask": "tea"}})); !strings.Contains(rep.Refused, "tea-1, tea-2") {
		t.Errorf("a name two asks share: %+v, want it refused with their ids", rep)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpViffAskGet, Fields: map[string]string{"ask": "nothing"}})); !strings.Contains(rep.Refused, "no ask named or numbered") {
		t.Errorf("an ask that is not there: %+v", rep)
	}

	denied := tk
	dl := *l
	dl.Autonomy = task.Autonomy{{Name: "no-read", Match: &task.Match{System: "viff", Verb: "ask-get"}, Then: task.Then{Mode: task.ThenDeny}}, {Then: task.Then{Mode: task.ThenAllow}}}
	denied.Launch = &dl
	saw = nil
	if rep := s.answer(denied, frame(t, Request{Op: OpViffAskGet, Fields: map[string]string{"ask": "land"}})); !strings.Contains(rep.Refused, "the autonomy's row no-read") || len(saw) != 0 {
		t.Errorf("a read the autonomy denies: %+v (carried %v)", rep, saw)
	}
	s.Services.Viff = nil
	if rep := s.answer(tk, frame(t, Request{Op: OpViffAskGet, Fields: map[string]string{"ask": "land"}})); !strings.Contains(rep.Error, "reaches no viff") {
		t.Errorf("a runner that reaches no viff: %+v", rep)
	}
}

// A TASK LISTS ITS OWNER'S ASKS, as `viff ask` lists them: each row with its id and its answer,
// `pending` until it settles, carried to viff for the task (GET /asks) after the task's autonomy;
// none is said in a sentence.
func TestATasksListingOfItsOwnersAsksIsCarriedToViff(t *testing.T) {
	var saw []string
	asks := `- {id: land-1a2b, name: land, question: "Land it?", answer: yes, status: answered}
- {id: tea-1, name: tea, question: "Tea?", status: pending}
`
	viff := func(_ context.Context, taskID, method, path string, body []byte) (int, string, error) {
		saw = append(saw, taskID+" "+method+" "+path)
		return http.StatusOK, asks, nil
	}
	l := within(nil, "viff")
	l.Autonomy = task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}
	tk := Task{ID: "t7", Workspace: &workspace.Workspace{}, Launch: l}
	s := &Server{Log: io.Discard, Services: Services{Viff: viff}, Ceilings: map[string]systems.Ceiling{"viff": systems.Open()}}

	rep := s.answer(tk, frame(t, Request{Op: OpViffAskList}))
	if !rep.OK || !strings.Contains(rep.Out, "id: land-1a2b") || !strings.Contains(rep.Out, "id: tea-1") || !strings.Contains(rep.Out, "answer: pending") {
		t.Errorf("the owner's asks listed: %+v", rep)
	}
	if len(saw) != 1 || saw[0] != "t7 GET /asks" {
		t.Errorf("the listing was carried as %v, want the task's GET /asks", saw)
	}
	asks = "[]"
	if rep := s.answer(tk, frame(t, Request{Op: OpViffAskList})); !rep.OK || !strings.Contains(rep.Out, "no asks standing") {
		t.Errorf("no asks: %+v, want a sentence saying so", rep)
	}

	denied := tk
	dl := *l
	dl.Autonomy = task.Autonomy{{Name: "no-list", Match: &task.Match{System: "viff", Verb: "ask-list"}, Then: task.Then{Mode: task.ThenDeny}}, {Then: task.Then{Mode: task.ThenAllow}}}
	denied.Launch = &dl
	saw = nil
	if rep := s.answer(denied, frame(t, Request{Op: OpViffAskList})); !strings.Contains(rep.Refused, "the autonomy's row no-list") || len(saw) != 0 {
		t.Errorf("a listing the autonomy denies: %+v (carried %v)", rep, saw)
	}
	s.Services.Viff = nil
	if rep := s.answer(tk, frame(t, Request{Op: OpViffAskList})); !strings.Contains(rep.Error, "reaches no viff") {
		t.Errorf("a runner that reaches no viff: %+v", rep)
	}
}

// A TASK'S DATA AND CHARTER ACTS ARE CARRIED ON THEIR OWN ROUTES: a read to GET /state, a write
// to POST /entity with its document, a removal to DELETE /entity, a charter read to GET /charter,
// a check to POST /charter/check with the charters, an install to POST /charter with them, a
// removal to DELETE /charter, each with the query the task's tool read and
// nothing else of the task's; each decided by the task's autonomy first, and what viff
// refuses is a no.
func TestATasksDataActsAreCarriedToViff(t *testing.T) {
	type carried struct{ method, path, body string }
	var saw []carried
	status, said := http.StatusOK, "acme: {notes: {n1: {stage: open}}}"
	viff := func(_ context.Context, taskID, method, path string, body []byte) (int, string, error) {
		saw = append(saw, carried{method, path, string(body)})
		return status, said, nil
	}
	l := within(nil, "viff")
	l.Autonomy = task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}
	tk := Task{ID: "t7", Workspace: &workspace.Workspace{}, Launch: l}
	s := &Server{Log: io.Discard, Services: Services{Viff: viff}, Ceilings: map[string]systems.Ceiling{"viff": systems.Open()}}

	for _, c := range []struct {
		req  Request
		want carried
	}{
		{Request{Op: OpViffDataGet, Fields: map[string]string{"query": "at=acme.notes.n1&paths=1"}}, carried{http.MethodGet, "/state?at=acme.notes.n1&paths=1", ""}},
		{Request{Op: OpViffDataPut, Body: "{at: acme.notes.n1, payload: {stage: open}}"}, carried{http.MethodPost, "/entity", "{at: acme.notes.n1, payload: {stage: open}}"}},
		{Request{Op: OpViffDataRm, Fields: map[string]string{"query": "at=acme.notes.n1"}}, carried{http.MethodDelete, "/entity?at=acme.notes.n1", ""}},
		{Request{Op: OpViffCharterGet, Fields: map[string]string{"query": "path=repos.r.hello"}}, carried{http.MethodGet, "/charter?path=repos.r.hello", ""}},
		{Request{Op: OpViffCharterCheck, Body: "at: repos.r.hello\n", Fields: map[string]string{"query": "at=repos.r"}}, carried{http.MethodPost, "/charter/check?at=repos.r", "at: repos.r.hello\n"}},
		{Request{Op: OpViffCharterInstall, Body: "at: repos.r.hello\n", Fields: map[string]string{"query": "prune=repos.r"}}, carried{http.MethodPost, "/charter?prune=repos.r", "at: repos.r.hello\n"}},
		{Request{Op: OpViffCharterInstall, Body: "at: repos.r.hello\n"}, carried{http.MethodPost, "/charter", "at: repos.r.hello\n"}},
		{Request{Op: OpViffCharterRemove, Fields: map[string]string{"query": "path=repos.r.hello.rules.lift"}}, carried{http.MethodDelete, "/charter?path=repos.r.hello.rules.lift", ""}},
	} {
		saw = nil
		if rep := s.answer(tk, frame(t, c.req)); !rep.OK || rep.Out != said {
			t.Errorf("%s: %+v, want what viff said", c.req.Op, rep)
		}
		if len(saw) != 1 || saw[0] != c.want {
			t.Errorf("%s was carried as %+v, want %+v", c.req.Op, saw, c.want)
		}
	}

	saw = nil
	for _, req := range []Request{{Op: OpViffDataPut}, {Op: OpViffDataRm}, {Op: OpViffDataGet, Fields: map[string]string{"query": "%zz"}},
		{Op: OpViffCharterCheck, Body: "at: repos.r.hello"}, {Op: OpViffCharterCheck, Fields: map[string]string{"query": "at=repos.r"}},
		{Op: OpViffCharterInstall, Fields: map[string]string{"query": "at=repos.r"}}, {Op: OpViffCharterRemove},
		{Op: OpViffDataPut, Body: "{at: acme.notes.n1}", Fields: map[string]string{"query": "scope=elsewhere"}},
		{Op: OpViffCharterInstall, Body: "at: repos.r.hello\n", Fields: map[string]string{"query": "scope=elsewhere"}}} {
		if rep := s.answer(tk, frame(t, req)); rep.Refused == "" {
			t.Errorf("%+v: %+v, want it refused", req, rep)
		}
	}
	if len(saw) != 0 {
		t.Errorf("an act refused by the runner was carried: %v", saw)
	}

	status, said = http.StatusForbidden, "user:tester may not write mirror.task.t7"
	if rep := s.answer(tk, frame(t, Request{Op: OpViffDataPut, Body: "{at: mirror.task.t7}"})); !strings.Contains(rep.Refused, "may not write") {
		t.Errorf("a write viff refused: %+v, want a no in viff's words", rep)
	}

	denied := tk
	dl := *l
	dl.Autonomy = task.Autonomy{{Name: "no-writes", Match: &task.Match{System: "viff", Verb: "data-put"}, Then: task.Then{Mode: task.ThenDeny}}, {Then: task.Then{Mode: task.ThenAllow}}}
	denied.Launch = &dl
	saw = nil
	if rep := s.answer(denied, frame(t, Request{Op: OpViffDataPut, Body: "{at: acme.notes.n1}"})); !strings.Contains(rep.Refused, "the autonomy's row no-writes") || len(saw) != 0 {
		t.Errorf("a write the autonomy denies: %+v (carried %v)", rep, saw)
	}

	// No answer from viff: a read was not done, and a write may have been.
	s.Services.Viff = func(context.Context, string, string, string, []byte) (int, string, error) {
		return 0, "", errors.New("context deadline exceeded")
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpViffDataGet, Fields: map[string]string{"query": "at=acme"}})); !strings.Contains(rep.Error, "not carried") {
		t.Errorf("a read viff did not answer: %+v, want it not done", rep)
	}
	for _, req := range []Request{{Op: OpViffDataPut, Body: "{at: acme.notes.n1}"}, {Op: OpViffCharterInstall, Body: "at: repos.r.hello\n"}} {
		if rep := s.answer(tk, frame(t, req)); !strings.Contains(rep.Error, "whether it was done is not known") {
			t.Errorf("%s viff did not answer: %+v, want it said that it may have been done", req.Op, rep)
		}
	}
	s.Services.Viff = nil
	if rep := s.answer(tk, frame(t, Request{Op: OpViffDataGet})); !strings.Contains(rep.Error, "reaches no viff") {
		t.Errorf("a runner that reaches no viff: %+v", rep)
	}
}

// AN INSTALL IS BOUND BY WHERE THE TASK'S ENVIRONMENT SAYS IT MAY INSTALL: the step's `at` is the
// address the install acts at (charterfile.InstallAt), the prune's or the deepest address its
// charters share; one at or under the environment's shape is carried, and one beyond it is refused
// before viff is asked.
func TestATasksInstallIsBoundByItsEnvironmentsAt(t *testing.T) {
	var saw []string
	viff := func(_ context.Context, _, _, path string, _ []byte) (int, string, error) {
		saw = append(saw, path)
		return http.StatusCreated, "installed: []", nil
	}
	l := within(nil)
	l.Environment.Systems["viff"] = []systems.Block{{Verbs: map[string][]systems.Shape{
		"charter-install": {{"at": ir.FromString("play.issues.issue")}},
	}}}
	tk := Task{ID: "t7", Workspace: &workspace.Workspace{}, Launch: l}
	s := &Server{Log: io.Discard, Services: Services{Viff: viff}, Ceilings: map[string]systems.Ceiling{"viff": systems.Open()}}

	for _, c := range []struct {
		req     Request
		carried bool
	}{
		{Request{Op: OpViffCharterInstall, Body: "at: play.issues.issue.a\n---\nat: play.issues.issue.b\n", Fields: map[string]string{"query": "prune=play.issues.issue"}}, true},
		{Request{Op: OpViffCharterInstall, Body: "at: play.issues.issue.a\n---\nat: play.issues.issue.b\n"}, true},
		{Request{Op: OpViffCharterInstall, Body: "{name: r, condition: {subject: x}, action: {patch: {}}}\n", Fields: map[string]string{"query": "at=play.issues.issue"}}, true},
		{Request{Op: OpViffCharterInstall, Body: "at: play.issues.issue.a\n", Fields: map[string]string{"query": "prune=play.issues"}}, false},
		{Request{Op: OpViffCharterInstall, Body: "at: play.issues.issue.a\n"}, true},
		{Request{Op: OpViffCharterInstall, Body: "at: play.issues.issue.a\n---\nat: play.other\n"}, false},
		{Request{Op: OpViffCharterInstall, Body: "rules: []\n"}, false},
	} {
		saw = nil
		rep := s.answer(tk, frame(t, c.req))
		switch {
		case c.carried && (!rep.OK || len(saw) != 1):
			t.Errorf("%s %q: %+v, want it carried", c.req.Field("query"), c.req.Body, rep)
		case !c.carried && (!strings.Contains(rep.Refused, "outside the environment") || len(saw) != 0):
			t.Errorf("%s %q: %+v (carried %v), want it refused as outside the environment", c.req.Field("query"), c.req.Body, rep, saw)
		}
	}
}
