package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/systems/systemstest"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd/verse"
	"github.com/signadot/verse/task"
)

// workEnv is the environment codex runs in: its shell, its edits and its questions, which it
// cannot run without (the first two) or asks through (the third).
var workEnv = systems.Environment{Systems: systems.Systems{
	"commands": {{Verbs: map[string][]systems.Shape{"run": {{}}}}},
	"files":    {{Verbs: map[string][]systems.Shape{"write": {{}}}}},
	"question": {{Verbs: map[string][]systems.Shape{"ask": {{}}}}},
}}

// The codex agent is tested against a stand-in `codex app-server`: this test binary,
// re-executed (TestMain) with fakeEnvVar naming a scenario. The stand-in speaks
// the same JSON-RPC over stdio the real one does — the message shapes come from the schema the
// real binary generates — and writes what it was told to a log the test reads back. So what is
// asserted is the conversation: what verse answered codex, and what codex's answers became on
// verse's side.

const (
	fakeEnvVar = "VERSE_RUNNER_FAKE_CODEX"
	fakeLogVar = "VERSE_RUNNER_FAKE_LOG"
)

// fakeServer is the stand-in. It handles the client's requests inline and runs each turn
// as a script, in a goroutine, that waits for the client's decisions.
func fakeServer() {
	f := &fake{out: os.Stdout, replies: map[int64]chan json.RawMessage{}, interrupted: make(chan struct{})}
	if p := os.Getenv(fakeLogVar); p != "" {
		lf, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			f.log = lf
			defer lf.Close()
		}
	}
	scenario := os.Getenv(fakeEnvVar)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		if m.Method == "" {
			// The client's answer to a request the script made.
			var id int64
			if json.Unmarshal(m.ID, &id) == nil {
				f.mu.Lock()
				ch := f.replies[id]
				delete(f.replies, id)
				f.mu.Unlock()
				if ch != nil {
					ch <- m.Result
				}
			}
			continue
		}
		f.record(map[string]any{"method": m.Method, "params": m.Params})
		switch m.Method {
		case "initialize":
			// How this process was given its login, for the test that asserts the shape.
			auth := map[string]any{"codexHome": os.Getenv("CODEX_HOME"), "keyInEnv": os.Getenv("OPENAI_API_KEY") != ""}
			auth["credentialFile"] = os.Getenv("VERSE_CREDENTIAL_FILE")
			if b, err := os.ReadFile(filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")); err == nil {
				var a map[string]any
				_ = json.Unmarshal(b, &a)
				auth["authMode"], auth["key"] = a["auth_mode"], a["OPENAI_API_KEY"]
			}
			f.record(map[string]any{"auth": auth})
			f.respond(m.ID, map[string]any{"userAgent": "fake"})
		case "initialized":
		case "thread/start":
			f.respond(m.ID, map[string]any{"thread": map[string]any{"id": "th1"}, "model": "fake-model"})
			f.notify("thread/started", map[string]any{"thread": map[string]any{"id": "th1"}})
		case "turn/start":
			f.turns++
			turn := fmt.Sprintf("tu%d", f.turns)
			f.respond(m.ID, map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress"}})
			f.notify("turn/started", map[string]any{"turn": map[string]any{"id": turn}})
			var in struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			_ = json.Unmarshal(m.Params, &in)
			text := ""
			if len(in.Input) > 0 {
				text = in.Input[0].Text
			}
			go f.turn(scenario, turn, text)
		case "turn/steer":
			f.respond(m.ID, map[string]any{})
		case "turn/interrupt":
			f.respond(m.ID, map[string]any{})
			f.once.Do(func() { close(f.interrupted) })
		default:
			f.respond(m.ID, map[string]any{})
		}
	}
}

type fake struct {
	out io.Writer
	log *os.File
	wmu sync.Mutex

	mu      sync.Mutex
	seq     int64
	replies map[int64]chan json.RawMessage

	turns       int
	once        sync.Once
	interrupted chan struct{}
}

func (f *fake) write(msg any) {
	b, _ := json.Marshal(msg)
	f.wmu.Lock()
	defer f.wmu.Unlock()
	fmt.Fprintf(f.out, "%s\n", b)
}

func (f *fake) respond(id json.RawMessage, result any) {
	f.write(map[string]any{"id": id, "result": result})
}

func (f *fake) notify(method string, params any) {
	f.write(map[string]any{"method": method, "params": params})
}

// request asks the client and waits for its answer.
func (f *fake) request(method string, params any) json.RawMessage {
	f.mu.Lock()
	f.seq++
	id := f.seq
	ch := make(chan json.RawMessage, 1)
	f.replies[id] = ch
	f.mu.Unlock()
	f.write(map[string]any{"id": id, "method": method, "params": params})
	return <-ch
}

func (f *fake) record(entry map[string]any) {
	if f.log == nil {
		return
	}
	b, _ := json.Marshal(entry)
	f.wmu.Lock()
	defer f.wmu.Unlock()
	fmt.Fprintf(f.log, "%s\n", b)
}

func (f *fake) item(turn string, item map[string]any, completed bool) {
	method := "item/started"
	if completed {
		method = "item/completed"
	}
	f.notify(method, map[string]any{"threadId": "th1", "turnId": turn, "item": item})
}

func (f *fake) completeTurn(turn, status string, errMsg string) {
	t := map[string]any{"id": turn, "status": status}
	if errMsg != "" {
		t["error"] = map[string]any{"message": errMsg}
	}
	f.notify("turn/completed", map[string]any{"threadId": "th1", "turn": t})
}

