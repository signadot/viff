package stock

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd"
	"go.viff.dev/viff/pkg/runner/runnerd/viff"
	"go.viff.dev/viff/pkg/runner/runnerd/viff/modeltest"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
)

// viffAgent is a runner configuration offering the viff agent alone, with the annotations
// and settings given.
func viffAgent(anns map[string]Annotation, settings map[string]Setting) Config {
	return Config{Agents: map[string]Agent{AgentName: {Annotations: anns, Settings: settings}}}
}

func TestCheckRefusesAnnotationsItDoesNotKnow(t *testing.T) {
	for _, tc := range []struct {
		c    Config
		want string
	}{
		{viffAgent(map[string]Annotation{"viff.portkey/route": {Type: "string"}}, nil), "takes no annotation"},
		{viffAgent(map[string]Annotation{viff.AnnotationVirtualKey: {Type: "int"}}, nil), "takes a string"},
		{Config{Agents: map[string]Agent{"codex": {Annotations: map[string]Annotation{viff.AnnotationVirtualKey: {Type: "string"}}}}}, "codex takes no annotation"},
	} {
		if err := tc.c.Check(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: got %v, want %q", tc.c, err, tc.want)
		}
	}
}

// The agent is offered where `agents:` names it, and scripts where `script:` is: presence is the
// switch.
func TestDescribeOffersWhatTheConfigNames(t *testing.T) {
	if d := (Config{Script: &Script{}}).Describe(nil); len(d.Offers) != 1 || !d.Offers[0].Script {
		t.Fatalf("scripts alone: %+v", d)
	}
	c := viffAgent(map[string]Annotation{viff.AnnotationVirtualKey: {Type: "string"}}, nil)
	d := c.Describe(nil)
	if len(d.Offers) != 1 || d.Offers[0].Agent == nil || d.Offers[0].Agent.Name != AgentName {
		t.Fatalf("the viff agent alone: %+v", d)
	}
	agent := d.Offers[0]
	// The systems it stops inside a run are held, closed until a ceiling says what of them.
	_, files := agent.Systems["files"]
	_, commands := agent.Systems["commands"]
	_, sub := agent.Systems["agent"]
	model, takesModel := agent.Agent.Settings[runner.SettingModel]
	if !takesModel || len(model.Values) != 0 || model.Default || agent.Agent.Annotations[viff.AnnotationVirtualKey] != "string" || !files || !commands || !sub ||
		len(agent.Systems["commands"].Ceiling) != 0 {
		t.Fatalf("agent offer: %+v", agent)
	}
	c.Script = &Script{}
	if d := c.Describe(nil); len(d.Offers) != 2 || d.Offers[1].Agent != nil {
		t.Fatalf("the agent and scripts: %+v", d)
	}
}

// The virtual key a launch's annotation picks is the one the model is called under; without one,
// the configured one is.
func TestTheVirtualKeyAnnotation(t *testing.T) {
	model := modeltest.New(t, func(modeltest.Request) modeltest.Reply { return modeltest.Say("ok") })
	r := New(opened(viffAgent(map[string]Annotation{viff.AnnotationVirtualKey: {Type: "string", Default: "default-vk"}}, nil)),
		Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL}, t.TempDir(), io.Discard)
	for i, picked := range []string{"picked", ""} {
		l := runner.Launch{ID: fmt.Sprint("t", i), Prompt: "go", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}},
			Agent: &runner.Agent{Name: AgentName}, Environment: envOf(netOpen, nil, nil)}
		if picked != "" {
			l.Agent.Annotations = map[string]string{viff.AnnotationVirtualKey: picked}
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
	for i, cfg := range []Config{viffAgent(nil, nil), viffAgent(map[string]Annotation{viff.AnnotationVirtualKey: {Type: "string", Default: "config-vk"}}, nil)} {
		l := runner.Launch{ID: fmt.Sprint("t", i), Prompt: "go", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}},
			Agent: &runner.Agent{Name: AgentName}, Environment: envOf(netOpen, nil, nil)}
		if _, err := New(cfg, secrets, t.TempDir(), io.Discard).Build(l, task.NewRegistry()).Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	seen := model.Seen()
	if len(seen) != 2 || seen[0].VirtualKey != "env-vk" || seen[1].VirtualKey != "config-vk" {
		t.Fatalf("the model was called under %+v, want env-vk then config-vk", seen)
	}
}

// Without a model key an agent launch that reaches the runner fails its own task (a runner
// configured with the viff agent and no key runs without it: Held).
func TestWithNoKeyThereIsNoAgent(t *testing.T) {
	_, err := New(opened(Config{}), Secrets{}, t.TempDir(), io.Discard).Build(runner.Launch{ID: "t1",
		Agent: &runner.Agent{Name: AgentName}, Prompt: "go"}, task.NewRegistry()).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no model key") {
		t.Errorf("got %v", err)
	}
}

