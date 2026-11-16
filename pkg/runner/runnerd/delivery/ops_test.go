package delivery

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// answer is one request as the server answers it, outside Serve: read, and performed with its
// op.
func (s *Server) answer(tk Task, raw []byte) Reply {
	req, o, rep := s.read(raw)
	if o == nil {
		return rep
	}
	return s.run(context.Background(), tk, *o, req)
}

// wellFormed is a request each op of the table reads all the way to the step it asks about, on
// the workspace of opsTask: one that names what is there and carries what the op needs. An op
// with none here fails the tests below, so an op added to the table is held to them.
var wellFormed = map[string]Request{
	"push":     {Fields: map[string]string{"repo": "svc", "branch": "feature"}},
	"fetch":    {Fields: map[string]string{"repo": "svc"}},
	OpState:    {Fields: map[string]string{"repo": "svc", "branch": "feature"}},
	OpCreatePR: {Fields: map[string]string{"repo": "svc", "branch": "feature", "base": "main", "title": "a change"}},
	OpUpdatePR: {Fields: map[string]string{"repo": "svc", "branch": "feature", "title": "a change"}},

	OpGitHubListPRs: {Fields: map[string]string{"repo": "svc"}},
	OpGitHubReadPR:  {Fields: map[string]string{"repo": "svc", "pr": "7"}},

	OpGitHubCommentPR:    {Body: "looks right", Fields: map[string]string{"repo": "svc", "pr": "7"}},
	OpGitHubReplyComment: {Body: "done", Fields: map[string]string{"repo": "svc", "pr": "7", "comment": "4242"}},

	OpGitIssuePull:       {Fields: map[string]string{"repo": "svc"}},
	OpGitIssuePush:       {Fields: map[string]string{"repo": "svc"}},
	OpGitHubIssueCreate:  {Fields: map[string]string{"repo": "svc", "title": "the roof leaks"}},
	OpGitHubIssueComment: {Body: "still", Fields: map[string]string{"repo": "svc", "issue": "7"}},
	OpGitHubIssueClose:   {Fields: map[string]string{"repo": "svc", "issue": "7"}},
	OpLinearIssueCreate:  {Fields: map[string]string{"team": "eng", "title": "the roof leaks"}},
	OpLinearIssueComment: {Body: "still", Fields: map[string]string{"issue": "eng-7"}},
	OpLinearIssueClose:   {Fields: map[string]string{"issue": "eng-7"}},

	OpSlackPost:  {Body: "shipped", Fields: map[string]string{"channel": "deploys"}},
	OpSlackReply: {Body: "shipped", Fields: map[string]string{"channel": "deploys", "thread": "1700000000.000100"}},
	OpSlackRead:  {Fields: map[string]string{"channel": "deploys"}},
	OpSlackReact: {Fields: map[string]string{"channel": "deploys", "message": "1700000000.000100", "emoji": "eyes"}},

	OpVerseAskCreate:      {Fields: map[string]string{"ask": "{name: land, question: \"Land it?\"}"}},
	OpVerseAskGet:         {Fields: map[string]string{"ask": "land"}},
	OpVerseAskList:        {},
	OpVerseDataGet:        {Fields: map[string]string{"query": "at=acme.notes.n1"}},
	OpVerseDataPut:        {Body: "{at: acme.notes.n1, payload: {stage: open}}"},
	OpVerseDataRm:         {Fields: map[string]string{"query": "at=acme.notes.n1"}},
	OpVerseCharterGet:     {Fields: map[string]string{"query": "path=repos.r.hello"}},
	OpVerseCharterCheck:   {Body: "at: repos.r.hello\n", Fields: map[string]string{"query": "at=repos.r"}},
	OpVerseCharterInstall: {Body: "at: repos.r.hello\n"},
	OpVerseCharterRemove:  {Fields: map[string]string{"query": "path=repos.r.hello.rules.lift"}},

	OpSecretExec: {Body: "{argv: [npm, publish, --ignore-scripts]}", Fields: map[string]string{"name": "npm-token", "dir": "packages/a"}},

	OpMCPCall: {Body: `{"id": "ENG-7"}`, Fields: map[string]string{"server": "linear", "tool": "get_issue"}},
	OpMCPList: {Fields: map[string]string{"server": "linear"}},

	OpPkgInstall: {Fields: map[string]string{"pkg": "jq", "version": "1.7.1-1", "as": "jq-v2"}},
}

