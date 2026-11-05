package verse

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/systems/systemstest"
	"github.com/signadot/verse/pkg/task"
)

func askCall(id, question string, options ...string) toolCall {
	args := map[string]any{"question": question}
	if len(options) > 0 {
		args["options"] = options
	}
	return askCallWith(id, args)
}

func askCallWith(id string, args map[string]any) toolCall {
	tc := mkTC(id, askTool)
	b, _ := json.Marshal(args)
	tc.Function.Arguments = string(b)
	return tc
}

// unasked is a policy that gives the task its questions and asks about nothing.
var unasked = task.Autonomy{{Match: &task.Match{System: "question"}, Then: task.Then{Mode: task.ThenAllow}}, {Then: task.Then{Mode: task.ThenAllow}}}

// askRun builds a task whose model asks once and then stops, and runs it against reg under g. It
// returns the fake (to read what the model was told) and the run's end.
func askRun(t *testing.T, ctx context.Context, reg *task.Registry, g task.Autonomy, call toolCall) (*fakeCompleter, <-chan error) {
	t.Helper()
	return askRunIn(t, ctx, reg, envGiving("question", "files", "commands", "agent"), g, call)
}

// envGiving is an environment of one block for each of names, giving every verb of it.
func envGiving(names ...string) systems.Environment {
	e := systems.Environment{Systems: systems.Systems{}}
	for _, name := range names {
		d, _ := systems.Get(name)
		verbs := map[string][]systems.Shape{}
		for _, v := range d.Verbs {
			verbs[v.Name] = []systems.Shape{{}}
		}
		e.Systems[name] = []systems.Block{{Verbs: verbs}}
	}
	return e
}

// askRunIn is askRun in env.
func askRunIn(t *testing.T, ctx context.Context, reg *task.Registry, env systems.Environment, g task.Autonomy, call toolCall) (*fakeCompleter, <-chan error) {
	t.Helper()
	fake := &fakeCompleter{turns: []scriptedTurn{
		{msg: chatMessage{Role: "assistant", ToolCalls: []toolCall{call}}, finish: "tool_calls"},
		{msg: chatMessage{Role: "assistant", Content: "thanks"}, finish: "stop"},
	}}
	p := Build(Config{ID: "t1", Prompt: "chat", Root: t.TempDir(), Resolver: reg, Client: fake, Environment: env, Ceilings: systemstest.Open(env), Autonomy: g})
	done := make(chan error, 1)
	go func() {
		_, err := p.Run(ctx)
		done <- err
	}()
	return fake, done
}

