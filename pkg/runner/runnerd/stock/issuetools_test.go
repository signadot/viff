package stock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/signadot/tony-format/git-issue/issuelib"
	"github.com/signadot/tony-format/git-issue/ops"
	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd/delivery"
	"go.viff.dev/viff/pkg/runner/runnerd/workspace"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
)

// A SCRIPT FILES, COMMENTS ON AND CLOSES AN ISSUE IN ITS CHECKOUT, PUSHES THE TRACKER, AND PULLS
// IT BACK. End to end through viff-call-tool: the three writes are the script's own and ask
// nobody, the push is the step a gate on git-issue.push asks about, naming the repo, the approval
// has the runner send the tracker with its credential, the remote's own store reads the issue
// closed with its comment, and a pull reports the tracker from the checkout.
func TestAScriptFilesAGitIssueAndPushesTheTracker(t *testing.T) {
	withCallTool(t)
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	rt := serveRepos(t, forge, Secrets{})
	launchScript(rt, "t1", `#!/bin/sh
set -e
out=$(viff-call-tool git-issue-create -title "the roof leaks" -body "when it rains"); echo "$out"
id=$(echo "$out" | sed -n 's/^filed issue \([a-z0-9]*\) .*/\1/p')
viff-call-tool git-issue-comment -issue "$id" -body "a tarp is on it"
viff-call-tool git-issue-close -issue "$id"
viff-call-tool git-issue-push
viff-call-tool git-issue-pull
git -C svc for-each-ref --format='%(refname)' refs/git-issues/
`, task.Row{Name: "filing", Match: &task.Match{System: "git-issue", Verb: "push"}, Then: task.Then{Mode: task.ThenAsk}})
	q := pending(t, rt, "t1")
	if q.Step != task.StepGitIssuePush || q.Gate != "filing" || !q.Fields.DeepEqual(systems.FieldsDoc(q.Step, map[string]string{"repo": "github.com/acme/svc"})) {
		t.Fatalf("asked about %+v, want the push of the tracker (a filing here and a pull ask nobody)", q)
	}
	if err := rt.Attach("t1").(resolver).Resolve(q.ID, task.Proceed, "", "user:ana"); err != nil {
		t.Fatal(err)
	}
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done {
		t.Fatalf("task: %+v", snap)
	}
	// Filed, commented on and closed here, then pushed: the remote reads it closed, with the
	// comment, and the pull lists nothing open.
	origin := issuelib.NewGitStoreAt(filepath.Join(forge, "acme/svc"), io.Discard)
	issues, err := origin.List(true)
	if err != nil || len(issues) != 1 || issues[0].Title != "the roof leaks" || issues[0].Status != "closed" {
		t.Fatalf("the remote's tracker lists %+v, %v", issues, err)
	}
	if shown, err := ops.Show(origin, issues[0].ID); err != nil || !strings.Contains(fmt.Sprint(shown), "a tarp is on it") {
		t.Errorf("the remote's copy lacks the comment: %+v, %v", shown, err)
	}
	for _, want := range []string{
		"filed issue " + issues[0].ID,
		"commented on issue " + issues[0].ID,
		"closed issue " + issues[0].ID,
		"pushed the issues of svc",
		"0 open:",
		"refs/git-issues/v1/closed/" + issues[0].ID,
	} {
		if !strings.Contains(snap.Result, want) {
			t.Errorf("the script read %q, want %q in it", snap.Result, want)
		}
	}
}

// A DENIED PUSH SENDS NOTHING: the issue stays in the checkout, the remote has none, and the
// script reads the denial as exit 3 and goes on.
func TestADeniedIssuePushSendsNothing(t *testing.T) {
	withCallTool(t)
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	rt := serveRepos(t, forge, Secrets{})
	launchScript(rt, "t1", "#!/bin/sh\nviff-call-tool git-issue-create -title no >/dev/null\n"+
		"viff-call-tool git-issue-push; echo \"exit=$?\"\ngit -C svc for-each-ref refs/git-issues/v1/open | wc -l",
		task.Row{Name: "leaving", Match: &task.Match{System: "git-issue", Verb: "push", Params: map[string]*ir.Node{"repo": systems.Glob("github.com/acme/*")}}, Then: task.Then{Mode: task.ThenAsk}})
	q := pending(t, rt, "t1")
	if err := rt.Attach("t1").(resolver).Resolve(q.ID, task.Deny, "", "user:ana"); err != nil {
		t.Fatal(err)
	}
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "exit=3") || !strings.HasSuffix(strings.TrimSpace(snap.Result), "1") {
		t.Fatalf("task: %+v", snap)
	}
	if issues, _ := issuelib.NewGitStoreAt(filepath.Join(forge, "acme/svc"), io.Discard).List(false); len(issues) != 0 {
		t.Fatalf("a denied push reached the remote: %+v", issues)
	}
}