// AN AGENT LAUNCH RUNS THE VIFF AGENT AS IT SAYS: under the model `with.model` names, with the
// launch's prompt, its `with.env` set in the agent's shell, and a policy that allows its
// command to run unasked.
func TestAnAgentLaunchRunsTheViffAgent(t *testing.T) {
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		if res := r.Results(); len(res) > 0 {
			out := strings.Split(strings.TrimSpace(res[0]), "\n") // run_bash's result ends with stdout
			return modeltest.Say("the branch is " + out[len(out)-1])
		}
		return modeltest.Use("run_bash", map[string]any{"command": "echo $BRANCH"})
	})
	d := New(opened(viffAgent(nil, map[string]Setting{runner.SettingModel: {Default: "the-default"}})), Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL},
		t.TempDir(), io.Discard)
	l := runner.Launch{ID: "t1", Prompt: "which branch?", Autonomy: allowing("commands"),
		Agent: &runner.Agent{Name: AgentName, Settings: map[string]string{runner.SettingModel: "m-2"}}, Environment: giving(withVars(envOf(netOpen, nil, nil), map[string]string{"BRANCH": "main"}), "commands")}
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
	// What it ran with is the record's to say: the launch's model, and the configured one where
	// the launch named none.
	if name, settings := p.Agent(); name != AgentName || settings[runner.SettingModel] != "m-2" {
		t.Errorf("the task says it ran as %q with model %q, want viff with m-2", name, settings[runner.SettingModel])
	}
	l.ID, l.Agent = "t2", &runner.Agent{Name: AgentName}
	if name, settings := d.Build(l, task.NewRegistry()).Agent(); name != AgentName || settings[runner.SettingModel] != "the-default" {
		t.Errorf("a launch naming no model: the task says %q with %q, want viff with the configured default", name, settings[runner.SettingModel])
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
	d := New(opened(Config{}), Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL}, t.TempDir(), io.Discard)
	reg := task.NewRegistry()
	p := d.Build(runner.Launch{ID: "t1", Prompt: "push it", Agent: &runner.Agent{Name: AgentName}, Environment: giving(envOf(netOpen, nil, nil), "commands"),
		Autonomy: task.Autonomy{{Name: "push", Match: &task.Match{System: "commands", Verb: "run", Params: map[string]*ir.Node{"command": systems.Regexp("^git push")}}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenAllow}}}}, reg)
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
	if q.Gate != "push" || q.Step.Name != task.StepCommandsRun || systems.FieldText(q.Step.Fields, "command") != "git push origin main" {
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
	r := New(opened(Config{}), Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL}, t.TempDir(), &log)
	p := r.Build(runner.Launch{ID: "t1", Prompt: "go", Autonomy: allowing("commands"), Agent: &runner.Agent{Name: AgentName}, Environment: giving(envOf(netOpen, nil, nil), "commands")}, task.NewRegistry())
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), "the-secret") || !strings.Contains(log.String(), "stdout 14 bytes") {
		t.Fatalf("the log:\n%s", log.String())
	}
}

// An agent's command asks under a row that names `commands` and asks, with no other gate written,
// and nothing asks before it starts. With no environment the agent has no shell: a command it
// proposes anyway is refused, and nobody is asked.
func TestUnderAskAnAgentsCommandAsks(t *testing.T) {
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		if res := r.Results(); len(res) > 0 {
			return modeltest.Say(res[0])
		}
		return modeltest.Use("run_bash", map[string]any{"command": "ls"})
	})
	r := New(opened(Config{}), Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL}, t.TempDir(), io.Discard)
	reg := task.NewRegistry()
	p := r.Build(runner.Launch{ID: "t1", Prompt: "look", Agent: &runner.Agent{Name: AgentName},
		Autonomy: task.Autonomy{{Match: &task.Match{System: "commands"}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenDeny}}}, Environment: giving(systems.Environment{}, "commands")}, reg)
	done := make(chan error, 1)
	go func() { _, err := p.Run(context.Background()); done <- err }()
	var pend []task.Pending
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && len(pend) == 0; time.Sleep(10 * time.Millisecond) {
		pend = reg.Pending()
	}
	if len(pend) != 1 || pend[0].Question.Step.Name != task.StepCommandsRun || pend[0].Question.Gate != "autonomy[0]" {
		t.Fatalf("pending %+v, want %s asked under the row on commands", pend, task.StepCommandsRun)
	}
	reg.Answer(pend[0].Question.ID, task.Choose(task.Proceed, "user:ana"))
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	reg = task.NewRegistry()
	var posted int
	reg.OnPost(func(task.Pending) { posted++ })
	p = r.Build(runner.Launch{ID: "t2", Prompt: "look", Agent: &runner.Agent{Name: AgentName}}, reg)
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if posted != 0 || !strings.Contains(p.Result(), "outside the environment") {
		t.Fatalf("with no environment: %d question(s), result %q; want the command refused and nobody asked", posted, p.Result())
	}
}

// A model base URL that names no host is refused as the runner starts: no network could let the
// agent reach it, and the launch would otherwise be accepted and fail only once the agent called.
func TestAHostlessModelURLIsAConfigError(t *testing.T) {
	_, err := Standalone(opened(Config{}), Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: "/v1"}, Tokens{},
		runnerd.Unconfined{}, []string{"true"}, t.TempDir(), t.TempDir(), nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), viff.EnvBaseURL) {
		t.Fatalf("Standalone with a hostless base url = %v, want it refused naming %s", err, viff.EnvBaseURL)
	}
}