// pendingAsk waits for the task's question to reach the docket.
func pendingAsk(t *testing.T, reg *task.Registry) task.Pending {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ps := reg.Pending(); len(ps) > 0 {
			return ps[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the ask never reached the docket")
	return task.Pending{}
}

// toldAfterAsk is what the model read as the ask's result: the tool message of the second
// model call.
func toldAfterAsk(t *testing.T, fake *fakeCompleter) string {
	t.Helper()
	if len(fake.seen) < 2 {
		t.Fatalf("the model was called %d time(s), want 2", len(fake.seen))
	}
	msgs := fake.seen[1]
	last := msgs[len(msgs)-1]
	if last.Role != "tool" || last.Name != askTool {
		t.Fatalf("the last message before the second call is %s/%s, want the ask's result", last.Role, last.Name)
	}
	return last.Content
}

// AN ASK IS A QUESTION ON THE DOCKET, and its answer is what the model reads next.
func TestAskBlocksOnTheDocketAndReturnsTheAnswer(t *testing.T) {
	reg := task.NewRegistry()
	fake, done := askRun(t, context.Background(), reg, unasked, askCall("c1", "which one?"))

	p := pendingAsk(t, reg)
	if !slices.Equal(p.Question.Answerers, []string{task.PrincipalAnswerer}) {
		t.Errorf("the ask is not put to the task's person: %+v", p.Question)
	}
	if p.Tool != askTool || p.Question.Reason != "which one?" || p.Question.Origin != "t1" {
		t.Fatalf("the docket holds %+v", p)
	}
	if !strings.HasPrefix(p.Question.ID, "t1"+task.GateSep) {
		t.Fatalf("question id %q does not carry the task's id, so no answer could find it", p.Question.ID)
	}
	if len(p.Question.Options) != 1 || p.Question.Options[0].ID != replyOption {
		t.Fatalf("an unnamed question offers %+v, want the one option %q", p.Question.Options, replyOption)
	}
	reg.Answer(p.Question.ID, task.Resolution{Choice: replyOption, By: "user:t", Steer: []string{"the second"}})
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := toldAfterAsk(t, fake); got != "user:t: the second" {
		t.Fatalf("the model read %q", got)
	}
}

// A NAMED OUTCOME COMES BACK BY ITS ID.
func TestAskWithOptionsReturnsTheChoice(t *testing.T) {
	reg := task.NewRegistry()
	fake, done := askRun(t, context.Background(), reg, unasked, askCall("c1", "a or b?", "a", "b"))
	p := pendingAsk(t, reg)
	if len(p.Question.Options) != 2 || p.Question.Options[1].ID != "b" {
		t.Fatalf("options %+v", p.Question.Options)
	}
	reg.Answer(p.Question.ID, task.Resolution{Choice: "b", By: "user:t"})
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := toldAfterAsk(t, fake); got != "user:t chose b" {
		t.Fatalf("the model read %q", got)
	}
}

// An ask that names answerers holds the question out to them, and not to the task's own person.
func TestAskNamesItsAnswerers(t *testing.T) {
	reg := task.NewRegistry()
	_, done := askRun(t, context.Background(), reg, unasked, askCallWith("c1", map[string]any{
		"question": "install?", "options": []string{"proceed", "deny"}, "answerers": []string{"user:*", " group:owners "},
	}))
	p := pendingAsk(t, reg)
	if got := p.Question.Answerers; !slices.Equal(got, []string{"user:*", "group:owners"}) {
		t.Fatalf("answerers %v, want the two the call named", got)
	}
	reg.Answer(p.Question.ID, task.Resolution{Choice: "proceed", By: "user:t"})
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	// Named nobody: the task's own person, as before.
	reg = task.NewRegistry()
	_, done = askRun(t, context.Background(), reg, unasked, askCall("c2", "go?", "yes"))
	p = pendingAsk(t, reg)
	if got := p.Question.Answerers; !slices.Equal(got, []string{task.PrincipalAnswerer}) {
		t.Fatalf("answerers %v, want the principal", got)
	}
	reg.Answer(p.Question.ID, task.Resolution{Choice: "yes", By: "user:t"})
	<-done
}

// AN ALLOWED ASK IS ASKED ONCE. Under gates on every other step an agent can take, a row that
// allows `question` puts the question at once: the person is not asked whether the task may ask
// them, and only then what it wanted to know.
func TestAnAllowedAskIsAskedOnce(t *testing.T) {
	reg := task.NewRegistry()
	var posted int
	reg.OnPost(func(task.Pending) { posted++ })
	ask := task.Then{Mode: task.ThenAsk}
	fake, done := askRun(t, context.Background(), reg, task.Autonomy{
		{Name: "w", Match: &task.Match{System: "files", Verb: "write"}, Then: ask},
		{Name: "c", Match: &task.Match{System: "commands", Verb: "run"}, Then: ask},
		{Name: "d", Match: &task.Match{System: "agent", Verb: "call-sub-agent"}, Then: ask},
		{Match: &task.Match{System: "question"}, Then: task.Then{Mode: task.ThenAllow}},
		{Then: ask},
	}, askCall("c1", "go on?"))
	p := pendingAsk(t, reg)
	if p.Tool != askTool || p.Question.Gate != askTool {
		t.Fatalf("the first question on the docket is the supervisor's, not the task's: %+v", p)
	}
	reg.Answer(p.Question.ID, task.Resolution{Choice: replyOption, By: "user:t", Steer: []string{"yes"}})
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if posted != 1 {
		t.Fatalf("%d questions were posed, want exactly the ask", posted)
	}
	if got := toldAfterAsk(t, fake); got != "user:t: yes" {
		t.Fatalf("the model read %q", got)
	}
}

// AN ASK THE ENVIRONMENT DOES NOT GIVE IS REFUSED. An environment with no `question` does not
// give the task its questions: the tool is not offered, and a call of it anyway puts nothing on
// the docket and is refused, whatever the default row says.
func TestAnAskTheEnvironmentDoesNotGiveIsRefused(t *testing.T) {
	reg := task.NewRegistry()
	var posted int
	reg.OnPost(func(task.Pending) { posted++ })
	fake, done := askRunIn(t, context.Background(), reg, envGiving("files"), task.Autonomy{
		{Match: &task.Match{System: "files"}, Then: task.Then{Mode: task.ThenAllow}},
		{Then: task.Then{Mode: task.ThenAllow}},
	}, askCall("c1", "go on?"))
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if posted != 0 {
		t.Fatalf("%d questions were posed, want none", posted)
	}
	if got := toldAfterAsk(t, fake); !strings.Contains(got, "the environment gives no `question` (it gives: files)") {
		t.Fatalf("the model read %q, want the refusal", got)
	}
}

// A CANCELLED ASK LEAVES THE DOCKET: the question is withdrawn with its task, not left for
// somebody to answer into nothing.
func TestACancelledAskLeavesTheDocket(t *testing.T) {
	reg := task.NewRegistry()
	dropped := make(chan string, 1)
	reg.OnDrop(func(q task.GateQuestion, _ string) { dropped <- q.ID })
	ctx, cancel := context.WithCancel(context.Background())
	_, done := askRun(t, ctx, reg, unasked, askCall("c1", "still there?"))
	p := pendingAsk(t, reg)
	cancel()
	<-done
	select {
	case id := <-dropped:
		if id != p.Question.ID {
			t.Fatalf("dropped %q, want %q", id, p.Question.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the question was not withdrawn")
	}
	if ps := reg.Pending(); len(ps) != 0 {
		t.Fatalf("the docket still holds %+v", ps)
	}
}

// A QUESTION WAITS UNTIL IT IS ANSWERED unless the model bounds it: a conversation hands the
// person the turn, and they take it whenever they come back.
func TestAskWithoutWithinHasNoClock(t *testing.T) {
	reg := task.NewRegistry()
	_, done := askRun(t, context.Background(), reg, unasked, askCall("c1", "which one?"))
	p := pendingAsk(t, reg)
	if p.Question.Deadline != nil {
		t.Fatalf("an ask with no within has a clock: %+v", p.Question.Deadline)
	}
	reg.Answer(p.Question.ID, task.Resolution{Choice: replyOption, By: "user:t", Steer: []string{"x"}})
	<-done
}

// WITHIN BOUNDS THE WAIT, and default is what silence settles on; the model reads that nobody
// answered, and what it came to.
func TestAskWithinIsTheQuestionsClock(t *testing.T) {
	reg := task.NewRegistry()
	fake, done := askRun(t, context.Background(), reg, unasked, askCallWith("c1", map[string]any{
		"question": "a or b?", "options": []string{"a", "b"}, "within": "30m", "default": "b"}))
	p := pendingAsk(t, reg)
	if d := p.Question.Deadline; d == nil || d.Within != "30m" || d.Default != "b" {
		t.Fatalf("the question's clock is %+v, want within 30m defaulting to b", d)
	}
	reg.Answer(p.Question.ID, task.Choose("b", task.ByDeadline))
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := toldAfterAsk(t, fake); got != "nobody answered within 30m, so it settled on b" {
		t.Fatalf("the model read %q", got)
	}
}

// A QUESTION IN WORDS THAT RUNS OUT settles on nothing, and says so.
func TestAnAskInWordsThatRunsOutSaysNobodyAnswered(t *testing.T) {
	reg := task.NewRegistry()
	fake, done := askRun(t, context.Background(), reg, unasked, askCallWith("c1", map[string]any{
		"question": "anything?", "within": "2s"}))
	p := pendingAsk(t, reg)
	reg.Answer(p.Question.ID, task.Choose(task.NoOption, task.ByDeadline))
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := toldAfterAsk(t, fake); got != "nobody answered within 2s" {
		t.Fatalf("the model read %q", got)
	}
}

// A CLOCK THAT CANNOT MEAN ANYTHING is refused to the model, which reads why, and nothing is posed.
func TestAskRefusesAClockThatMeansNothing(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"within not a duration":  {"question": "q", "within": "soon"},
		"within not positive":    {"question": "q", "within": "0s"},
		"default without within": {"question": "q", "options": []string{"a"}, "default": "a"},
		"default not an option":  {"question": "q", "options": []string{"a"}, "within": "1h", "default": "c"},
		"default on words":       {"question": "q", "within": "1h", "default": replyOption},
	} {
		reg := task.NewRegistry()
		fake, done := askRun(t, context.Background(), reg, unasked, askCallWith("c1", args))
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: run: %v", name, err)
			}
		case <-time.After(2 * time.Second):
			// Posed, so the run waits on it: say so, and answer it to let the run end.
			for _, p := range reg.Pending() {
				reg.Answer(p.Question.ID, task.Resolution{Choice: replyOption, By: "user:t"})
			}
			<-done
			t.Errorf("%s: a question was posed", name)
			continue
		}
		if got := toldAfterAsk(t, fake); !strings.Contains(got, "ask:") {
			t.Errorf("%s: the model read %q, want the refusal", name, got)
		}
	}
}
