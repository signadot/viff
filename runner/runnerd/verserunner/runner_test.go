package verserunner

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/verserunner/agent/modeltest"
	"github.com/signadot/verse/task"
)

func TestCheckRefusesAnnotationsItDoesNotKnow(t *testing.T) {
	for _, tc := range []struct {
		c    Config
		want string
	}{
		{Config{Annotations: map[string]string{"verse.portkey/route": "string"}}, "no annotation"},
		{Config{Annotations: map[string]string{AnnotationVirtualKey: "int"}}, "takes a string"},
	} {
		if err := tc.c.Check(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: got %v, want %q", tc.c, err, tc.want)
		}
	}
}

// The agent is offered only when the runner can reach its model; scripts always are.
func TestDescribeOffersTheAgentOnlyWithAKey(t *testing.T) {
	c := Config{Annotations: map[string]string{AnnotationVirtualKey: "string"}}

	d := c.Describe(Secrets{}, nil)
	if len(d.Offers) != 1 || !d.Offers[0].Script || d.Offers[0].With != nil {
		t.Fatalf("without a key: %+v", d)
	}

	d = c.Describe(Secrets{PortkeyAPIKey: "pk-test"}, nil)
	if len(d.Offers) != 2 || d.Offers[0].Agent != AgentName {
		t.Fatalf("with a key: %+v", d)
	}
	agent := d.Offers[0]
	if !agent.With["model"].Any || agent.Annotations[AnnotationVirtualKey] != "string" ||
		agent.Systems["files"].Ceiling == nil || agent.Systems["commands"].Ceiling == nil || agent.Systems["agent"].Ceiling == nil {
		t.Fatalf("agent offer: %+v", agent)
	}
}

// The virtual key a launch's annotation picks is the one the model is called under; without one,
// the configured one is.
func TestTheVirtualKeyAnnotation(t *testing.T) {
	model := modeltest.New(t, func(modeltest.Request) modeltest.Reply { return modeltest.Say("ok") })
	r := New(Config{Models: Models{Portkey: Portkey{VirtualKey: "default-vk"}},
		Annotations: map[string]string{AnnotationVirtualKey: "string"}},
		Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL}, t.TempDir(), io.Discard)
	for i, picked := range []string{"picked", ""} {
		l := runner.Launch{ID: fmt.Sprint("t", i), Prompt: "go", Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}},
			Doer: runner.Doer{Agent: AgentName, With: runner.With{Network: netOpen}}}
		if picked != "" {
			l.Doer.Annotations = map[string]string{AnnotationVirtualKey: picked}
		}
		if _, err := r.Build(l, task.NewRegistry()).Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	seen := model.Seen()
	if len(seen) != 2 || seen[0].VirtualKey != "picked" || seen[1].VirtualKey != "default-vk" {
		t.Fatalf("the model was called under %+v, want picked then default-vk", seen)
	}
}

// The deployment's virtual key, read from PORTKEY_VIRTUAL_KEY beside its API key, is the one a run
// uses when neither the launch nor the configuration names one; the configuration's comes first.
func TestTheVirtualKeyFromTheEnvironment(t *testing.T) {
	model := modeltest.New(t, func(modeltest.Request) modeltest.Reply { return modeltest.Say("ok") })
	secrets := Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL, PortkeyVirtualKey: "env-vk"}
	for i, cfg := range []Config{{}, {Models: Models{Portkey: Portkey{VirtualKey: "config-vk"}}}} {
		l := runner.Launch{ID: fmt.Sprint("t", i), Prompt: "go", Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}},
			Doer: runner.Doer{Agent: AgentName, With: runner.With{Network: netOpen}}}
		if _, err := New(cfg, secrets, t.TempDir(), io.Discard).Build(l, task.NewRegistry()).Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	seen := model.Seen()
	if len(seen) != 2 || seen[0].VirtualKey != "env-vk" || seen[1].VirtualKey != "config-vk" {
		t.Fatalf("the model was called under %+v, want env-vk then config-vk", seen)
	}
}

// Without a model key the runner offers no agent, and an agent launch that reaches it anyway
// fails its own task.
func TestWithNoKeyThereIsNoAgent(t *testing.T) {
	_, err := New(Config{}, Secrets{}, t.TempDir(), io.Discard).Build(runner.Launch{ID: "t1",
		Doer: runner.Doer{Agent: AgentName}, Prompt: "go"}, task.NewRegistry()).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no model key") {
		t.Errorf("got %v", err)
	}
}

// AN AGENT LAUNCH RUNS THE VERSE AGENT AS IT SAYS: under the model `with.model` names, with the
// launch's prompt, its `with.env` set in the agent's shell, and a policy that allows its
// command to run unasked.
func TestAnAgentLaunchRunsTheVerseAgent(t *testing.T) {
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		if res := r.Results(); len(res) > 0 {
			out := strings.Split(strings.TrimSpace(res[0]), "\n") // run_bash's result ends with stdout
			return modeltest.Say("the branch is " + out[len(out)-1])
		}
		return modeltest.Use("run_bash", map[string]any{"command": "echo $BRANCH"})
	})
	d := New(Config{Models: Models{Model: "the-default"}}, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL},
		t.TempDir(), io.Discard)
	l := runner.Launch{ID: "t1", Prompt: "which branch?", Policy: allowing("commands"),
		Doer: runner.Doer{Agent: AgentName,
			With: runner.With{Network: netOpen, Model: "m-2", Env: map[string]string{"BRANCH": "main"}}}}
	p := d.Build(l, task.NewRegistry())
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := p.Result(); got != "the branch is main" {
		t.Errorf("result = %q, want the branch the launch's env set", got)
	}
	seen := model.Seen()
	if len(seen) == 0 || seen[0].Model != "m-2" || seen[0].Prompt() != "which branch?" {
		t.Fatalf("the model was asked %+v, want model m-2 and the launch's prompt", seen)
	}
}