// turn is one scripted turn.
func (f *fake) turn(scenario, turn, text string) {
	cwd, _ := os.Getwd()
	if f.turns > 1 {
		// A later turn: a steer, a repair or a reply. Echo it and end.
		f.item(turn, map[string]any{"id": "m" + turn, "type": "agentMessage", "text": "again: " + text}, true)
		f.completeTurn(turn, "completed", "")
		return
	}
	switch scenario {
	case "fail":
		f.completeTurn(turn, "failed", "401 Unauthorized")

	case "trusted":
		// A command codex's own policy trusted: run without asking, reported when done.
		f.item(turn, map[string]any{"id": "c0", "type": "commandExecution", "command": "ls", "cwd": cwd, "status": "inProgress"}, false)
		f.item(turn, map[string]any{"id": "c0", "type": "commandExecution", "command": "ls", "cwd": cwd,
			"status": "completed", "exitCode": 0, "aggregatedOutput": "a.go\nb.go\n"}, true)
		f.item(turn, map[string]any{"id": "m1", "type": "agentMessage", "text": "two files"}, true)
		f.completeTurn(turn, "completed", "")

	case "tool":
		// Codex calls a tool this side declared on thread/start, and reports what it answered.
		res := f.request("item/tool/call", map[string]any{
			"threadId": "th1", "turnId": turn, "callId": "call1", "tool": "greet",
			"arguments": map[string]any{"name": "ana"}})
		f.record(map[string]any{"toolCall": res})
		var ans struct {
			Success      bool `json:"success"`
			ContentItems []struct {
				Text string `json:"text"`
			} `json:"contentItems"`
		}
		_ = json.Unmarshal(res, &ans)
		text := ""
		if len(ans.ContentItems) > 0 {
			text = ans.ContentItems[0].Text
		}
		f.item(turn, map[string]any{"id": "m1", "type": "agentMessage", "text": fmt.Sprintf("done: %v: %s", ans.Success, text)}, true)
		f.completeTurn(turn, "completed", "")

	case "hang":
		f.item(turn, map[string]any{"id": "c1", "type": "commandExecution", "command": "sleep 1000", "cwd": cwd, "status": "inProgress"}, false)
		res := f.request("item/commandExecution/requestApproval", map[string]any{
			"threadId": "th1", "turnId": turn, "itemId": "c1", "command": "sleep 1000", "cwd": cwd, "startedAtMs": 1})
		f.record(map[string]any{"approval": "c1", "decision": res})
		<-f.interrupted
		f.item(turn, map[string]any{"id": "c1", "type": "commandExecution", "command": "sleep 1000", "cwd": cwd, "status": "failed"}, true)
		f.completeTurn(turn, "interrupted", "")

	default: // basic
		f.item(turn, map[string]any{"id": "c1", "type": "commandExecution", "command": "go test ./...", "cwd": cwd, "status": "inProgress"}, false)
		res := f.request("item/commandExecution/requestApproval", map[string]any{
			"threadId": "th1", "turnId": turn, "itemId": "c1", "command": "go test ./...", "cwd": cwd, "startedAtMs": 1})
		f.record(map[string]any{"approval": "c1", "decision": res})
		c1 := map[string]any{"id": "c1", "type": "commandExecution", "command": "go test ./...", "cwd": cwd}
		if strings.Contains(string(res), "accept") {
			c1["status"], c1["exitCode"], c1["aggregatedOutput"] = "completed", 0, "ok\tverse\t0.1s\n"
		} else {
			c1["status"] = "declined"
		}
		f.item(turn, c1, true)

		changes := []map[string]any{{"path": filepath.Join(cwd, "notes.txt"), "kind": map[string]any{"type": "add"}, "diff": "+hi"}}
		f.item(turn, map[string]any{"id": "p1", "type": "fileChange", "changes": changes, "status": "inProgress"}, false)
		res = f.request("item/fileChange/requestApproval", map[string]any{
			"threadId": "th1", "turnId": turn, "itemId": "p1", "startedAtMs": 2})
		f.record(map[string]any{"approval": "p1", "decision": res})
		p1 := map[string]any{"id": "p1", "type": "fileChange", "changes": changes, "status": "completed"}
		if !strings.Contains(string(res), "accept") {
			p1["status"] = "declined"
		}
		f.item(turn, p1, true)

		res = f.request("item/tool/requestUserInput", map[string]any{
			"threadId": "th1", "turnId": turn, "itemId": "q1", "isBlocking": true,
			"questions": []map[string]any{{"id": "pick", "header": "Colour", "question": "which colour?",
				"options": []map[string]any{{"label": "red", "description": "warm"}, {"label": "blue", "description": "cool"}}}},
		})
		f.record(map[string]any{"answers": res})
		var ans struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		}
		_ = json.Unmarshal(res, &ans)
		picked := ""
		if a, ok := ans.Answers["pick"]; ok && len(a.Answers) > 0 {
			picked = a.Answers[0]
		}
		f.item(turn, map[string]any{"id": "m1", "type": "agentMessage", "text": "done: " + picked}, true)
		f.completeTurn(turn, "completed", "")
	}
}

// fakeCreds points the driver at the stand-in running scenario, and returns where its log
// lands.
func fakeCreds(t *testing.T, scenario string) (Creds, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "codex.log")
	t.Setenv(fakeEnvVar, scenario)
	t.Setenv(fakeLogVar, logPath)
	return Creds{Bin: exe, Config: map[string]string{"model_reasoning_effort": "low"}}, logPath
}

// fakeEnv is the environment the stand-in needs to find its scenario, built as the runner builds
// a task's: the named variables, passed through.
func fakeEnv() []string { return task.ShellEnv([]string{"PATH", "HOME", fakeEnvVar, fakeLogVar}) }