// A LINEAR ISSUE IS FILED WITH THE RUNNER'S CREDENTIAL, which the script never holds: the gate
// on linear-issue.create asks, naming the team, the runner makes both calls to Linear, and the
// script reads the identifier. A runner with no `linear:` section offers no such tool, and a
// gate on its step is refused at launch: nothing there could stop it.
func TestAScriptFilesALinearIssueWithTheRunnersCredential(t *testing.T) {
	withCallTool(t)
	var auths []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		var in struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if strings.Contains(in.Query, "teams(") {
			_, _ = w.Write([]byte(`{"data":{"teams":{"nodes":[{"id":"team-uuid"}]}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"i","identifier":"ENG-42","url":"https://linear.app/acme/issue/ENG-42"}}}}`))
	}))
	defer api.Close()
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	rt := serveConfigured(t, every(Config{Repos: &Repos{Mirrors: t.TempDir(), Forge: forge}}, map[string]System{"linear-issue": {}}),
		Secrets{}, Tokens{Linear: "lin_api_secret", LinearAPI: api.URL})
	script := "#!/bin/sh\nviff-call-tool linear-issue-create -team eng -title \"the roof leaks\" -description \"when it rains\"\n" +
		"echo \"exit=$?\"\nenv | grep -c LINEAR || true"
	launchScript(rt, "t1", script, task.Row{Name: "linear", Match: &task.Match{System: "linear-issue", Verb: "create", Params: map[string]*ir.Node{"team": ir.FromString("ENG")}}, Then: task.Then{Mode: task.ThenAsk}})
	q := pending(t, rt, "t1")
	if q.Step != task.StepLinearIssueCreate || !q.Fields.DeepEqual(systems.FieldsDoc(q.Step, map[string]string{"team": "ENG"})) {
		t.Fatalf("asked about %+v, want the filing in ENG", q)
	}
	if err := rt.Attach("t1").(resolver).Resolve(q.ID, task.Proceed, "", "user:ana"); err != nil {
		t.Fatal(err)
	}
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "created ENG-42: https://linear.app/acme/issue/ENG-42") ||
		!strings.Contains(snap.Result, "exit=0") {
		t.Fatalf("task: %+v", snap)
	}
	if !strings.HasSuffix(strings.TrimSpace(snap.Result), "0") {
		t.Errorf("the script's environment carries the credential:\n%s", snap.Result)
	}
	if len(auths) != 2 || auths[0] != "lin_api_secret" || auths[1] != "lin_api_secret" {
		t.Errorf("Linear saw %v, want the runner's credential on both calls", auths)
	}

	none := serveRepos(t, forge, Secrets{})
	launchScript(none, "t2", "#!/bin/sh\nviff-call-tool | grep -c linear || true")
	if snap := ended(t, none, "t2"); snap.Status != task.Done || strings.TrimSpace(snap.Result) != "0" {
		t.Errorf("a runner with no linear section lists: %+v", snap)
	}
	h := none.Launch(runner.Launch{ID: "t3", Autonomy: task.Autonomy{{Name: "linear", Match: &task.Match{System: "linear-issue", Verb: "create"}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenAllow}}},
		Script: "#!/bin/sh\necho hi", Environment: giving(envOf(netOpen, []runner.Repo{{Repo: "acme/svc"}}, nil), "linear-issue")})
	if err := h.Err(); !strings.Contains(err, "holds no `linear-issue`") {
		t.Errorf("a gate on a step the runner has nothing for: %q", err)
	}
}

