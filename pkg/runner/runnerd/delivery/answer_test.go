package delivery

import (
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// AN ANSWER IS TO THE CALL ITS QUESTION SHOWED, AND TO NO OTHER. The task's process names the
// decision its act is performed on, and says what it likes: a secret.exec naming the decision of
// an answer about another command, in another directory or with other variables, is refused,
// naming what differs and no value, and the answer is kept. The call that was answered is then
// performed on it, once; a second act on the same decision is asked about again. An answer
// about no call the runner read is matched by its decision alone.
func TestAnAnswerPerformsTheCallItWasAbout(t *testing.T) {
	asks := task.Autonomy{{Name: "publish", Match: &task.Match{System: "secret"}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenAllow}}}
	// No store: an act that is performed reaches it and is refused for the store, which is what
	// says the decision let it through.
	s := &Server{Log: io.Discard, Ceilings: secretCeiling(t)}
	ch := &Channel{}
	tk := Task{ID: "t1", Root: "/w", Launch: secretLaunch([]systems.Shape{{"env": pattern(t, `null`)}}, asks), Answered: ch.answered}
	exec := func(decision, body, dir string) Reply {
		return s.answer(tk, frame(t, Request{Op: OpSecretExec, Decision: decision, Body: body, Fields: map[string]string{"name": "npm-token", "dir": dir}}))
	}
	answered := Request{Op: OpSecretExec, Body: "{argv: [npm, publish], env: {NODE_ENV: production}}", Fields: map[string]string{"name": "npm-token", "dir": "packages/a"}}
	about, why := ReadExec(tk.environment(), "npm-token", []string{"npm", "publish"}, map[string]string{"NODE_ENV": "production"}, "packages/a")
	if why != "" {
		t.Fatal(why)
	}
	ch.Answer("t1~d1", about.Step(), task.Proceed, "user:ana")
	for _, c := range []struct {
		name, body, dir, differs string
	}{
		{"another command", "{argv: [npm, publish, --tag, next], env: {NODE_ENV: production}}", "packages/a", "argv"},
		{"another directory", "{argv: [npm, publish], env: {NODE_ENV: production}}", "packages/b", "dir"},
		{"another variable", "{argv: [npm, publish], env: {NODE_ENV: production, NPM_CONFIG_REGISTRY: https://registry.invalid}}", "packages/a", "env"},
		{"another value", "{argv: [npm, publish], env: {NODE_ENV: staging}}", "packages/a", "env"},
	} {
		rep := exec("t1~d1", c.body, c.dir)
		if !strings.Contains(rep.Refused, "the answer to t1~d1 is about another call: this one differs in "+c.differs+". Nothing was done") {
			t.Errorf("%s: %+v, want it refused, naming %s", c.name, rep, c.differs)
		}
		if strings.Contains(rep.Refused, "staging") || strings.Contains(rep.Refused, "registry.invalid") || strings.Contains(rep.Refused, "next") {
			t.Errorf("%s: the refusal says a value: %s", c.name, rep.Refused)
		}
		if n := ch.Answers(); n != 1 {
			t.Errorf("%s: %d answer(s) kept, want the one, unspent", c.name, n)
		}
	}
	if rep := exec("t1~d1", answered.Body, "packages/a"); !strings.Contains(rep.Refused, "no secret to run with") || ch.Answers() != 0 {
		t.Errorf("the call that was answered: %+v, %d answer(s) left, want it performed on the answer", rep, ch.Answers())
	}
	if rep := exec("t1~d1", answered.Body, "packages/a"); !strings.Contains(rep.Refused, "relayed no answer") {
		t.Errorf("a second act on the one answer: %+v, want it asked about again", rep)
	}
	// About no call the runner read: by the decision alone.
	ch.Answer("t1~d2", task.Step{}, task.Proceed, "user:bo")
	if rep := exec("t1~d2", "{argv: [npm, view]}", ""); !strings.Contains(rep.Refused, "no secret to run with") || ch.Answers() != 0 {
		t.Errorf("an answer about no call this side read: %+v, want it performed by the decision", rep)
	}
}

// A POST IS PERFORMED ON AN ANSWER ABOUT ITS CHANNEL, and an answer about a post to #deploys
// does not send one to #general, the text being no field of the step.
func TestAnAnswerAboutAPostToOneChannelPostsToNoOther(t *testing.T) {
	var saw []string
	s := &Server{Log: io.Discard, Services: Services{SlackToken: Holds("xoxb-own"), SlackAPI: slackAPI(t, &saw)},
		Ceilings: map[string]systems.Ceiling{"slack": systems.Open()}}
	l := within(nil, "slack")
	l.Autonomy = task.Autonomy{{Name: "announce", Match: &task.Match{System: "slack", Verb: "post"}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenAllow}}}
	ch := &Channel{}
	tk := Task{ID: "t7", Workspace: &workspace.Workspace{}, Launch: l, Answered: ch.answered}
	ch.Answer("t7~d1", task.NewStep(task.StepSlackPost, map[string]string{"channel": "deploys"}), task.Proceed, "user:ana")
	post := func(channel string) Reply {
		return s.answer(tk, frame(t, Request{Op: OpSlackPost, Decision: "t7~d1", Body: "v1.3 is out", Fields: map[string]string{"channel": channel}}))
	}
	if rep := post("general"); !strings.Contains(rep.Refused, "differs in channel") || len(saw) != 0 || ch.Answers() != 1 {
		t.Errorf("a post to #general on the answer about #deploys: %+v (sent %v), want it refused and the answer kept", rep, saw)
	}
	if rep := post("#Deploys"); !rep.OK || len(saw) != 1 || !strings.Contains(saw[0], " post C1 ") {
		t.Errorf("the post that was answered, however it spells the channel: %+v (sent %v)", rep, saw)
	}
}

// DIFFERS NAMES WHAT A STEP SAYS OTHERWISE, by field and never by value: a field changed, one
// added, one dropped, a verb that is another, and nothing for the same step spelled otherwise.
func TestDiffersNamesTheFieldsAndNoValue(t *testing.T) {
	push := task.NewStep(task.StepGitPush, map[string]string{"repo": "github.com/acme/svc", "ref": "refs/heads/a", "force": "false"})
	for _, c := range []struct {
		name string
		o    task.Step
		want []string
	}{
		{"the same", task.NewStep(task.StepGitPush, map[string]string{"force": "false", "ref": "refs/heads/a", "repo": "github.com/acme/svc"}), nil},
		{"a field changed", push.With("ref", ir.FromString("refs/heads/main")), []string{"ref"}},
		{"a field added", push.With("own", ir.FromBool(true)), []string{"own"}},
		{"two changed", push.With("ref", ir.FromString("refs/heads/main")).With("force", ir.FromBool(true)), []string{"ref", "force"}},
		{"a field dropped", task.NewStep(task.StepGitPush, map[string]string{"repo": "github.com/acme/svc", "ref": "refs/heads/a"}), []string{"force"}},
		{"another verb", task.NewStep(task.StepGitFetch, map[string]string{"repo": "github.com/acme/svc"}), []string{"name"}},
	} {
		if got := push.Differs(c.o); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