// readLog is what the stand-in recorded, so far.
func readLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// logHas reports whether some entry of the log, rendered, contains every one of the needles.
func logHas(entries []map[string]any, needles ...string) bool {
	for _, e := range entries {
		b, _ := json.Marshal(e)
		s := string(b)
		ok := true
		for _, n := range needles {
			if !strings.Contains(s, n) {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func waitLog(t *testing.T, path string, needles ...string) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if es := readLog(t, path); logHas(es, needles...) {
			return es
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the stand-in never recorded %q; it has:\n%s", needles, render(readLog(t, path)))
	return nil
}

func render(entries []map[string]any) string {
	var b strings.Builder
	for _, e := range entries {
		j, _ := json.Marshal(e)
		b.Write(j)
		b.WriteByte('\n')
	}
	return b.String()
}

// pendingOn waits for a question to be on the docket and returns it.
func pendingOn(t *testing.T, reg *task.Registry) task.Pending {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ps := reg.Pending(); len(ps) > 0 {
			return ps[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no question reached the docket")
	return task.Pending{}
}

// collect gathers a task's frames until stop is called.
func collect(tk *task.Task) (get func() []task.Event, stop func()) {
	ch, unsub := tk.Subscribe()
	var mu sync.Mutex
	var frames []task.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for f := range ch {
			mu.Lock()
			frames = append(frames, f)
			mu.Unlock()
		}
	}()
	return func() []task.Event {
			mu.Lock()
			defer mu.Unlock()
			return append([]task.Event{}, frames...)
		}, func() {
			unsub()
			<-done
		}
}

func frameWhere(frames []task.Event, pred func(task.Event) bool) (task.Event, bool) {
	for _, f := range frames {
		if pred(f) {
			return f, true
		}
	}
	return task.Event{}, false
}

// EVERY COMMAND AND PATCH CODEX PROPOSES IS A QUESTION ON THE DOCKET, under the default autonomy,
// and the answer is what codex hears: an approved command runs and its output is the action's;
// a refused patch is declined and the model is told why; a question the model asks a person is
// answered from the same docket.
func TestCodexActionsAreGatedOnTheDocket(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "basic")
	dir := t.TempDir()
	tk, err := Task(runner.Launch{ID: "run-codex-1", Prompt: "fix the flaky test", Agent: &runner.Agent{Name: Agent}, Environment: workEnv, Autonomy: namedGates},
		reg, creds, fakeEnv(), dir, nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	frames, stopFrames := collect(tk)
	defer stopFrames()
	done := make(chan task.Outcome, 1)
	go func() {
		oc, _ := tk.Run(context.Background())
		done <- oc
	}()

	// 1. The command: ASK under the default autonomy, on the docket as `shell`.
	p := pendingOn(t, reg)
	if p.Tool != kindShell || p.Question.Gate != "shell" {
		t.Fatalf("the first question is %s via gate %q, want %s via gate shell", p.Tool, p.Question.Gate, kindShell)
	}
	if !logHas(readLog(t, logPath), "thread/start", "untrusted") {
		t.Error("the thread was not started with approvalPolicy untrusted, so codex would run commands without asking")
	}
	if !reg.Answer(p.Question.ID, task.Resolution{Choice: task.Proceed, Proceed: true, By: "user:ana"}) {
		t.Fatal("the answer was not delivered")
	}
	waitLog(t, logPath, `"approval":"c1"`, "accept")

	// 2. The patch: refused. Codex hears decline, and then why, through the running turn.
	p = pendingOn(t, reg)
	if p.Tool != kindPatch || p.Question.Gate != "write" {
		t.Fatalf("the second question is %s via gate %q, want %s via gate write", p.Tool, p.Question.Gate, kindPatch)
	}
	if st := p.Question.Step; st.Name != task.StepFilesWrite || st.Fields["path"] != "notes.txt" {
		t.Errorf("the patch is asked about as %s, want a write of notes.txt (relative to the workspace)", st)
	}
	reg.Answer(p.Question.ID, task.Resolution{Choice: task.Deny, By: "user:ana", Note: "no new files in this change"})
	waitLog(t, logPath, `"approval":"p1"`, "decline")
	waitLog(t, logPath, "turn/steer", "no new files in this change")

	// 3. The model's question for the person: on the docket with codex's options, answered by one.
	p = pendingOn(t, reg)
	if p.Tool != kindAsk {
		t.Fatalf("the third question is %s, want %s", p.Tool, kindAsk)
	}
	ids := task.OptionIDs(p.Question.Options)
	if strings.Join(ids, ",") != "red,blue" {
		t.Errorf("the question offers %v, want codex's options red,blue", ids)
	}
	if p.Question.Reason != "Colour: which colour?" {
		t.Errorf("the question reads %q", p.Question.Reason)
	}
	reg.Answer(p.Question.ID, task.Resolution{Choice: "blue", By: "user:ana"})

	select {
	case oc := <-done:
		if oc.Reason != string(task.Done) {
			t.Fatalf("the run ended %s, want completed", oc.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the run did not end")
	}
	if got := tk.Result(); got != "done: blue" {
		t.Errorf("the result is %q, want the agent's last message with the person's answer in it", got)
	}
	entries := readLog(t, logPath)
	if !logHas(entries, `"pick"`, "blue") {
		t.Errorf("codex did not receive the answer blue:\n%s", render(entries))
	}

	// The read plane: the approved command's output rides the loop's own exec frame, the refused
	// patch is declined and the agent's words are the task's narration.
	stopFrames()
	fs := frames()
	if _, ok := frameWhere(fs, func(f task.Event) bool {
		return f.Kind == task.EventExec && f.Tool == kindShell && strings.Contains(f.Text, "ok")
	}); !ok {
		t.Errorf("no exec frame carries the command's output; frames:\n%s", renderFrames(fs))
	}
	if _, ok := frameWhere(fs, func(f task.Event) bool {
		return f.Kind == task.EventDecision && f.Tool == kindPatch && !f.Proceed
	}); !ok {
		t.Errorf("no decision frame records the refused patch; frames:\n%s", renderFrames(fs))
	}
	if _, ok := frameWhere(fs, func(f task.Event) bool {
		return f.Kind == task.EventSay && f.By == "" && f.Text == "done: blue"
	}); !ok {
		t.Errorf("the agent's last message is not on the read plane; frames:\n%s", renderFrames(fs))
	}
}

// allowAll gives codex its shell, its edits and its questions, and asks about none of them.
var allowAll = task.Autonomy{
	{Match: &task.Match{System: "commands"}, Then: task.Then{Mode: task.ThenAllow}},
	{Match: &task.Match{System: "files"}, Then: task.Then{Mode: task.ThenAllow}},
	{Match: &task.Match{System: "question"}, Then: task.Then{Mode: task.ThenAllow}},
	{Then: task.Then{Mode: task.ThenAllow}},
}

// namedGates is the policy the tests that look at a question's row run under: commands as
// `shell`, writes as `write`.
var namedGates = task.Autonomy{
	{Name: "shell", Match: &task.Match{System: "commands", Verb: "run"}, Then: task.Then{Mode: task.ThenAsk}},
	{Name: "write", Match: &task.Match{System: "files", Verb: "write"}, Then: task.Then{Mode: task.ThenAsk}},
	{Match: &task.Match{System: "question"}, Then: task.Then{Mode: task.ThenAllow}},
	{Then: task.Then{Mode: task.ThenAsk}},
}

func renderFrames(fs []task.Event) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "%s tool=%s gate=%s proceed=%v by=%s text=%q err=%q\n", f.Kind, f.Tool, f.Gate, f.Proceed, f.By, f.Text, f.Err)
	}
	return b.String()
}

// UNDER `[{then: allow}]` NOTHING IS ASKED, and everything is still on the record: the commands
// codex ran without asking appear when they complete, and the run's result is the agent's last
// message.
func TestCodexNarratesWhatItDidNotAsk(t *testing.T) {
	reg := task.NewRegistry()
	creds, _ := fakeCreds(t, "trusted")
	tk, err := Task(runner.Launch{ID: "run-codex-2", Prompt: "look around", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll}, reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	frames, stopFrames := collect(tk)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	oc, err := tk.Run(ctx)
	if err != nil || oc.Reason != string(task.Done) {
		t.Fatalf("run: %v %s", err, oc.Reason)
	}
	stopFrames()
	fs := frames()
	if _, ok := frameWhere(fs, func(f task.Event) bool {
		return f.Kind == task.EventExec && f.Tool == kindShell && strings.HasPrefix(f.Text, "ls → exit 0")
	}); !ok {
		t.Errorf("the trusted command is not on the read plane; frames:\n%s", renderFrames(fs))
	}
	if tk.Result() != "two files" {
		t.Errorf("result %q", tk.Result())
	}
}

// A STEER MID-TURN REACHES THE RUNNING TURN, as `turn/steer`, at the next boundary — which for
// this driver is the next thing codex asks about.
func TestCodexSteerReachesTheRunningTurn(t *testing.T) {
	creds, logPath := fakeCreds(t, "basic")
	d := &driver{creds: creds, prompt: "work", dir: t.TempDir(),
		env: fakeEnv(), resolver: task.AutoResolver{Who: "test"}}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ex := &executor{d: d}

	tu, more, err := d.Next(ctx, task.StepResult{})
	if err != nil || !more || tu.Name != kindShell {
		t.Fatalf("first proposal: %v %v %s", err, more, tu.Name)
	}
	// Words arrive while the command is in front of the gate...
	d.Fold([]string{"skip the generated files"})
	tu.ID = "run-codex-3~a1"
	if _, _, err := ex.Execute(ctx, tu); err != nil {
		t.Fatal(err)
	}
	// ...and are taken at the next boundary, into the turn that is still running.
	tu, more, err = d.Next(ctx, task.StepResult{Use: tu})
	if err != nil || !more || tu.Name != kindPatch {
		t.Fatalf("second proposal: %v %v %s", err, more, tu.Name)
	}
	entries := readLog(t, logPath)
	if !logHas(entries, "turn/steer", "skip the generated files", `"expectedTurnId":"tu1"`) {
		t.Fatalf("the steer did not reach the running turn:\n%s", render(entries))
	}
	// A refusal from the loop is a decline to codex, with the reason said into the turn.
	tu, more, err = d.Next(ctx, task.StepResult{Use: tu, Blocked: true, Note: "docs only"})
	if err != nil || !more || tu.Name != kindAsk {
		t.Fatalf("third proposal: %v %v %s", err, more, tu.Name)
	}
	waitLog(t, logPath, `"approval":"p1"`, "decline")
	waitLog(t, logPath, "turn/steer", "docs only")
	tu.ID = "run-codex-3~a3"
	if _, _, err := ex.Execute(ctx, tu); err != nil {
		t.Fatal(err)
	}
	if _, more, err = d.Next(ctx, task.StepResult{Use: tu}); err != nil || more {
		t.Fatalf("the turn did not end: %v %v", err, more)
	}
	// Between turns, words start the next turn.
	d.Fold([]string{"and now the tests"})
	if _, more, err = d.Next(ctx, task.StepResult{}); err != nil || more {
		t.Fatalf("the second turn: %v %v", err, more)
	}
	if d.Result() != "again: and now the tests" {
		t.Errorf("result %q", d.Result())
	}
}

// A CONVERSATION WITH CODEX: when its turn ends the person is handed the turn, their words are
// Codex's next turn, and the run ends when they end it.
func TestCodexHoldsAConversation(t *testing.T) {
	reg := task.NewRegistry()
	creds, _ := fakeCreds(t, "trusted")
	tk, err := Task(runner.Launch{ID: "run-codex-30", Prompt: "look around", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll, Converse: true}, reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	tk.SetConverse(reg)
	done := make(chan task.Outcome, 1)
	go func() {
		oc, _ := tk.Run(context.Background())
		done <- oc
	}()

	turn := pendingOn(t, reg)
	if turn.Question.Gate != "turn" || tk.Result() != "two files" {
		t.Fatalf("after the first turn: %+v, result %q", turn.Question, tk.Result())
	}
	reg.Answer(turn.Question.ID, task.Resolution{Choice: task.Reply, By: "user:ana", Steer: []string{"and the tests?"}})
	deadline := time.Now().Add(10 * time.Second)
	var second task.Pending
	for time.Now().Before(deadline) && second.Question.ID == "" {
		for _, p := range reg.Pending() {
			if p.Question.Gate == "turn" && p.Question.ID != turn.Question.ID {
				second = p
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if second.Question.ID == "" {
		t.Fatal("the person was not handed the turn again")
	}
	if got := tk.Result(); got != "again: and the tests?" {
		t.Errorf("result %q, want Codex's answer to the person's words", got)
	}
	reg.Answer(second.Question.ID, task.Resolution{Choice: task.End, By: "user:ana"})
	select {
	case oc := <-done:
		if oc.Reason != string(task.Done) {
			t.Fatalf("the run ended %s", oc.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the run did not end when the person ended it")
	}
}

// CANCEL INTERRUPTS THE TURN: a command codex is still running when the run is cancelled is not
// left running.
func TestCodexCancelInterruptsTheTurn(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "hang")
	tk, err := Task(runner.Launch{ID: "run-codex-4", Prompt: "wait", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll}, reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan task.Outcome, 1)
	go func() {
		oc, _ := tk.Run(ctx)
		done <- oc
	}()
	waitLog(t, logPath, `"approval":"c1"`, "accept")
	cancel()
	select {
	case oc := <-done:
		if oc.Reason == string(task.Done) {
			t.Errorf("a cancelled run completed")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the run did not end after cancel")
	}
	waitLog(t, logPath, "turn/interrupt", `"turnId":"tu1"`)
}

// A TURN THAT FAILS FAILS THE RUN, with codex's reason.
func TestCodexTurnFailureFailsTheRun(t *testing.T) {
	reg := task.NewRegistry()
	creds, _ := fakeCreds(t, "fail")
	tk, err := Task(runner.Launch{ID: "run-codex-5", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll}, reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	oc, err := tk.Run(ctx)
	if oc.Reason != string(task.Failed) || err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("run ended %s with %v, want failed with codex's reason", oc.Reason, err)
	}
}

// THE KEY REACHES CODEX AS A LOGIN FILE in a private CODEX_HOME, and in no environment: codex
// does not read a key from its environment, and a variable is what every subprocess inherits.
// The home dies with the run.
func TestCodexKeyIsHandedAsALogin(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "trusted")
	creds.Key = "sk-test-not-a-real-key"
	tk, err := Task(runner.Launch{ID: "run-codex-7", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll}, reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if oc, err := tk.Run(ctx); err != nil || oc.Reason != string(task.Done) {
		t.Fatalf("run: %v %s", err, oc.Reason)
	}
	entries := readLog(t, logPath)
	var auth map[string]any
	for _, e := range entries {
		if a, ok := e["auth"].(map[string]any); ok {
			auth = a
		}
	}
	if auth == nil {
		t.Fatalf("the stand-in recorded no auth:\n%s", render(entries))
	}
	home, _ := auth["codexHome"].(string)
	if home == "" || auth["authMode"] != "apikey" || auth["key"] != creds.Key {
		t.Errorf("codex was authenticated as %v, want an apikey login file in a private CODEX_HOME", auth)
	}
	if auth["keyInEnv"] == true {
		t.Error("the key was in codex's environment, where every command it runs inherits it")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("the private CODEX_HOME %s outlived the run (%v)", home, err)
	}
}

// A CODEX THAT IS NOT THERE fails the run with a message that says so.
func TestCodexMissingBinaryFailsTheRun(t *testing.T) {
	reg := task.NewRegistry()
	tk, err := Task(runner.Launch{ID: "run-codex-6", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv, Autonomy: namedGates},
		reg, Creds{Bin: filepath.Join(t.TempDir(), "no-such-codex")}, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	oc, err := tk.Run(context.Background())
	if oc.Reason != string(task.Failed) || err == nil || !strings.Contains(err.Error(), "no-such-codex") {
		t.Fatalf("run ended %s with %v", oc.Reason, err)
	}
}

// THE STEPS ARE DERIVED FROM WHAT CODEX SAID, not from a tool name: a command is a command run,
// each change of a patch is a write of its path (a move, of both), and a permissions request is
// what it asks for. The network is no step: the task's is built around codex's process.
func TestCodexSteps(t *testing.T) {
	d := &driver{dir: "/w", items: map[string]item{}}
	ev := func(method string, params any) event {
		b, _ := json.Marshal(params)
		return event{ID: json.RawMessage(`1`), Method: method, Params: b}
	}
	steps := func(p *proposal) string {
		var parts []string
		for _, st := range p.steps {
			parts = append(parts, st.String())
		}
		return strings.Join(parts, "; ")
	}

	p, tu, err := d.propose(ev("item/commandExecution/requestApproval", map[string]any{
		"itemId": "c1", "turnId": "t", "command": "curl https://x.test", "cwd": "/w/svc",
		"networkApprovalContext": map[string]any{"host": "x.test"}}))
	if err != nil {
		t.Fatal(err)
	}
	// The host it asks to reach is no step: the task's network is built around codex.
	if got := steps(p); got != "commands.run command=curl https://x.test" {
		t.Errorf("command: %s", got)
	}
	if tu.Input["cwd"] != "svc" {
		t.Errorf("cwd %v, want relative to the workspace", tu.Input["cwd"])
	}

	// The patch's changes come from the item codex started, by id.
	var started itemEvent
	b, _ := json.Marshal(map[string]any{"item": map[string]any{"id": "p1", "type": "fileChange", "changes": []map[string]any{
		{"path": "/w/a.go", "kind": map[string]any{"type": "update"}},
		{"path": "/w/b.go", "kind": map[string]any{"type": "delete"}},
		{"path": "/w/c.go", "kind": map[string]any{"type": "add"}},
		{"path": "/w/d.go", "kind": map[string]any{"type": "update", "move_path": "/w/e.go"}},
		{"path": "/elsewhere/f.go", "kind": map[string]any{"type": "add"}},
	}}})
	_ = json.Unmarshal(b, &started)
	d.items["p1"] = started.Item
	p, tu, err = d.propose(ev("item/fileChange/requestApproval", map[string]any{"itemId": "p1", "turnId": "t", "reason": "needs it"}))
	if err != nil {
		t.Fatal(err)
	}
	want := "files.write path=a.go; files.write path=b.go; files.write path=c.go; files.write path=d.go; files.write path=e.go; files.write path=/elsewhere/f.go"
	if got := steps(p); got != want {
		t.Errorf("patch:\n got %s\nwant %s", got, want)
	}
	// And the docket can say what the patch touches: the action describes itself.
	if got := tu.Describe(); !strings.Contains(got, "update a.go") || !strings.Contains(got, "add c.go") {
		t.Errorf("the patch describes itself as %q, want its changes", got)
	}

	// A patch whose item was never seen is still a write.
	p, _, _ = d.propose(ev("item/fileChange/requestApproval", map[string]any{"itemId": "p9", "turnId": "t"}))
	if got := steps(p); got != "files.write path=" {
		t.Errorf("unknown patch: %s", got)
	}

	// Permissions: the paths asked for; the network is the task's, whatever codex is told.
	p, _, err = d.propose(ev("item/permissions/requestApproval", map[string]any{
		"itemId": "x1", "turnId": "t", "cwd": "/w", "permissions": map[string]any{
			"network":    map[string]any{"enabled": true},
			"fileSystem": map[string]any{"entries": []map[string]any{{"access": "write", "path": map[string]any{"type": "path", "path": "/w/out"}}}},
		}}))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(p); got != "files.write path=out" {
		t.Errorf("permissions: %s", got)
	}
	// The network alone asks nothing, as a read alone does not.
	p, _, err = d.propose(ev("item/permissions/requestApproval", map[string]any{
		"itemId": "x2", "turnId": "t", "cwd": "/w", "permissions": map[string]any{
			"network": map[string]any{"enabled": true},
		}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.steps) != 0 {
		t.Errorf("a request for the network alone derives %s, want no step", steps(p))
	}
	// Beside a kind this side does not read, it is a widening: the answer grants the whole.
	p, _, err = d.propose(ev("item/permissions/requestApproval", map[string]any{
		"itemId": "x3", "turnId": "t", "cwd": "/w", "permissions": map[string]any{
			"network": map[string]any{"enabled": true}, "macos": map[string]any{"accessibility": true},
		}}))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(p); got != "files.write path=" {
		t.Errorf("a request for the network and a kind unread derives %q, want it gated as a write", got)
	}
	// An access to a path this side does not read is gated as a write too.
	p, _, err = d.propose(ev("item/permissions/requestApproval", map[string]any{
		"itemId": "x4", "turnId": "t", "cwd": "/w", "permissions": map[string]any{
			"fileSystem": map[string]any{"entries": []any{map[string]any{"access": "readWrite", "path": map[string]any{"type": "path", "path": "/w/db"}}}},
		}}))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(p); got != "files.write path=db" {
		t.Errorf("a request for an access unread derives %q, want it gated as a write of db", got)
	}

	// A question is the step question.ask, as the question-ask tool's use is.
	p, _, _ = d.propose(ev("item/tool/requestUserInput", map[string]any{"itemId": "q", "turnId": "t", "isBlocking": true,
		"questions": []map[string]any{{"id": "a", "header": "h", "question": "why?"}}}))
	if got := steps(p); got != task.StepQuestionAsk {
		t.Errorf("a user-input request derives %q, want %s", got, task.StepQuestionAsk)
	}

	// What this driver does not know is refused, not guessed.
	if _, _, err := d.propose(ev("item/somethingNew/requestApproval", map[string]any{})); err == nil {
		t.Error("an unknown request was accepted as an action")
	}
}

// A REQUEST THAT IS SEVERAL STEPS GOES AHEAD ONLY WHERE EACH WOULD: a patch writing a.go and
// db/b.sql is asked about by the gate on `db/**`, as b.sql, refused by a deny on it below an
// allow that covers a.go, and allowed unasked where every write is.
func TestARequestOfSeveralStepsGoesAheadOnlyWhereEachWould(t *testing.T) {
	d := &driver{}
	d.proposed.Store(&proposal{steps: []task.Step{
		{Name: task.StepFilesWrite, Fields: map[string]string{"path": "a.go"}},
		{Name: task.StepFilesWrite, Fields: map[string]string{"path": "db/b.sql"}},
	}})
	var asked []string
	sup := systemstest.Opened(task.NewSupervisor(workEnv, task.Autonomy{
		{Name: "schema", Match: &task.Match{System: "files", Verb: "write", Params: map[string]string{"path": "db/**"}}, Then: task.Then{Mode: task.ThenAsk}},
		{Then: task.Then{Mode: task.ThenAllow}},
	}, resolveWith(func(q task.GateQuestion) { asked = append(asked, q.Gate+" "+q.Step.Fields["path"]) })), workEnv)
	sup.SetStepsOf(d.stepsOf)
	if res, err := sup.Check(context.Background(), task.ToolUse{}); err != nil || !res.Proceed || strings.Join(asked, ", ") != "schema db/b.sql" {
		t.Fatalf("asked %v (%+v, %v), want the write the gate names", asked, res, err)
	}
	asked = nil
	sup.SetAutonomy(task.Autonomy{
		{Name: "go", Match: &task.Match{System: "files", Verb: "write", Params: map[string]string{"path": "*.go"}}, Then: task.Then{Mode: task.ThenAllow}},
		{Name: "rest", Match: &task.Match{System: "files", Verb: "write"}, Then: task.Then{Mode: task.ThenDeny}},
	})
	if res, _ := sup.Check(context.Background(), task.ToolUse{}); res.Proceed || res.Question.Gate != "rest" || len(asked) != 0 {
		t.Fatalf("allow *.go, deny the rest: %+v (asked %v), want refused by rest", res, asked)
	}
	sup.SetAutonomy(allowAll)
	if res, _ := sup.Check(context.Background(), task.ToolUse{}); !res.Proceed || len(asked) != 0 {
		t.Fatalf("with no gate: %+v (asked %v), want allowed unasked", res, asked)
	}
	d.proposed.Store(&proposal{})
	if res, _ := sup.Check(context.Background(), task.ToolUse{}); !res.Proceed || res.Question.Reason != "not a step" {
		t.Fatalf("a request that is no step: %+v, want no step", res)
	}
}

// resolveWith answers every question yes, telling f what it was.
type resolveWith func(task.GateQuestion)

func (f resolveWith) Resolve(_ context.Context, q task.GateQuestion, _ task.ToolUse) (task.Resolution, error) {
	f(q)
	return task.Resolution{Choice: task.Proceed, Proceed: true, Question: q}, nil
}

// THE CONFIG SAYS HOW CODEX RUNS: the sandbox is checked where it is written, the key is read
// from OPENAI_API_KEY and never kept in the section, and the overrides reach the command line in
// a fixed order.
func TestConfigIsChecked(t *testing.T) {
	if err := (&Config{Sandbox: "everything"}).Validate(); err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("a bad sandbox validated: %v", err)
	}
	c := &Config{Bin: "/opt/codex", Config: map[string]string{"b": "2", "a": "1"}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvAPIKey, "secret")
	creds := c.Creds(Key())
	if creds.Key != "secret" || creds.Sandbox != defaultSandbox {
		t.Errorf("resolved %+v", creds)
	}
	if got := strings.Join(creds.argv(), " "); got != "/opt/codex app-server -c a=1 -c b=2" {
		t.Errorf("argv %q", got)
	}
	if why := MissingKey(os.Getenv); why != "" {
		t.Errorf("a set key reported missing: %s", why)
	}
	if why := MissingKey(func(string) string { return "" }); !strings.Contains(why, EnvAPIKey) {
		t.Errorf("an unset key not reported: %q", why)
	}
}

// greetTool is a runner tool for the tests: a command run, so a gate on commands can stop it, and
// it says hello.
func greetTool() verse.Tool {
	return verse.Tool{
		Name: "greet", Description: "say hello to somebody",
		Params:   map[string]any{"name": map[string]any{"type": "string"}},
		Required: []string{"name"},
		Step:     task.StepCommandsRun,
		Fields: func(map[string]any) (map[string]string, bool) {
			return map[string]string{"command": "greet"}, true
		},
		Do: func(_ context.Context, tu task.ToolUse) (string, bool, error) {
			name, _ := tu.Input["name"].(string)
			return "hello, " + name, false, nil
		},
	}
}

// THE RUNNER'S TOOLS ARE CODEX'S TOOLS: declared to it when the thread starts, and a call of one
// is an action of the loop — the tool's own step, decided by the gates and autonomy, performed by
// the tool in this process, its result the call's output.
func TestCodexIsOfferedTheRunnersTools(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "tool")
	tk, err := Task(runner.Launch{ID: "run-codex-8", Prompt: "greet", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll}, reg, creds, fakeEnv(), t.TempDir(), []verse.Tool{greetTool()}, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	frames, stopFrames := collect(tk)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if oc, err := tk.Run(ctx); err != nil || oc.Reason != string(task.Done) {
		t.Fatalf("run: %v %s", err, oc.Reason)
	}
	entries := readLog(t, logPath)
	if !logHas(entries, "thread/start", "dynamicTools", `"name":"greet"`, "say hello") {
		t.Errorf("the tool was not declared to codex on thread/start:\n%s", render(entries))
	}
	if !logHas(entries, "toolCall", `"success":true`, "hello, ana") {
		t.Errorf("codex did not get the tool's result:\n%s", render(entries))
	}
	if got := tk.Result(); got != "done: true: hello, ana" {
		t.Errorf("result %q", got)
	}
	stopFrames()
	fs := frames()
	if _, ok := frameWhere(fs, func(f task.Event) bool {
		return f.Kind == task.EventExec && f.Tool == "greet" && f.Text == "hello, ana"
	}); !ok {
		t.Errorf("the call is not on the read plane as the action it was; frames:\n%s", renderFrames(fs))
	}
	if _, ok := frameWhere(fs, func(f task.Event) bool {
		return f.Kind == task.EventDecision && f.Tool == "greet" && f.Proceed
	}); !ok {
		t.Errorf("the call was not gated; frames:\n%s", renderFrames(fs))
	}
}

// A CALL THE GATE REFUSES IS REFUSED TO CODEX AS THE CALL'S RESULT, with the reason, so the model
// reads it where it asked and plans around it.
func TestARefusedToolCallIsCodexsToRead(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "tool")
	tk, err := Task(runner.Launch{ID: "run-codex-9", Prompt: "greet", Agent: &runner.Agent{Name: Agent}, Environment: workEnv, Autonomy: namedGates},
		reg, creds, fakeEnv(), t.TempDir(), []verse.Tool{greetTool()}, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan task.Outcome, 1)
	go func() {
		oc, _ := tk.Run(context.Background())
		done <- oc
	}()
	p := pendingOn(t, reg)
	if p.Tool != "greet" || p.Question.Gate != "shell" {
		t.Fatalf("the question is %s via %q, want greet via the shell gate (it is a command run)", p.Tool, p.Question.Gate)
	}
	reg.Answer(p.Question.ID, task.Resolution{Choice: task.Deny, By: "user:ana", Note: "no greetings today"})
	select {
	case oc := <-done:
		if oc.Reason != string(task.Done) {
			t.Fatalf("run ended %s", oc.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the run did not end")
	}
	entries := readLog(t, logPath)
	if !logHas(entries, "toolCall", `"success":false`, "no greetings today") {
		t.Errorf("codex was not told the refusal and why:\n%s", render(entries))
	}
	if !strings.Contains(tk.Result(), "false") || !strings.Contains(tk.Result(), "refused") {
		t.Errorf("result %q", tk.Result())
	}
}

// THE SPAWN'S MODEL AND EFFORT REACH CODEX: `with.model` is the thread's model and rides every
// turn, and `with.effort` is the reasoning effort of its turns. Codex's
// protocol takes both on `turn/start`, for that turn and the ones after it.
func TestCodexTakesTheSpawnsModelAndEffort(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "trusted")
	tk, err := Task(runner.Launch{ID: "run-codex-10", Prompt: "x", Agent: &runner.Agent{Name: Agent, Settings: map[string]string{"model": "gpt-5.3-codex", "effort": "high"}}, Environment: workEnv,
		Autonomy: allowAll}, reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if oc, err := tk.Run(ctx); err != nil || oc.Reason != string(task.Done) {
		t.Fatalf("run: %v %s", err, oc.Reason)
	}
	entries := readLog(t, logPath)
	if !logHas(entries, "thread/start", `"model":"gpt-5.3-codex"`) {
		t.Errorf("the spawn's model did not reach thread/start:\n%s", render(entries))
	}
	if !logHas(entries, "turn/start", `"model":"gpt-5.3-codex"`, `"effort":"high"`) {
		t.Errorf("the spawn's model and effort did not reach turn/start:\n%s", render(entries))
	}
	// And the task says what it ran with, for the record.
	if _, set := tk.Agent(); set["model"] != "gpt-5.3-codex" || set["effort"] != "high" {
		t.Errorf("the task's settings: %v", set)
	}
	// A spawn naming no model reaches the driver naming none, and runs codex's own: the runner's
	// configured default is filled in before (package stock), so the record says it either way.
	tk2, err := Task(runner.Launch{ID: "run-codex-11", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll}, task.NewRegistry(), creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	if _, set := tk2.Agent(); set["model"] != "" || set["effort"] != "" {
		t.Errorf("the default's settings: %v", set)
	}
}

// THE CREDENTIAL FILE IS CODEX'S TOO: what a spawn's `credential` minted is handed to the codex
// process as it is to a shell, by the variable naming the file, so a command codex runs finds it.
func TestCodexIsHandedTheTasksCredential(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "trusted")
	tk, err := Task(runner.Launch{ID: "run-codex-11", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll}, reg, creds, append(fakeEnv(), task.CredentialFileEnv+"=/run/cred"), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if oc, err := tk.Run(ctx); err != nil || oc.Reason != string(task.Done) {
		t.Fatalf("run: %v %s", err, oc.Reason)
	}
	if !logHas(readLog(t, logPath), `"credentialFile":"/run/cred"`) {
		t.Errorf("the credential file's name did not reach the codex process:\n%s", render(readLog(t, logPath)))
	}
}

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnvVar) != "" {
		fakeServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A COMMAND'S OUTPUT IS CUT WHERE A RUNE STARTS: its last 4000 bytes or fewer, so a rune across
// the bound is left out whole rather than split.
func TestACommandsOutputIsCutWhereARuneStarts(t *testing.T) {
	out := "é" + strings.Repeat("b", 3999) // the last 4000 bytes start inside é
	if got := commandOutput(item{AggregatedOutput: out}); got != "…"+strings.Repeat("b", 3999) {
		t.Errorf("commandOutput: %q", got[:8])
	}
}