// EACH ISSUE TOOL IS A VERB OF ITS TRACKER, and a step with the fields a row may match: the
// checkout's tracker names the repo and, past a filing, the issue; its pull and push the repo;
// the forge's verbs the repo and, past a filing, the issue; Linear's the team or the issue. The forge's tools are offered for the repositories at GitHub alone, Linear's
// only where the runner has its credential, and `repo` is required where there is more than one.
func TestAnIssueToolsStepNamesItsTracker(t *testing.T) {
	github := &workspace.Tree{Repo: workspace.Repo{Repo: runner.Repo{Repo: "example/app"}, Remote: "https://github.com/example/app.git"}}
	local := &workspace.Tree{Repo: workspace.Repo{Repo: runner.Repo{Repo: "x/l"}, Remote: "/tmp/x.git"}}
	ws := &workspace.Workspace{Trees: []*workspace.Tree{github, local}}
	cl := &delivery.Client{}

	trackers := []runner.Tracker{{Repo: "example/app"}, {Repo: "x/l"}}
	tools := issueTools(cl, ws, trackers, true, true)
	if len(tools) != 11 {
		t.Fatalf("tools offered: %d, want the eleven", len(tools))
	}
	type shape struct {
		step   string
		fields map[string]string
	}
	want := map[string]shape{
		gitIssueCreateTool:     {task.StepGitIssueCreate, map[string]string{"repo": "github.com/x/l"}},
		gitIssueCommentTool:    {task.StepGitIssueComment, map[string]string{"repo": "github.com/x/l", "issue": "7"}},
		gitIssueCloseTool:      {task.StepGitIssueClose, map[string]string{"repo": "github.com/x/l", "issue": "7"}},
		gitIssuePullTool:       {task.StepGitIssuePull, map[string]string{"repo": "github.com/x/l"}},
		gitIssuePushTool:       {task.StepGitIssuePush, map[string]string{"repo": "github.com/x/l"}},
		githubIssueCreateTool:  {task.StepGitHubIssueCreate, map[string]string{"repo": "github.com/example/app"}},
		githubIssueCommentTool: {task.StepGitHubIssueComment, map[string]string{"repo": "github.com/example/app", "issue": "7"}},
		githubIssueCloseTool:   {task.StepGitHubIssueClose, map[string]string{"repo": "github.com/example/app", "issue": "7"}},
		linearIssueCreateTool:  {task.StepLinearIssueCreate, map[string]string{"team": "ENG"}},
		linearIssueCommentTool: {task.StepLinearIssueComment, map[string]string{"issue": "ENG-42"}},
		linearIssueCloseTool:   {task.StepLinearIssueClose, map[string]string{"issue": "ENG-42"}},
	}
	for _, tl := range tools {
		w, ok := want[tl.Name]
		if !ok {
			t.Errorf("%s is offered and unexpected", tl.Name)
			continue
		}
		delete(want, tl.Name)
		if tl.Step != w.step {
			t.Errorf("%s is the step %q, want %q", tl.Name, tl.Step, w.step)
		}
		if w.step == "" {
			if tl.Fields != nil {
				t.Errorf("%s, no step, derives fields", tl.Name)
			}
			continue
		}
		in := map[string]any{"title": "t", "repo": "l", "issue": "7", "body": "b"}
		switch {
		case strings.HasPrefix(tl.Name, "github-"):
			in["repo"] = "app"
		case strings.HasPrefix(tl.Name, "linear-"):
			delete(in, "repo")
			in["team"], in["issue"] = "eng", "eng-42"
		}
		if got, ok := tl.Fields(asTaken(tl.Params, in)); !ok || !maps.Equal(got, w.fields) {
			t.Errorf("%s derives %v (%v), want %v", tl.Name, got, ok, w.fields)
		}
		if _, ok := tl.Fields(map[string]any{"repo": "nope"}); ok {
			t.Errorf("%s derives fields for a call it refuses", tl.Name)
		}
	}
	if len(want) != 0 {
		t.Errorf("not offered: %v", slices.Sorted(maps.Keys(want)))
	}
	byName := map[string]int{}
	for i, tl := range tools {
		byName[tl.Name] = i
	}
	if gh := tools[byName[githubIssueCreateTool]]; !strings.Contains(gh.Description, "Repositories: app, l.") || strings.Join(gh.Required, ",") != "repo,title" {
		t.Errorf("github-issue-create offers %q requiring %v, want every repository of a GitHub forge", gh.Description, gh.Required)
	}
	if gi := tools[byName[gitIssueCreateTool]]; !strings.Contains(gi.Description, "Repositories: app, l.") || strings.Join(gi.Required, ",") != "repo,title" {
		t.Errorf("git-issue-create offers %q requiring %v", gi.Description, gi.Required)
	}
	for _, tl := range issueTools(cl, ws, trackers, true, false) {
		if strings.HasPrefix(tl.Name, "linear-") {
			t.Errorf("%s offered with no linear section on the runner", tl.Name)
		}
	}
	if n, ok := issueNumber(map[string]any{"issue": "#12"}); !ok || n != 12 {
		t.Errorf("issue number from %q: %d %v", "#12", n, ok)
	}
}

// THE OFFERS HOLD THE TRACKERS THE RUNNER HAS WHAT IT TAKES FOR: a repository's tracker with
// repos, the forge's issues where the forge is GitHub, Linear's where a ceiling names it; every offer
// alike, since every doer reaches them through the same tools.
func TestTheOffersInterceptTheTrackersSteps(t *testing.T) {
	github := every(Config{Repos: &Repos{Mirrors: "m"}}, map[string]System{"linear-issue": {Ceiling: systems.Open()}})
	for _, o := range github.Describe(nil).Offers {
		for _, name := range []string{"git-issue", "github-issue", "linear-issue"} {
			if _, has := o.Systems[name]; !has {
				t.Errorf("the offer %s/%v does not hold %s: %v", o.Name(), o.Script, name, slices.Sorted(maps.Keys(o.Systems)))
			}
		}
	}
	dir := every(Config{Repos: &Repos{Mirrors: "m", Forge: "/forge"}}, nil)
	for _, o := range dir.Describe(nil).Offers {
		_, gi := o.Systems["git-issue"]
		_, ghi := o.Systems["github-issue"]
		_, li := o.Systems["linear-issue"]
		if !gi || ghi || li {
			t.Errorf("a directory forge with no linear: %v", slices.Sorted(maps.Keys(o.Systems)))
		}
	}
	if o := (Config{Script: &Script{}}).Describe(nil).Offers[0]; !slices.Equal(slices.Sorted(maps.Keys(o.Systems)), []string{"network", "question"}) {
		t.Errorf("no repos, no trackers: %v, want a script holding the network and its questions alone", o.Systems)
	}
}