// A command a launch's gate names asks first, and the question says the step and its fields. A
// denial is the agent's to read, not the task's end.
func TestAnAgentsGatedCommandAsks(t *testing.T) {
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		if res := r.Results(); len(res) > 0 {
			return modeltest.Say("told: " + res[0])
		}
		return modeltest.Use("run_bash", map[string]any{"command": "git push origin main"})
	})
	d := New(Config{}, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL}, t.TempDir(), io.Discard)
	reg := task.NewRegistry()
	p := d.Build(runner.Launch{ID: "t1", Prompt: "push it", Doer: runner.Doer{Agent: AgentName, With: runner.With{Network: netOpen}},
		Policy: task.Policy{{Name: "push", Match: &task.Match{System: "commands", Verb: "run", Params: map[string]string{"command": "git push*"}}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenAllow}}}}, reg)
	done := make(chan error, 1)
	go func() { _, err := p.Run(context.Background()); done <- err }()
	var pend []task.Pending
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && len(pend) == 0; time.Sleep(10 * time.Millisecond) {
		pend = reg.Pending()
	}
	if len(pend) != 1 {
		t.Fatal("the gated command didn't ask")
	}
	q := pend[0].Question
	if q.Gate != "push" || q.Step.Name != task.StepCommandsRun || q.Step.Fields["command"] != "git push origin main" {
		t.Fatalf("asked %+v", q)
	}
	reg.Answer(q.ID, task.Choose(task.Deny, "user:ana"))
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := p.Result(); !strings.Contains(got, "denied by user:ana") {
		t.Errorf("the agent was told %q, want the denial", got)
	}
}

// The runner's log says what the agent did and how much, never the task's content: not a
// command's arguments or output, and not the model's words. Any of them can hold what the task
// was given to keep, such as its credential; the read plane carries them.
func TestTheLogKeepsACommandsOutputOut(t *testing.T) {
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		if len(r.Results()) > 0 {
			return modeltest.Say("it said the-secret-word")
		}
		return modeltest.Use("run_bash", map[string]any{"command": "echo the-secret-$((40+2)) # the-secret-arg"})
	})
	var log strings.Builder
	r := New(Config{}, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL}, t.TempDir(), &log)
	p := r.Build(runner.Launch{ID: "t1", Prompt: "go", Policy: allowing("commands"), Doer: runner.Doer{Agent: AgentName, With: runner.With{Network: netOpen}}}, task.NewRegistry())
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), "the-secret") || !strings.Contains(log.String(), "stdout 14 bytes") {
		t.Fatalf("the log:\n%s", log.String())
	}
}

// An agent's command asks under a row that names `commands` and asks, with no other gate written,
// and nothing asks before it starts. With no policy the agent has no shell: a command it
// proposes anyway is refused, and nobody is asked.
func TestUnderAskAnAgentsCommandAsks(t *testing.T) {
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		if res := r.Results(); len(res) > 0 {
			return modeltest.Say(res[0])
		}
		return modeltest.Use("run_bash", map[string]any{"command": "ls"})
	})
	r := New(Config{}, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL}, t.TempDir(), io.Discard)
	reg := task.NewRegistry()
	p := r.Build(runner.Launch{ID: "t1", Prompt: "look", Doer: runner.Doer{Agent: AgentName},
		Policy: task.Policy{{Match: &task.Match{System: "commands"}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenDeny}}}}, reg)
	done := make(chan error, 1)
	go func() { _, err := p.Run(context.Background()); done <- err }()
	var pend []task.Pending
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && len(pend) == 0; time.Sleep(10 * time.Millisecond) {
		pend = reg.Pending()
	}
	if len(pend) != 1 || pend[0].Question.Step.Name != task.StepCommandsRun || pend[0].Question.Gate != "policy[0]" {
		t.Fatalf("pending %+v, want %s asked under the row on commands", pend, task.StepCommandsRun)
	}
	reg.Answer(pend[0].Question.ID, task.Choose(task.Proceed, "user:ana"))
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	reg = task.NewRegistry()
	var posted int
	reg.OnPost(func(task.Pending) { posted++ })
	p = r.Build(runner.Launch{ID: "t2", Prompt: "look", Doer: runner.Doer{Agent: AgentName}}, reg)
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if posted != 0 || !strings.Contains(p.Result(), "not provided") {
		t.Fatalf("with no policy: %d question(s), result %q; want the command refused and nobody asked", posted, p.Result())
	}
}

// A model base URL that names no host is refused as the runner starts: no network could let the
// agent reach it, and the launch would otherwise be accepted and fail only once the agent called.
func TestAHostlessModelURLIsAConfigError(t *testing.T) {
	_, err := Standalone(Config{}, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: "/v1"}, Tokens{},
		runnerd.Unconfined{}, []string{"true"}, t.TempDir(), t.TempDir(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), EnvPortkeyBaseURL) {
		t.Fatalf("Standalone with a hostless base url = %v, want it refused naming %s", err, EnvPortkeyBaseURL)
	}
}