// unreached fails a test that reaches a service: what an act would call, on a server no act of
// which may run.
type unreached struct{ t *testing.T }

func (u unreached) RoundTrip(r *http.Request) (*http.Response, error) {
	u.t.Errorf("a call reached %s %s", r.Method, r.URL)
	return nil, io.ErrUnexpectedEOF
}

// opsTask is a server that holds every credential and performs every system, each under an
// open ceiling, and a task on a checkout of acme/svc whose launch is l. So what a request is
// refused by is the launch alone, and a call that reaches a service fails the test.
func opsTask(t *testing.T, l *runner.Launch) (*Server, Task) {
	t.Helper()
	forge := t.TempDir()
	initRepo(t, forge, "acme/svc")
	repos, err := workspace.FromLaunch(gitLaunch("t1", runner.Repo{Repo: "acme/svc"}, pushing).Repos(), forge)
	if err != nil {
		t.Fatal(err)
	}
	mirrors := workspace.NewMirrors(t.TempDir(), workspace.TokenAuth(""))
	ws, err := mirrors.Open(context.Background(), "t1", repos, t.TempDir(), workspace.Author{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range systems.All() {
		names = append(names, d.Name)
	}
	s := &Server{Mirrors: mirrors, Log: io.Discard, HTTP: &http.Client{Transport: unreached{t}}, Ceilings: performs(names...),
		Services: Services{LinearAuth: Holds("a-key"), SlackToken: Holds("xoxb-a-token"), GitHubAPI: "https://api.github.invalid",
			Verse: func(_ context.Context, _, method, path string, _ []byte) (int, string, error) {
				t.Errorf("a request reached verse: %s %s", method, path)
				return http.StatusInternalServerError, "", nil
			}}}
	tk := Task{ID: "t1", Workspace: ws, Launch: l, Credential: func(workspace.Repo) (string, error) { return "the-runners-own", nil },
		created: map[string]bool{}, pushed: map[string]bool{}}
	return s, tk
}

// NO OP ACTS UNDER AN EMPTY ENVIRONMENT. A task whose spawn gave it no system is refused every
// op of the table, as outside its environment, whatever its autonomy allows and though the
// runner holds every credential and its ceilings are open. This is the table's promise, held
// for each op in it: what an op performs, it asked about first.
func TestNoOpActsUnderAnEmptyEnvironment(t *testing.T) {
	l := &runner.Launch{ID: "t1", Prompt: "p", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}}
	s, tk := opsTask(t, l)
	for _, name := range slices.Sorted(maps.Keys(ops)) {
		req, ok := wellFormed[name]
		if !ok {
			t.Errorf("%s: the table has it and wellFormed has no request for it", name)
			continue
		}
		req.Op = name
		rep := s.answer(tk, frame(t, req))
		switch {
		case rep.OK:
			t.Errorf("%s: performed under an empty environment: %+v", name, rep)
		case ops[name].unasked != "":
			if rep.Refused == "" {
				t.Errorf("%s asks about no step, and under an empty environment answered %+v, want a refusal", name, rep)
			}
		case !strings.Contains(rep.Refused, "outside the environment"):
			t.Errorf("%s: %+v, want it refused as outside the environment", name, rep)
		}
	}
	for name := range wellFormed {
		if _, ok := ops[name]; !ok {
			t.Errorf("wellFormed has a request for %s, which the table does not have", name)
		}
	}
}

// EVERY OP OF THE TABLE IS HELD TO THE SYSTEMS' DOCUMENTS: the steps it may ask about are verbs
// a document describes, it has a deadline, and it asks about a step unless the table says why
// not. Two ops say so: the branch's state, which a push's own decision is made on, and a server's
// list of tools, which a call's is.
func TestEveryOpAsksAboutADocumentedStep(t *testing.T) {
	documented := systems.Steps()
	var unasked []string
	for name, o := range ops {
		if o.ask == nil || o.deadline <= 0 {
			t.Errorf("%s: an op reads its request and has a deadline: %+v", name, o)
		}
		if (len(o.steps) == 0) == (o.unasked == "") {
			t.Errorf("%s: an op asks about its steps, or says why it asks about none, and not both: %+v", name, o)
		}
		if o.unasked != "" {
			unasked = append(unasked, name)
		}
		// A name is read one way: as text, as a flag or as a number.
		reads := slices.Sorted(slices.Values(o.reads()))
		if len(slices.Compact(slices.Clone(reads))) != len(reads) {
			t.Errorf("%s reads a name more than one way: %v", name, reads)
		}
		for _, st := range o.steps {
			if !slices.Contains(documented, st) {
				t.Errorf("%s asks about %s, which no system's document describes", name, st)
			}
		}
	}
	slices.Sort(unasked)
	if !slices.Equal(unasked, []string{OpMCPList, OpState}) {
		t.Errorf("the ops that ask about no step are %v, want the branch's state and a server's list alone", unasked)
	}
}

// A NAME AN OP DOES NOT READ IS REFUSED, AND NOTHING IS READ OF THE REQUEST: what the task sent
// and the runner dropped is something the task believes was done. A name with nothing under it
// is no name, and a document of several lines crosses whole.
func TestARequestNamesOnlyWhatItsOpReads(t *testing.T) {
	l := within(nil, "slack", "verse")
	s := &Server{Log: io.Discard, Ceilings: performs("slack", "verse"), Services: Services{SlackToken: Holds("xoxb-a-token"),
		Verse: func(_ context.Context, _, _, _ string, body []byte) (int, string, error) {
			return http.StatusCreated, "{id: land-1a2b}", nil
		}}}
	tk := Task{ID: "t1", Workspace: &workspace.Workspace{}, Launch: l}
	rep := s.answer(tk, frame(t, Request{Op: OpSlackReact, Fields: map[string]string{"channel": "deploys", "message": "1.2", "emoji": "eyes", "force": "true"}}))
	if !strings.Contains(rep.Error, "slack.react reads no `force` (it reads: channel, message, emoji)") {
		t.Errorf("a react that names force: %+v", rep)
	}
	// A body is read as a name is: by the op that takes one, and refused by one that does not.
	for _, req := range []Request{
		{Op: OpSlackReact, Body: "and a word", Fields: map[string]string{"channel": "deploys", "message": "1.2", "emoji": "eyes"}},
		{Op: OpSlackRead, Body: "and a word", Fields: map[string]string{"channel": "deploys"}},
		{Op: OpLinearIssueClose, Body: "done, see the PR", Fields: map[string]string{"issue": "eng-7"}},
		{Op: OpGitHubIssueClose, Body: "done, see the PR", Fields: map[string]string{"repo": "svc", "issue": "7"}},
	} {
		if rep := s.answer(tk, frame(t, req)); !strings.Contains(rep.Error, req.Op+" reads no body, and nothing was done") {
			t.Errorf("%s with a body: %+v, want it refused", req.Op, rep)
		}
	}
	// A flag is true or false, and a number a number: one that is neither is not read as no.
	for _, c := range []struct {
		req  Request
		want string
	}{
		{Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": "old", "delete": "yes"}}, "push reads `delete` as true or false, and it is \"yes\""},
		{Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": "old", "force": "1"}}, "push reads `force` as true or false, and it is \"1\""},
		{Request{Op: OpSlackRead, Fields: map[string]string{"channel": "deploys", "limit": "5O"}}, "slack.read reads `limit` as a number, and it is \"5O\""},
		{Request{Op: OpUpdatePR, Fields: map[string]string{"repo": "svc", "branch": "x", "draft": "true"}}, "update-pr reads no `draft`"},
	} {
		if rep := s.answer(tk, frame(t, c.req)); !strings.Contains(rep.Error, c.want) {
			t.Errorf("%s %v: %+v, want %q", c.req.Op, c.req.Fields, rep, c.want)
		}
	}
	rep = s.answer(tk, frame(t, Request{Op: OpVerseAskList, Fields: map[string]string{"ask": "land"}}))
	if !strings.Contains(rep.Error, "verse.ask-list reads no `ask` (it reads: nothing)") {
		t.Errorf("a listing that names an ask: %+v", rep)
	}
	ask := "name: land\nquestion: |\n  Land it?\n  Or wait?\n"
	rep = s.answer(tk, frame(t, Request{Op: OpVerseAskCreate, Fields: map[string]string{"ask": ask, "query": ""}}))
	if !rep.OK || !strings.Contains(rep.Out, "land-1a2b") {
		t.Errorf("a raise whose ask is several lines, beside an empty name: %+v", rep)
	}
}

// AN ACT IS PERFORMED ONCE EVERY STEP IT ASKED ABOUT HAS PASSED, and an op that does otherwise
// is answered as the bug it is, with nothing performed: one that answers OK with no act and no
// word in the table for why, one whose act asked about nothing, and one that asked about a
// step that is not its own.
func TestAnActFollowsEveryStepItAskedAbout(t *testing.T) {
	post := func(channel string) task.Step {
		return task.NewStep(task.StepSlackPost, map[string]string{"channel": channel})
	}
	l := within(nil)
	l.Environment.Systems["slack"] = []systems.Block{{Verbs: map[string][]systems.Shape{"post": {{"channel": systems.Regexp("^(deploys|alerts)$")}}}}}
	s := &Server{Log: io.Discard, Ceilings: performs("slack")}
	tk := Task{ID: "t1", Workspace: &workspace.Workspace{}, Launch: l}
	acted := 0
	act := func() Reply { acted++; return Reply{OK: true} }
	posts := []string{task.StepSlackPost}
	for _, c := range []struct {
		name string
		o    op
		want func(Reply) bool
		acts int
	}{
		{"one step within", op{steps: posts, ask: func(*Server, context.Context, Task, Request) (asked, Reply) {
			return asking(post("deploys"), act)
		}}, func(r Reply) bool { return r.OK }, 1},
		{"two steps within", op{steps: posts, ask: func(*Server, context.Context, Task, Request) (asked, Reply) {
			return asked{steps: []task.Step{post("deploys"), post("alerts")}, act: act}, Reply{}
		}}, func(r Reply) bool { return r.OK }, 1},
		{"the second step outside", op{steps: posts, ask: func(*Server, context.Context, Task, Request) (asked, Reply) {
			return asked{steps: []task.Step{post("deploys"), post("general")}, act: act}, Reply{}
		}}, func(r Reply) bool { return strings.Contains(r.Refused, "outside the environment") }, 0},
		{"answered before any step", op{steps: posts, ask: func(*Server, context.Context, Task, Request) (asked, Reply) {
			return before(Reply{Refused: "a message needs text"})
		}}, func(r Reply) bool { return r.Refused == "a message needs text" }, 0},
		{"OK with no act", op{steps: posts, ask: func(*Server, context.Context, Task, Request) (asked, Reply) {
			acted++
			return before(Reply{OK: true})
		}}, func(r Reply) bool { return strings.Contains(r.Error, "without asking about a step") }, 1},
		{"OK with no act, where the table says why", op{unasked: "a test's", ask: func(*Server, context.Context, Task, Request) (asked, Reply) {
			return before(Reply{OK: true})
		}}, func(r Reply) bool { return r.OK }, 0},
		{"an act that asked about nothing", op{steps: posts, ask: func(*Server, context.Context, Task, Request) (asked, Reply) {
			return asked{act: act}, Reply{}
		}}, func(r Reply) bool { return strings.Contains(r.Error, "asked about no step") }, 0},
		{"a step that is not the op's", op{steps: posts, ask: func(*Server, context.Context, Task, Request) (asked, Reply) {
			return asking(task.NewStep(task.StepSlackRead, map[string]string{"channel": "deploys"}), act)
		}}, func(r Reply) bool { return strings.Contains(r.Error, "no step of its own") }, 0},
		{"a field the document does not describe", op{steps: posts, ask: func(*Server, context.Context, Task, Request) (asked, Reply) {
			return asking(task.NewStep(task.StepSlackPost, map[string]string{"channel": "deploys", "loudly": "true"}), act)
		}}, func(r Reply) bool {
			return strings.Contains(r.Error, "a field `loudly` its system's document does not describe")
		}, 0},
	} {
		acted = 0
		rep := s.perform(context.Background(), tk, c.o, Request{Op: "a-test"})
		if !c.want(rep) || acted != c.acts {
			t.Errorf("%s: %+v, performed %d time(s), want %d", c.name, rep, acted, c.acts)
		}
	}
}

// WHAT AN OP IS ASKED WITH IS UNDER `fields`, AND A NAME BESIDE IT IS REFUSED AS ONE WITHIN IT
// IS. A side that still wrote a request the old way, its names at the top, would otherwise have
// them dropped: a push asked with `delete: true` beside `fields` would be a push. The refusal
// names the request, so the one who asked hears it.
func TestANameBesideFieldsIsRefused(t *testing.T) {
	s, tk := opsTask(t, within(nil, "verse", "git"))
	for _, raw := range []string{
		"{id: 4, op: verse.data-get, query: at=small}",
		"{id: 4, op: push, fields: {repo: svc, branch: old}, delete: true}",
	} {
		rep := s.answer(tk, []byte(raw))
		if rep.ID != 4 || rep.OK || !strings.Contains(rep.Error, "undecodable request, and nothing was done") {
			t.Errorf("%s: %+v, want it refused under its id", raw, rep)
		}
	}
}

// AN OP READS A NAME AS ITS ENTRY IN THE TABLE SAYS IT DOES. The request was checked against
// the entry (misread), so an op that reads as a flag what the entry calls text reads what
// nobody checked: it is answered as a bug, whether it reads so before its steps or in its act.
// A name the entry does not list reads as nothing, since no request that gives it gets here.
func TestAnOpReadsANameAsItsEntrySays(t *testing.T) {
	var log bytes.Buffer
	l := within(nil, "slack")
	s := &Server{Log: &log, Ceilings: performs("slack")}
	tk := Task{ID: "t1", Workspace: &workspace.Workspace{}, Launch: l}
	post := task.NewStep(task.StepSlackPost, map[string]string{"channel": "deploys"})
	for _, c := range []struct {
		name string
		o    op
		req  map[string]string
		want string
	}{
		{"text read as a flag", op{unasked: "a test's", takes: []string{"delete"},
			ask: func(_ *Server, _ context.Context, _ Task, req Request) (asked, Reply) {
				return before(Reply{OK: req.Flag("delete")})
			}}, map[string]string{"delete": "yes"}, "read `delete` as a flag"},
		{"a flag read as text, in the act", op{steps: []string{task.StepSlackPost}, flags: []string{"force"},
			ask: func(_ *Server, _ context.Context, _ Task, req Request) (asked, Reply) {
				return asking(post, func() Reply { return Reply{OK: true, Out: req.Field("force")} })
			}}, map[string]string{"force": "true"}, "read `force` as text"},
		{"a number read as text", op{unasked: "a test's", numbers: []string{"limit"},
			ask: func(_ *Server, _ context.Context, _ Task, req Request) (asked, Reply) {
				return before(Reply{OK: true, Out: req.Field("limit")})
			}}, map[string]string{"limit": "5"}, "read `limit` as text"},
		{"each as the entry says", op{unasked: "a test's", takes: []string{"repo"}, flags: []string{"force"}, numbers: []string{"limit"},
			ask: func(_ *Server, _ context.Context, _ Task, req Request) (asked, Reply) {
				return before(Reply{OK: true, Out: fmt.Sprintf("%s %t %d %q", req.Field("repo"), req.Flag("force"), req.Int("limit"), req.Field("other"))})
			}}, map[string]string{"repo": "svc", "force": "true", "limit": "5"}, ""},
	} {
		log.Reset()
		rep := s.perform(context.Background(), tk, c.o, Request{Op: "a-test.op", Fields: c.req})
		switch {
		case c.want == "":
			if !rep.OK || rep.Out != `svc true 5 ""` || log.Len() != 0 {
				t.Errorf("%s: %+v, and the log %q", c.name, rep, log.String())
			}
		case rep.OK || !strings.Contains(rep.Error, c.want) || !strings.Contains(log.String(), "BUG"):
			t.Errorf("%s: %+v, and the log %q, want it answered as a bug: %s", c.name, rep, log.String(), c.want)
		case strings.Contains(c.name, "in the act") != strings.Contains(rep.Error, "whether it was performed is not known"),
			strings.Contains(c.name, "in the act") == strings.Contains(rep.Error, "did not perform it"):
			// Before its steps nothing was performed, and the answer says so; in its act
			// something may have been, and the answer does not say otherwise.
			t.Errorf("%s: %+v, which says the wrong thing about what was performed", c.name, rep)
		}
	}
}