// asTaken is one input given to every tool of a system, narrowed to what a tool takes: a tool
// refuses what it does not take (untaken).
func asTaken(params, in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		if _, ok := params[k]; ok {
			out[k] = v
		}
	}
	return out
}

// A TOOL REFUSES WHAT IT DOES NOT TAKE, RATHER THAN DROP IT AND GO ON. A close given a body
// would close without the comment and answer `closed`, and the model would believe the comment
// made; so would a reaction given text, a read given text, an update given `draft: true`, and a
// comment on a pull request given the id of a review comment it was meant as a reply to. Such a
// call derives no fields, so no row is asked about it, and is refused by the tool before it
// reaches the runner: the clients here have no connection to reach it by. What is given as
// nothing asks for nothing, and is let by.
func TestAToolRefusesWhatItDoesNotTake(t *testing.T) {
	github := &workspace.Tree{Repo: workspace.Repo{Repo: runner.Repo{Repo: "example/app"}, Remote: "https://github.com/example/app.git"}}
	ws := &workspace.Workspace{Branch: "viff/auto/t1", Trees: []*workspace.Tree{github}}
	names := []string{github.Repo.Name()}
	cl := &delivery.Client{}
	byName := map[string]int{}
	slacks := slackTools(cl)
	for i, tl := range slacks {
		byName[tl.Name] = i
	}
	githubClose := githubIssueVerb(cl, ws, names, githubIssueCloseTool, delivery.OpGitHubIssueClose, task.StepGitHubIssueClose)
	linearClose := linearIssueVerb(cl, linearIssueCloseTool, delivery.OpLinearIssueClose, task.StepLinearIssueClose)
	update := prTool(nil, ws, names, updatePRTool)
	commentPR := commentPRToolFor(cl, ws, names)
	for _, c := range []struct {
		fields func(map[string]any) (map[string]string, bool)
		do     func(context.Context, task.ToolUse) (string, bool, error)
		name   string
		in     map[string]any
		stray  string
	}{
		{githubClose.Fields, githubClose.Do, githubIssueCloseTool, map[string]any{"issue": 7, "body": "done, see the PR"}, "body"},
		{linearClose.Fields, linearClose.Do, linearIssueCloseTool, map[string]any{"issue": "eng-42", "body": "done, see the PR"}, "body"},
		{update.Fields, update.Do, updatePRTool, map[string]any{"title": "t", "draft": true}, "draft"},
		{commentPR.Fields, commentPR.Do, commentPRTool, map[string]any{"pr": 71, "body": "b", "comment": 9001}, "comment"},
		{slacks[byName[slackReactTool]].Fields, slacks[byName[slackReactTool]].Do, slackReactTool,
			map[string]any{"channel": "deploys", "message": "17.2", "emoji": "eyes", "text": "and a word"}, "text"},
		{slacks[byName[slackReadTool]].Fields, slacks[byName[slackReadTool]].Do, slackReadTool,
			map[string]any{"channel": "deploys", "text": "and a word"}, "text"},
		{slacks[byName[slackPostTool]].Fields, slacks[byName[slackPostTool]].Do, slackPostTool,
			map[string]any{"channel": "deploys", "text": "t", "thread": "17.1"}, "thread"},
	} {
		if f, ok := c.fields(c.in); ok {
			t.Errorf("%s given `%s` derives %v, want no fields", c.name, c.stray, f)
		}
		out, blocked, err := c.do(t.Context(), task.ToolUse{Name: c.name, Input: c.in})
		if want := "refused, and nothing was done: " + c.name + " takes no `" + c.stray + "`"; err != nil || !blocked || !strings.HasPrefix(out, want) {
			t.Errorf("%s given `%s`: %q, %v, %v, want %q", c.name, c.stray, out, blocked, err, want)
		}
		// And given it as nothing, the call is the one without it.
		for _, nothing := range []any{nil, "", false, 0, 0.0} {
			in := maps.Clone(c.in)
			in[c.stray] = nothing
			if _, ok := c.fields(in); !ok {
				t.Errorf("%s given `%s: %#v` derives no fields", c.name, c.stray, nothing)
			}
		}
	}
}
