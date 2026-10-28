package claude

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

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd/verse"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/systems/systemstest"
	"github.com/signadot/verse/task"
)

// workEnv is the environment claude runs in: its shell, its edits and its questions, which it
// cannot run without (the first two) or asks through (the third).
var workEnv = systems.Environment{Systems: systems.Systems{
	"commands": {{Verbs: map[string][]systems.Shape{"run": {{}}}}},
	"files":    {{Verbs: map[string][]systems.Shape{"write": {{}}}}},
	"question": {{Verbs: map[string][]systems.Shape{"ask": {{}}}}},
}}

// The claude agent is tested against a stand-in headless `claude`: this test binary,
// re-executed (TestMain) with fakeEnvVar naming a scenario. The stand-in speaks the same
// stream protocol over stdio the real one does — the message shapes were read from Claude Code
// 2.1.304 — and writes what it was told to a log the test reads back. So what is asserted is
// the conversation: what verse answered Claude, and what Claude's answers became on verse's
// side.

const (
	fakeEnvVar = "VERSE_RUNNER_FAKE_CLAUDE"
	fakeLogVar = "VERSE_RUNNER_FAKE_LOG"
)

// fakeServer is the stand-in. It handles the client's messages inline and runs each turn as a
// script, in a goroutine, that waits for the client's decisions.
func fakeServer() {
	f := &fake{out: os.Stdout, replies: map[string]chan json.RawMessage{}, interrupted: make(chan struct{})}
	if p := os.Getenv(fakeLogVar); p != "" {
		lf, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			f.log = lf
			defer lf.Close()
		}
	}
	scenario := os.Getenv(fakeEnvVar)
	// How this process was started and authenticated, for the tests that assert the shape.
	f.record(map[string]any{"argv": os.Args[1:]})
	f.record(map[string]any{"auth": map[string]any{
		"configDir":      os.Getenv("CLAUDE_CONFIG_DIR"),
		"apiKey":         os.Getenv("ANTHROPIC_API_KEY"),
		"credentialFile": os.Getenv("VERSE_CREDENTIAL_FILE"),
	}})
	go func() {
		// The MCP handshake, which the real one does as it starts, before any turn, where its
		// config declares a server.
		if slicesContains(os.Args, "--mcp-config") {
			f.mcp("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}})
			f.record(map[string]any{"notified": f.mcp("notifications/initialized", nil)})
			f.record(map[string]any{"toolsList": f.mcp("tools/list", map[string]any{})})
		}
		f.write(map[string]any{"type": "system", "subtype": "init", "session_id": "s1", "tools": []string{"Bash"}})
	}()
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var m struct {
			Type      string          `json:"type"`
			RequestID string          `json:"request_id"`
			Request   json.RawMessage `json:"request"`
			Response  json.RawMessage `json:"response"`
			Message   json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		switch m.Type {
		case "control_response":
			// The client's answer to a request the script made.
			var r struct {
				Subtype   string          `json:"subtype"`
				RequestID string          `json:"request_id"`
				Response  json.RawMessage `json:"response"`
				Error     string          `json:"error"`
			}
			if json.Unmarshal(m.Response, &r) == nil {
				if r.Subtype == "error" {
					r.Response, _ = json.Marshal(map[string]any{"error": r.Error})
				}
				f.mu.Lock()
				ch := f.replies[r.RequestID]
				delete(f.replies, r.RequestID)
				f.mu.Unlock()
				if ch != nil {
					ch <- r.Response
				}
			}
		case "control_request":
			var r struct {
				Subtype string `json:"subtype"`
			}
			_ = json.Unmarshal(m.Request, &r)
			f.record(map[string]any{"control": r.Subtype})
			f.write(map[string]any{"type": "control_response", "response": map[string]any{
				"subtype": "success", "request_id": m.RequestID, "response": map[string]any{}}})
			if r.Subtype == "interrupt" {
				f.once.Do(func() { close(f.interrupted) })
			}
		case "user":
			var msg struct {
				Content string `json:"content"`
			}
			_ = json.Unmarshal(m.Message, &msg)
			// Taken up at once, into the running turn or as a turn of its own, and echoed
			// back as taken (--replay-user-messages); under the lock a result is written
			// under, so a message is never echoed into a turn that has already ended.
			f.mu.Lock()
			mid := f.inTurn
			if !mid {
				f.inTurn = true
				f.turns++
			}
			turns := f.turns
			f.write(map[string]any{"type": "user", "isReplay": true, "message": map[string]any{"role": "user", "content": msg.Content}})
			f.mu.Unlock()
			f.record(map[string]any{"user": msg.Content, "midTurn": mid})
			if !mid {
				go f.turn(scenario, turns, msg.Content)
			}
		}
	}
	f.bg.Wait()
}

func slicesContains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

type fake struct {
	out io.Writer
	log *os.File
	wmu sync.Mutex

	mu      sync.Mutex
	seq     int64
	replies map[string]chan json.RawMessage

	turns       int
	inTurn      bool
	once        sync.Once
	interrupted chan struct{}
	bg          sync.WaitGroup // what the stand-in records after the turn has moved on
}

func (f *fake) write(msg any) {
	b, _ := json.Marshal(msg)
	f.wmu.Lock()
	defer f.wmu.Unlock()
	fmt.Fprintf(f.out, "%s\n", b)
}

// request asks the client and waits for its answer.
func (f *fake) request(req map[string]any) json.RawMessage {
	f.mu.Lock()
	f.seq++
	id := fmt.Sprintf("req%d", f.seq)
	ch := make(chan json.RawMessage, 1)
	f.replies[id] = ch
	f.mu.Unlock()
	f.write(map[string]any{"type": "control_request", "request_id": id, "request": req})
	return <-ch
}

// requestAsync asks the client and returns where its answer will land.
func (f *fake) requestAsync(req map[string]any) <-chan json.RawMessage {
	f.mu.Lock()
	f.seq++
	id := fmt.Sprintf("req%d", f.seq)
	ch := make(chan json.RawMessage, 1)
	f.replies[id] = ch
	f.mu.Unlock()
	f.write(map[string]any{"type": "control_request", "request_id": id, "request": req})
	return ch
}

// mcp sends one JSON-RPC message of the MCP server's conversation and returns the answer.
func (f *fake) mcp(method string, params any) json.RawMessage {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	if !strings.HasPrefix(method, "notifications/") {
		f.mu.Lock()
		f.seq++
		msg["id"] = f.seq
		f.mu.Unlock()
	}
	return f.request(map[string]any{"subtype": "mcp_message", "server_name": "verse", "message": msg})
}

// decision is the client's answer to a permission request.
type decision struct {
	Behavior     string         `json:"behavior"`
	Message      string         `json:"message"`
	UpdatedInput map[string]any `json:"updatedInput"`
}

// ask announces a tool use and asks permission for it, as the real one does for a call its
// permission mode does not allow.
func (f *fake) ask(tool, id string, input map[string]any) decision {
	f.use(tool, id, input)
	raw := f.request(map[string]any{"subtype": "can_use_tool", "tool_name": tool, "input": input, "tool_use_id": id})
	f.record(map[string]any{"ask": tool, "id": id, "decision": raw})
	var d decision
	_ = json.Unmarshal(raw, &d)
	return d
}

// use announces a tool use: an assistant message with a tool_use block.
func (f *fake) use(tool, id string, input map[string]any) {
	f.write(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
		"content": []map[string]any{{"type": "tool_use", "id": id, "name": tool, "input": input}}}})
}

// toolResult is a call's result: a user message with a tool_result block.
func (f *fake) toolResult(id, text string, isError bool) {
	f.write(map[string]any{"type": "user", "message": map[string]any{"role": "user",
		"content": []map[string]any{{"type": "tool_result", "tool_use_id": id, "content": text, "is_error": isError}}}})
}

func (f *fake) text(s string) {
	f.write(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
		"content": []map[string]any{{"type": "text", "text": s}}}})
}

func (f *fake) result(subtype string, isError bool, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inTurn = false
	f.write(map[string]any{"type": "result", "subtype": subtype, "is_error": isError, "result": text, "stop_reason": "end_turn"})
}

// requestRaw writes a request line as given and waits for the answer to id.
func (f *fake) requestRaw(id string, line map[string]any) json.RawMessage {
	ch := make(chan json.RawMessage, 1)
	f.mu.Lock()
	f.replies[id] = ch
	f.mu.Unlock()
	f.write(line)
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

// turn is one scripted turn.
func (f *fake) turn(scenario string, turns int, text string) {
	cwd, _ := os.Getwd()
	if turns > 1 {
		// A later turn: a steer, a repair or a reply. Echo it and end.
		f.text("again: " + text)
		f.result("success", false, "again: "+text)
		return
	}
	switch scenario {
	case "fail":
		f.result("success", true, "Not logged in · Please run /login")

	case "trusted":
		// A read: Claude's permission mode allows it, so it runs without asking, and is reported
		// when done.
		f.use("Read", "r1", map[string]any{"file_path": filepath.Join(cwd, "a.go")})
		f.toolResult("r1", "1\tpackage a\n", false)
		f.text("two files")
		f.result("success", false, "two files")

	case "tool":
		// Claude calls a tool of the server this side declared: the permission request first,
		// then the call itself over the server's channel.
		d := f.ask("mcp__verse__greet", "t1", map[string]any{"name": "ana"})
		if d.Behavior != "allow" {
			f.toolResult("t1", d.Message, true)
			f.text("done: false: " + d.Message)
			f.result("success", false, "")
			return
		}
		// The call's arguments differ from the request's: the gated input is what runs.
		res := f.mcp("tools/call", map[string]any{"name": "greet", "arguments": map[string]any{"name": "mallory"},
			"_meta": map[string]any{"claudecode/toolUseId": "t1"}})
		f.record(map[string]any{"toolCall": res})
		var ans struct {
			Response struct {
				Result struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
					IsError bool `json:"isError"`
				} `json:"result"`
			} `json:"mcp_response"`
		}
		_ = json.Unmarshal(res, &ans)
		out := ""
		if len(ans.Response.Result.Content) > 0 {
			out = ans.Response.Result.Content[0].Text
		}
		f.toolResult("t1", out, ans.Response.Result.IsError)
		f.text(fmt.Sprintf("done: %v: %s", !ans.Response.Result.IsError, out))
		f.result("success", false, "")

	case "pair":
		// A read and a command in one turn, their results delivered in one user message, as
		// the real one does for parallel tool use.
		f.use("Read", "r1", map[string]any{"file_path": filepath.Join(cwd, "a.go")})
		d := f.ask("Bash", "c1", map[string]any{"command": "go vet", "description": "vet"})
		out := "ok"
		if d.Behavior != "allow" {
			out = d.Message
		}
		f.write(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": "r1", "content": "1\tpackage a\n"},
			{"type": "tool_result", "tool_use_id": "c1", "content": out, "is_error": d.Behavior != "allow"},
		}}})
		f.text("paired")
		f.result("success", false, "paired: final")

	case "odd":
		// A permission request this side cannot read (its input is no object), then a call of
		// a tool the task does not offer: each must be answered, or the real one waits forever.
		fmt.Fprintln(f.out, "WARN: a line that is not a message")
		f.write(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
			"content": []map[string]any{
				{"type": "text", "text": "odd start"},
				{"type": "tool_use", "id": "x1", "name": "Bash", "input": "not an object"}}}})
		raw := f.request(map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "input": "not an object", "tool_use_id": "x1"})
		f.record(map[string]any{"ask": "Bash", "id": "x1", "decision": raw})
		f.toolResult("x1", "refused", true)
		d := f.ask("mcp__verse__nope", "x2", map[string]any{})
		f.toolResult("x2", d.Message, true)
		// A request whose line has a field of a type this side does not expect, and a request
		// of a kind it does not serve: each is still answered, the second as an error.
		raw = f.requestRaw("x3", map[string]any{"type": "control_request", "request_id": "x3", "is_error": "no",
			"request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "input": map[string]any{"command": "ls"}, "tool_use_id": "x3"}})
		f.record(map[string]any{"ask": "Bash", "id": "x3", "decision": raw})
		f.record(map[string]any{"hook": f.request(map[string]any{"subtype": "hook_callback", "callback_id": "h1"})})
		f.text("odd done")
		f.result("success", false, "odd done")

	case "burst":
		// Two permission requests at once, the second before the first has run: the real one
		// has not been seen to do this, and the driver must not spin if it does.
		f.use("Bash", "c1", map[string]any{"command": "go vet", "description": "vet"})
		r1 := f.requestAsync(map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "input": map[string]any{"command": "go vet"}, "tool_use_id": "c1"})
		f.use("Bash", "c2", map[string]any{"command": "go test", "description": "test"})
		r2 := f.requestAsync(map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "input": map[string]any{"command": "go test"}, "tool_use_id": "c2"})
		d1 := <-r1
		f.record(map[string]any{"ask": "Bash", "id": "c1", "decision": d1})
		f.toolResult("c1", "vetted", false)
		d2 := <-r2
		f.record(map[string]any{"ask": "Bash", "id": "c2", "decision": d2})
		f.toolResult("c2", "tested", false)
		f.text("burst done")
		f.result("success", false, "burst done")

	case "twoq":
		q1, q2 := "which colour?", "which size?"
		d := f.ask("AskUserQuestion", "q1", map[string]any{"questions": []map[string]any{
			{"question": q1, "header": "Colour", "options": []map[string]any{{"label": "red"}, {"label": "blue"}}},
			{"question": q2, "header": "Size", "multiSelect": true, "options": []map[string]any{{"label": "s"}, {"label": "m"}}}}})
		answers, _ := d.UpdatedInput["answers"].(map[string]any)
		a1, _ := answers[q1].(string)
		a2, _ := answers[q2].(string)
		f.toolResult("q1", "answered", false)
		f.text(fmt.Sprintf("done: %s, %s", a1, a2))
		f.result("success", false, "")

	case "cancelled":
		// Claude asks, then no longer waits on the answer (its turn moved on), then ends the
		// turn: the question must leave the docket without anyone answering it.
		f.use("Bash", "c1", map[string]any{"command": "go test", "description": "test"})
		r1 := f.requestAsync(map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "input": map[string]any{"command": "go test"}, "tool_use_id": "c1"})
		time.Sleep(300 * time.Millisecond) // long enough for the question to reach the docket
		f.mu.Lock()
		rid := fmt.Sprintf("req%d", f.seq)
		f.mu.Unlock()
		f.write(map[string]any{"type": "control_cancel_request", "request_id": rid})
		// It does not wait on the answer: it gives the use a result of its own and goes on.
		f.bg.Add(1)
		go func() {
			defer f.bg.Done()
			f.record(map[string]any{"ask": "Bash", "id": "c1", "decision": <-r1})
		}()
		f.toolResult("c1", "The user doesn't want to proceed with this tool use.", true)
		f.text("moved on")
		f.result("success", false, "moved on")

	case "takenback":
		// A second request while the first runs, taken back before anyone was asked about it.
		f.use("Bash", "c1", map[string]any{"command": "go vet", "description": "vet"})
		d1 := <-f.requestAsync(map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "input": map[string]any{"command": "go vet"}, "tool_use_id": "c1"})
		f.record(map[string]any{"ask": "Bash", "id": "c1", "decision": d1})
		f.use("Bash", "c2", map[string]any{"command": "go test", "description": "test"})
		r2 := f.requestAsync(map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "input": map[string]any{"command": "go test"}, "tool_use_id": "c2"})
		f.mu.Lock()
		rid := fmt.Sprintf("req%d", f.seq)
		f.mu.Unlock()
		f.write(map[string]any{"type": "control_cancel_request", "request_id": rid})
		f.bg.Add(1)
		go func() {
			defer f.bg.Done()
			select {
			case d := <-r2:
				f.record(map[string]any{"ask": "Bash", "id": "c2", "decision": d})
			case <-time.After(200 * time.Millisecond): // an answer would be here by now
				f.record(map[string]any{"ask": "Bash", "id": "c2", "decision": "none"})
			}
		}()
		f.toolResult("c2", "The user doesn't want to proceed with this tool use.", true)
		f.toolResult("c1", "vetted", false)
		f.text("taken back")
		f.result("success", false, "taken back")

	case "askgone":
		// Claude asks the person, then takes the question back while they are being asked.
		f.use("AskUserQuestion", "q1", map[string]any{"questions": []map[string]any{{"question": "which colour?"}}})
		r1 := f.requestAsync(map[string]any{"subtype": "can_use_tool", "tool_name": "AskUserQuestion",
			"input": map[string]any{"questions": []map[string]any{{"question": "which colour?"}}}, "tool_use_id": "q1"})
		time.Sleep(300 * time.Millisecond)
		f.mu.Lock()
		rid := fmt.Sprintf("req%d", f.seq)
		f.mu.Unlock()
		f.write(map[string]any{"type": "control_cancel_request", "request_id": rid})
		f.bg.Add(1)
		go func() {
			defer f.bg.Done()
			select {
			case d := <-r1:
				f.record(map[string]any{"ask": "AskUserQuestion", "id": "q1", "decision": d})
			case <-time.After(200 * time.Millisecond): // an answer would be here by now
				f.record(map[string]any{"ask": "AskUserQuestion", "id": "q1", "decision": "none"})
			}
		}()
		f.toolResult("q1", "The user doesn't want to proceed with this tool use.", true)
		f.text("asked nobody")
		f.result("success", false, "asked nobody")

	case "linked":
		// A write through a link in the workspace.
		d := f.ask("Write", "p1", map[string]any{"file_path": filepath.Join(cwd, "docs/link/x.sql"), "content": "select 1\n"})
		f.toolResult("p1", "written", d.Behavior != "allow")
		f.text("linked done")
		f.result("success", false, "linked done")

	case "late":
		// The command's result, the turn's last words and its end, all before anything this
		// side says after the result can reach it.
		d := f.ask("Bash", "c1", map[string]any{"command": "go vet", "description": "vet"})
		f.mu.Lock()
		f.toolResult("c1", "vetted", d.Behavior != "allow")
		f.text("first done")
		f.inTurn = false
		f.write(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "first done", "stop_reason": "end_turn"})
		f.mu.Unlock()

	case "nocall":
		// An allowed call of the runner's tool that Claude fails on its own side, so no
		// tools/call comes; then a command, which must still be answered.
		d := f.ask("mcp__verse__greet", "t1", map[string]any{"name": "ana"})
		f.record(map[string]any{"ask": "greet", "id": "t1", "decision": d})
		f.toolResult("t1", "MCP server \"verse\" is not connected", true)
		d = f.ask("Bash", "c2", map[string]any{"command": "go vet", "description": "vet"})
		f.toolResult("c2", "vetted", d.Behavior != "allow")
		f.text("nocall done")
		f.result("success", false, "nocall done")

	case "die":
		// Claude asks, then goes away while the question stands.
		f.use("Bash", "c1", map[string]any{"command": "go test", "description": "test"})
		f.requestAsync(map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "input": map[string]any{"command": "go test"}, "tool_use_id": "c1"})
		time.Sleep(300 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "claude: out of memory")
		os.Exit(3)

	case "hang":
		d := f.ask("Bash", "c1", map[string]any{"command": "sleep 1000", "description": "wait"})
		if d.Behavior == "allow" {
			<-f.interrupted
		}
		f.result("error_during_execution", true, "")

	default: // basic
		d := f.ask("Bash", "c1", map[string]any{"command": "go test ./...", "description": "run the tests"})
		if d.Behavior == "allow" {
			f.toolResult("c1", "ok\tverse\t0.1s\n", false)
		} else {
			f.toolResult("c1", d.Message, true)
		}

		d = f.ask("Write", "p1", map[string]any{"file_path": filepath.Join(cwd, "notes.txt"), "content": "hi\n"})
		if d.Behavior == "allow" {
			f.toolResult("p1", "File created successfully at: "+filepath.Join(cwd, "notes.txt"), false)
		} else {
			f.toolResult("p1", d.Message, true)
		}

		q := "which colour?"
		d = f.ask("AskUserQuestion", "q1", map[string]any{"questions": []map[string]any{{
			"question": q, "header": "Colour", "multiSelect": false,
			"options": []map[string]any{{"label": "red", "description": "warm"}, {"label": "blue", "description": "cool"}}}}})
		picked := ""
		if d.Behavior == "allow" {
			answers, _ := d.UpdatedInput["answers"].(map[string]any)
			picked, _ = answers[q].(string)
			f.toolResult("q1", fmt.Sprintf("The user answered: %q=%q", q, picked), false)
		} else {
			f.toolResult("q1", d.Message, true)
		}
		f.text("done: " + picked)
		f.result("success", false, "done: "+picked)
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
	logPath := filepath.Join(t.TempDir(), "claude.log")
	t.Setenv(fakeEnvVar, scenario)
	t.Setenv(fakeLogVar, logPath)
	return Creds{Bin: exe}, logPath
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

func renderFrames(fs []task.Event) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "%s tool=%s gate=%s proceed=%v by=%s text=%q err=%q\n", f.Kind, f.Tool, f.Gate, f.Proceed, f.By, f.Text, f.Err)
	}
	return b.String()
}

// allowAll gives Claude its shell, its edits and its questions, and asks about none of them.
var allowAll = task.Autonomy{
	{Match: &task.Match{System: "commands"}, Then: task.Then{Mode: task.ThenAllow}},
	{Match: &task.Match{System: "files"}, Then: task.Then{Mode: task.ThenAllow}},
	{Match: &task.Match{System: "question"}, Then: task.Then{Mode: task.ThenAllow}},
	{Then: task.Then{Mode: task.ThenAllow}},
}

// namedGates is the autonomy the tests that look at a question's row run under: commands as
// `shell`, writes as `write`.
var namedGates = task.Autonomy{
	{Name: "shell", Match: &task.Match{System: "commands", Verb: "run"}, Then: task.Then{Mode: task.ThenAsk}},
	{Name: "write", Match: &task.Match{System: "files", Verb: "write"}, Then: task.Then{Mode: task.ThenAsk}},
	{Match: &task.Match{System: "question"}, Then: task.Then{Mode: task.ThenAllow}},
	{Then: task.Then{Mode: task.ThenAsk}},
}

// EVERY COMMAND AND EDIT CLAUDE PROPOSES IS A QUESTION ON THE DOCKET, under the default
// autonomy, and the answer is what Claude hears: an allowed command runs and its output is the
// action's; a refused edit is denied and the model is told why; a question the model asks a
// person is answered from the same docket.
func TestClaudeActionsAreGatedOnTheDocket(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "basic")
	dir := t.TempDir()
	tk, err := Task(runner.Launch{ID: "run-claude-1", Prompt: "fix the flaky test", Agent: &runner.Agent{Name: Agent}, Environment: workEnv, Autonomy: namedGates},
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
	if p.Tool != kindBash || p.Question.Gate != "shell" {
		t.Fatalf("the first question is %s via gate %q, want %s via gate shell", p.Tool, p.Question.Gate, kindBash)
	}
	if !logHas(readLog(t, logPath), "argv", "--permission-prompt-tool", `"stdio"`, `"manual"`, "--setting-sources") {
		t.Error("claude was not started asking this side, under manual permissions, with no settings of a workspace's")
	}
	if !reg.Answer(p.Question.ID, task.Resolution{Choice: task.Proceed, Proceed: true, By: "user:ana"}) {
		t.Fatal("the answer was not delivered")
	}
	waitLog(t, logPath, `"id":"c1"`, `"allow"`)

	// 2. The edit: refused. Claude hears deny, with why, as the call's result.
	p = pendingOn(t, reg)
	if p.Tool != kindWrite || p.Question.Gate != "write" {
		t.Fatalf("the second question is %s via gate %q, want %s via gate write", p.Tool, p.Question.Gate, kindWrite)
	}
	if st := p.Question.Step; st.Name != task.StepFilesWrite || st.Fields["path"] != "notes.txt" {
		t.Errorf("the edit is asked about as %s, want a write of notes.txt (relative to the workspace)", st)
	}
	reg.Answer(p.Question.ID, task.Resolution{Choice: task.Deny, By: "user:ana", Note: "no new files in this change"})
	waitLog(t, logPath, `"id":"p1"`, `"deny"`, "no new files in this change")

	// 3. The model's question for the person: on the docket with Claude's options, answered by one.
	p = pendingOn(t, reg)
	if p.Tool != kindAsk {
		t.Fatalf("the third question is %s, want %s", p.Tool, kindAsk)
	}
	ids := task.OptionIDs(p.Question.Options)
	if strings.Join(ids, ",") != "red,blue,reply" {
		t.Errorf("the question offers %v, want Claude's options red,blue and an answer in words", ids)
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
	if !logHas(entries, `"id":"q1"`, `"answers"`, "blue") {
		t.Errorf("claude did not receive the answer blue:\n%s", render(entries))
	}

	// The read plane: the allowed command's output rides the loop's own exec frame, the refused
	// edit is denied and the agent's words are the task's narration.
	stopFrames()
	fs := frames()
	if _, ok := frameWhere(fs, func(f task.Event) bool {
		return f.Kind == task.EventExec && f.Tool == kindBash && strings.Contains(f.Text, "ok")
	}); !ok {
		t.Errorf("no exec frame carries the command's output; frames:\n%s", renderFrames(fs))
	}
	if _, ok := frameWhere(fs, func(f task.Event) bool {
		return f.Kind == task.EventDecision && f.Tool == kindWrite && !f.Proceed
	}); !ok {
		t.Errorf("no decision frame records the refused edit; frames:\n%s", renderFrames(fs))
	}
	if _, ok := frameWhere(fs, func(f task.Event) bool {
		return f.Kind == task.EventSay && f.By == "" && f.Text == "done: blue"
	}); !ok {
		t.Errorf("the agent's last message is not on the read plane; frames:\n%s", renderFrames(fs))
	}
}

// WHAT CLAUDE DOES WITHOUT ASKING IS ON THE READ PLANE: a read its permission mode allows is
// narrated when it completes, as the action it was, and the agent's words are the result.
func TestClaudeNarratesWhatItDidNotAsk(t *testing.T) {
	reg := task.NewRegistry()
	creds, _ := fakeCreds(t, "trusted")
	tk, err := Task(runner.Launch{ID: "run-claude-2", Prompt: "look around", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
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
		return f.Kind == task.EventExec && f.Tool == "read" && f.Text == "a.go"
	}); !ok {
		t.Errorf("the free read is not on the read plane as a read of a.go; frames:\n%s", renderFrames(fs))
	}
	if tk.Result() != "two files" {
		t.Errorf("result %q", tk.Result())
	}
}

// A STEER MID-TURN REACHES THE RUNNING SESSION, as a user message, at the next boundary — which
// for this driver is the next thing Claude asks about.
func TestClaudeSteerReachesTheRunningTurn(t *testing.T) {
	creds, logPath := fakeCreds(t, "basic")
	d := &driver{creds: creds, prompt: "work", dir: t.TempDir(),
		env: fakeEnv(), resolver: task.AutoResolver{Who: "test"}}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ex := &executor{d: d}

	tu, more, err := d.Next(ctx, task.StepResult{})
	if err != nil || !more || tu.Name != kindBash {
		t.Fatalf("first proposal: %v %v %s", err, more, tu.Name)
	}
	// Words arrive while the command is in front of the gate...
	d.Fold([]string{"skip the generated files"})
	tu.ID = "run-claude-3~a1"
	if _, _, err := ex.Execute(ctx, tu); err != nil {
		t.Fatal(err)
	}
	// ...and are taken at the next boundary, into the session whose turn is still running.
	tu, more, err = d.Next(ctx, task.StepResult{Use: tu})
	if err != nil || !more || tu.Name != kindWrite {
		t.Fatalf("second proposal: %v %v %s", err, more, tu.Name)
	}
	entries := readLog(t, logPath)
	if !logHas(entries, `"user":"skip the generated files"`, `"midTurn":true`) {
		t.Fatalf("the steer did not reach the running session:\n%s", render(entries))
	}
	// A refusal from the loop is a deny to Claude, with the reason as the call's result.
	tu, more, err = d.Next(ctx, task.StepResult{Use: tu, Blocked: true, Note: "docs only"})
	if err != nil || !more || tu.Name != kindAsk {
		t.Fatalf("third proposal: %v %v %s", err, more, tu.Name)
	}
	waitLog(t, logPath, `"id":"p1"`, `"deny"`, "docs only")
	tu.ID = "run-claude-3~a3"
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

// CANCEL INTERRUPTS THE TURN: a command Claude is still running when the run is cancelled is
// not left running.
func TestClaudeCancelInterruptsTheTurn(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "hang")
	tk, err := Task(runner.Launch{ID: "run-claude-4", Prompt: "wait", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
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
	waitLog(t, logPath, `"id":"c1"`, `"allow"`)
	cancel()
	select {
	case oc := <-done:
		if oc.Reason == string(task.Done) {
			t.Errorf("a cancelled run completed")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the run did not end after cancel")
	}
	waitLog(t, logPath, `"control":"interrupt"`)
}

// A TURN THAT FAILS FAILS THE RUN, with Claude's reason.
func TestClaudeTurnFailureFailsTheRun(t *testing.T) {
	reg := task.NewRegistry()
	creds, _ := fakeCreds(t, "fail")
	tk, err := Task(runner.Launch{ID: "run-claude-5", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll}, reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	oc, err := tk.Run(ctx)
	if oc.Reason != string(task.Failed) || err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("run ended %s with %v, want failed with Claude's reason", oc.Reason, err)
	}
}

// THE CREDENTIAL REACHES CLAUDE UNDER THE NAME IT READS IT BY, in a CLAUDE_CONFIG_DIR made for
// the task, which dies with the run. It is in the claude process's environment, which is where
// Claude reads it from, and the source says what that costs.
func TestClaudeCredentialIsHandedUnderItsName(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "trusted")
	creds.CredentialEnv, creds.Credential = "ANTHROPIC_API_KEY", "sk-ant-test-not-a-real-key"
	tk, err := Task(runner.Launch{ID: "run-claude-7", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
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
	home, _ := auth["configDir"].(string)
	if home == "" || auth["apiKey"] != creds.Credential {
		t.Errorf("claude was authenticated as %v, want the key under ANTHROPIC_API_KEY with a private CLAUDE_CONFIG_DIR", auth)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("the private CLAUDE_CONFIG_DIR %s outlived the run (%v)", home, err)
	}
}

// A CLAUDE THAT IS NOT THERE fails the run with a message that says so.
func TestClaudeMissingBinaryFailsTheRun(t *testing.T) {
	reg := task.NewRegistry()
	tk, err := Task(runner.Launch{ID: "run-claude-6", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv, Autonomy: namedGates},
		reg, Creds{Bin: filepath.Join(t.TempDir(), "no-such-claude")}, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	oc, err := tk.Run(context.Background())
	if oc.Reason != string(task.Failed) || err == nil || !strings.Contains(err.Error(), "no-such-claude") {
		t.Fatalf("run ended %s with %v", oc.Reason, err)
	}
}

// THE STEPS ARE DERIVED FROM WHAT CLAUDE ASKED: Bash is a command run, Edit and Write are a
// write of their path relative to the workspace, a question is `question.ask`, and a tool this
// side has no step for is refused, not guessed.
func TestClaudeSteps(t *testing.T) {
	w := t.TempDir()
	// docs/link and the chain docs/chain → mid/hop lead to db; docs/new.sql names a file of db
	// not there yet; out leads out of the workspace.
	for _, dir := range []string{"db", "docs", "mid"} {
		if err := os.Mkdir(filepath.Join(w, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for link, to := range map[string]string{"docs/link": "../db", "docs/chain": "../mid/hop", "mid/hop": "../db",
		"docs/new.sql": "../db/new.sql", "out": t.TempDir()} {
		if err := os.Symlink(to, filepath.Join(w, link)); err != nil {
			t.Fatal(err)
		}
	}
	d := &driver{dir: w, toolBy: map[string]verse.Tool{}, toolStep: verse.StepOf(nil),
		rpc: &rpc{cancels: map[string]chan struct{}{}}}
	ev := func(tool string, input map[string]any) event {
		return event{Type: "control_request", RequestID: "r", Req: controlRequest{Subtype: "can_use_tool", ToolName: tool, Input: input, ToolUseID: "u"}}
	}
	steps := func(p *proposal) string {
		var parts []string
		for _, st := range p.steps {
			parts = append(parts, st.String())
		}
		return strings.Join(parts, "; ")
	}

	p, tu, err := d.propose(ev("Bash", map[string]any{"command": "curl https://x.test", "description": "fetch it"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(p); got != "commands.run command=curl https://x.test" {
		t.Errorf("command: %s", got)
	}
	if tu.Input["reason"] != "fetch it" {
		t.Errorf("the command's reason %v, want Claude's description", tu.Input["reason"])
	}

	p, tu, err = d.propose(ev("Edit", map[string]any{"file_path": filepath.Join(w, "svc/a.go"), "old_string": "x", "new_string": "y"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := steps(p); got != "files.write path=svc/a.go" {
		t.Errorf("edit: %s", got)
	}
	if got := tu.Describe(); !strings.Contains(got, "svc/a.go") {
		t.Errorf("the edit describes itself as %q, want its path", got)
	}
	// A path is what a row sees: cleaned and relative to the workspace, whether Claude wrote it
	// with a detour, relative, or under the root's other spelling.
	real, err := filepath.EvalSymlinks(w)
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{filepath.Join(w, "cfg") + "/../.git/config": ".git/config", "./x/./y.go": "x/y.go",
		filepath.Join(real, "a.go"): "a.go"} {
		p, _, err = d.propose(ev("Write", map[string]any{"file_path": in, "content": ""}))
		if err != nil {
			t.Errorf("%s: %v", in, err)
		} else if got := steps(p); got != "files.write path="+want {
			t.Errorf("%s: %s, want path=%s", in, got, want)
		}
	}
	// A write through a symlink is decided on the path named and on the file of the workspace it
	// lands on, at the end of a chain, and where the last link names a file not there yet. Where
	// it leads out of the workspace, or is outside it to begin with, it is the path named alone:
	// the sandbox bounds where a write lands.
	for in, want := range map[string]string{
		filepath.Join(w, "docs/link/x.sql"):  "files.write path=docs/link/x.sql; files.write path=db/x.sql",
		filepath.Join(w, "docs/chain/x.sql"): "files.write path=docs/chain/x.sql; files.write path=db/x.sql",
		filepath.Join(w, "docs/new.sql"):     "files.write path=docs/new.sql; files.write path=db/new.sql",
		filepath.Join(w, "out/x"):            "files.write path=out/x",
		"/elsewhere/f.go":                    "files.write path=/elsewhere/f.go",
		"../../etc/passwd":                   "files.write path=" + filepath.Clean(filepath.Join(w, "../../etc/passwd")),
	} {
		p, _, err = d.propose(ev("Write", map[string]any{"file_path": in, "content": ""}))
		if err != nil {
			t.Errorf("%s: %v", in, err)
		} else if got := steps(p); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
	if _, tu, _ := d.propose(ev("Edit", map[string]any{"file_path": filepath.Join(w, "docs/link/x.sql")})); tu.Input["lands"] != "db/x.sql" {
		t.Errorf("the edit through a link describes itself as %v, want where it lands", tu.Input)
	}
	if _, _, err := d.propose(ev("Write", map[string]any{"file_path": "", "content": ""})); err == nil {
		t.Error("a write naming no file was accepted as an action")
	}
	if _, _, err := d.propose(ev("Bash", map[string]any{"command": "go run ./srv", "run_in_background": true})); err == nil {
		t.Error("a command in the background was accepted as an action")
	}
	if _, _, err := d.propose(ev("Read", map[string]any{"file_path": "/etc/hosts"})); err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Errorf("a read outside the workspace is refused as %v, which does not say why", err)
	}
	if p, _, err := d.propose(ev("Bash", nil)); err != nil || p.ev.Req.Input == nil {
		t.Errorf("a request with no input is allowed back with %v (%v), want an object", p.ev.Req.Input, err)
	}
	if _, _, err := d.propose(ev("AskUserQuestion", map[string]any{"questions": "not a list"})); err == nil {
		t.Error("a request asking no readable question was accepted as an action")
	}

	p, _, _ = d.propose(ev("AskUserQuestion", map[string]any{"questions": []any{map[string]any{"question": "why?", "header": "h"}}}))
	if got := steps(p); got != task.StepQuestionAsk {
		t.Errorf("a question derives %q, want %s", got, task.StepQuestionAsk)
	}

	if _, _, err := d.propose(ev("WebFetch", map[string]any{"url": "https://x.test"})); err == nil {
		t.Error("a tool this side has no step for was accepted as an action")
	}
	if _, _, err := d.propose(ev("mcp__verse__nope", map[string]any{})); err == nil {
		t.Error("a call of a tool the task does not offer was accepted as an action")
	}
}

// A NEW TURN OWES THE LAST NOTHING: a request set aside while a turn was cut short, and the
// uses it left, are about calls Claude is no longer making; words for a running turn keep them.
func TestANewTurnStartsClean(t *testing.T) {
	d := &driver{rpc: &rpc{stdin: nopWriteCloser{io.Discard}}}
	stale := func() {
		d.queued = []event{{Type: "control_request", RequestID: "stale"}}
		d.uses = map[string]block{"u": {Type: "tool_use", ID: "u"}}
		d.answered = map[string]bool{"u": true}
	}
	stale()
	if err := d.say("next"); err != nil {
		t.Fatal(err)
	}
	if len(d.queued)+len(d.uses)+len(d.answered) != 0 {
		t.Errorf("a new turn carried the last one's queued %v, uses %v, answered %v", d.queued, d.uses, d.answered)
	}
	stale()
	if err := d.say("and also"); err != nil {
		t.Fatal(err)
	}
	if len(d.queued) != 1 || len(d.uses) != 1 || len(d.answered) != 1 {
		t.Errorf("words for the running turn dropped its queued %v, uses %v, answered %v", d.queued, d.uses, d.answered)
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// THE CONFIG SAYS HOW CLAUDE RUNS: the credential is read from the variable the section names
// and never kept in it, and the command line asks this side, under manual permissions, with
// the tools this side has a step for and nothing of a workspace's. What the flags DO is not
// this test's to know: that `manual` asks, that the `ask` rule outranks Claude's read-only
// list, that `--tools` keeps the MCP server's tools, and that `--setting-sources ""` ignores
// a workspace's settings were each seen against Claude Code 2.1.304, locally and on staging;
// and that `--replay-user-messages` echoes a message where it is taken up, after a tool's result
// in the running turn or as the start of a turn of its own, against 2.1.304 and 2.1.305 locally;
// and that `--effort` takes the five levels Efforts lists, which 2.1.304's `--help` names as
// its own (issue 0t4bdajwh12krvwatxn0).
func TestConfigIsChecked(t *testing.T) {
	c := &Config{Bin: "/opt/claude"}
	t.Setenv(EnvOAuthToken, "")
	t.Setenv(EnvAPIKey, "secret")
	creds := c.Creds(Key())
	if creds.Credential != "secret" || creds.CredentialEnv != EnvAPIKey {
		t.Errorf("resolved %+v", creds)
	}
	// A subscription's token is taken first, where both are set.
	t.Setenv(EnvOAuthToken, "token")
	if env, v := Key(); env != EnvOAuthToken || v != "token" {
		t.Errorf("with both set, read %s=%s", env, v)
	}
	got := strings.Join(creds.argv("m2", "high", true, true), " ")
	for _, want := range []string{"/opt/claude -p", "--replay-user-messages", "--permission-prompt-tool stdio", "--permission-mode manual",
		"--setting-sources  --settings", `"ask":["Bash","Edit","Write","AskUserQuestion"]`, "--strict-mcp-config",
		"--tools Bash,Read,Edit,Write,Glob,Grep,AskUserQuestion",
		`"type":"sdk"`, "--model m2", "--effort high"} {
		if !strings.Contains(got, want) {
			t.Errorf("argv %q lacks %q", got, want)
		}
	}
	if got := strings.Join(creds.argv("", "", false, false), " "); strings.Contains(got, "--mcp-config") || strings.Contains(got, "--model") ||
		strings.Contains(got, "AskUserQuestion") {
		t.Errorf("argv with nothing to offer, no questions and no model: %q", got)
	}
	if why := MissingKey(os.Getenv); why != "" {
		t.Errorf("a set credential reported missing: %s", why)
	}
	if why := MissingKey(func(string) string { return "" }); !strings.Contains(why, EnvOAuthToken) || !strings.Contains(why, EnvAPIKey) {
		t.Errorf("an unset credential not reported: %q", why)
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

// THE RUNNER'S TOOLS ARE CLAUDE'S TOOLS: served to it as an MCP server over the same pipe, and a
// call of one is an action of the loop — the tool's own step, decided by the gates and autonomy,
// performed by the tool in this process, its result the call's output.
func TestClaudeIsOfferedTheRunnersTools(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "tool")
	tk, err := Task(runner.Launch{ID: "run-claude-8", Prompt: "greet", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
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
	if !logHas(entries, "toolsList", `"name":"greet"`, "say hello", `"required":["name"]`) {
		t.Errorf("the tool was not declared to claude on tools/list:\n%s", render(entries))
	}
	if !logHas(entries, "toolCall", `"isError":false`, "hello, ana") {
		t.Errorf("claude did not get the tool's result:\n%s", render(entries))
	}
	if !logHas(entries, "notified", `"mcp_response":{"jsonrpc":"2.0","result":{}}`) {
		t.Errorf("the MCP notification was not acknowledged as the SDKs acknowledge one:\n%s", render(entries))
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

// A CALL THE GATE REFUSES IS REFUSED TO CLAUDE AS THE CALL'S RESULT, with the reason, so the
// model reads it where it asked and plans around it.
func TestARefusedToolCallIsClaudesToRead(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "tool")
	tk, err := Task(runner.Launch{ID: "run-claude-9", Prompt: "greet", Agent: &runner.Agent{Name: Agent}, Environment: workEnv, Autonomy: namedGates},
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
	if !logHas(entries, `"id":"t1"`, `"deny"`, "no greetings today") {
		t.Errorf("claude was not told the refusal and why:\n%s", render(entries))
	}
	if !strings.Contains(tk.Result(), "false") || !strings.Contains(tk.Result(), "refused") {
		t.Errorf("result %q", tk.Result())
	}
}

// THE SPAWN'S MODEL AND EFFORT REACH CLAUDE: the agent's `model` setting is the session's model,
// and its `effort` the session's effort.
func TestClaudeTakesTheSpawnsModelAndEffort(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "trusted")
	tk, err := Task(runner.Launch{ID: "run-claude-10", Prompt: "x", Agent: &runner.Agent{Name: Agent, Settings: map[string]string{"model": "claude-opus-5-5", "effort": "high"}}, Environment: workEnv,
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
	if !logHas(entries, "argv", `"--model","claude-opus-5-5"`, `"--effort","high"`) {
		t.Errorf("the spawn's model and effort did not reach the command line:\n%s", render(entries))
	}
	// And the task says what it ran with, for the record.
	if _, set := tk.Agent(); set["model"] != "claude-opus-5-5" || set["effort"] != "high" {
		t.Errorf("the task's settings: %v", set)
	}
	// A spawn naming no model reaches the driver naming none, and runs Claude's own: the runner's
	// configured default is filled in before (package stock), so the record says it either way.
	tk2, err := Task(runner.Launch{ID: "run-claude-10b", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll}, task.NewRegistry(), creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	if _, set := tk2.Agent(); set["model"] != "" || set["effort"] != "" {
		t.Errorf("the default's settings: %v", set)
	}
}

// THE CREDENTIAL FILE IS CLAUDE'S TOO: what a spawn's `credential` minted is handed to the
// claude process as it is to a shell, by the variable naming the file, so a command Claude runs
// finds it.
func TestClaudeIsHandedTheTasksCredential(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "trusted")
	tk, err := Task(runner.Launch{ID: "run-claude-11", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
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
		t.Errorf("the credential file's name did not reach the claude process:\n%s", render(readLog(t, logPath)))
	}
}

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnvVar) != "" {
		fakeServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A RESULT BESIDE THE ONE THE EXECUTOR WAITS FOR IS STILL NARRATED: the real one delivers the
// results of a turn's parallel tool uses in one user message, and the free read among them is
// on the read plane as it would be alone.
func TestASiblingResultIsNarrated(t *testing.T) {
	reg := task.NewRegistry()
	creds, _ := fakeCreds(t, "pair")
	tk, err := Task(runner.Launch{ID: "run-claude-12", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll}, reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	frames, stopFrames := collect(tk)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if oc, err := tk.Run(ctx); err != nil || oc.Reason != string(task.Done) {
		t.Fatalf("run: %v %s", err, oc.Reason)
	}
	stopFrames()
	fs := frames()
	if _, ok := frameWhere(fs, func(f task.Event) bool {
		return f.Kind == task.EventExec && f.Tool == "read" && f.Text == "a.go"
	}); !ok {
		t.Errorf("the read beside the command is not on the read plane; frames:\n%s", renderFrames(fs))
	}
	if _, ok := frameWhere(fs, func(f task.Event) bool {
		return f.Kind == task.EventExec && f.Tool == kindBash && f.Text == "ok"
	}); !ok {
		t.Errorf("the command's output is not on its exec frame; frames:\n%s", renderFrames(fs))
	}
	if got := tk.Result(); got != "paired: final" {
		t.Errorf("result %q, want the turn's own account of its answer", got)
	}
}

// EVERY REQUEST IS ANSWERED, the ones this side cannot act on too: one it cannot read is refused
// rather than dropped, and a call of a tool the task does not offer is refused without being
// narrated as a call that ran. Either left unanswered is a Claude that waits forever.
func TestARequestThisSideCannotActOnIsRefused(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "odd")
	tk, err := Task(runner.Launch{ID: "run-claude-13", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll}, reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
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
	if !logHas(entries, `"id":"x1"`, `"deny"`) || !logHas(entries, `"id":"x2"`, `"deny"`, "does not offer") ||
		!logHas(entries, `"id":"x3"`, `"deny"`, "cannot read") {
		t.Errorf("the requests were not all refused:\n%s", render(entries))
	}
	if !logHas(entries, `"hook"`, `"error"`, "hook_callback") {
		t.Errorf("a request of a kind this side does not serve was not answered as an error:\n%s", render(entries))
	}
	stopFrames()
	fs := frames()
	if f, ok := frameWhere(fs, func(f task.Event) bool {
		return f.Kind == task.EventExec && (strings.Contains(f.Tool, "nope") || strings.Contains(f.Text, "refused"))
	}); ok {
		t.Errorf("a refused call is narrated as one that ran: %+v", f)
	}
	// The text beside the unreadable block is still narrated, and the stray line before it
	// did not end the stream.
	if _, ok := frameWhere(fs, func(f task.Event) bool {
		return f.Kind == task.EventSay && f.Text == "odd start"
	}); !ok {
		t.Errorf("the text beside an unreadable block is not on the read plane; frames:\n%s", renderFrames(fs))
	}
}

// A SECOND REQUEST WHILE THE EXECUTOR WAITS IS KEPT FOR NEXT, and the wait reads on: the
// executor reads the stream and never what it set aside, or it would hand itself the same
// request forever and the awaited result would never be read.
func TestASecondRequestMidAwaitIsKeptForNext(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "burst")
	tk, err := Task(runner.Launch{ID: "run-claude-14", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll}, reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
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
	if !logHas(entries, `"id":"c1"`, `"allow"`) || !logHas(entries, `"id":"c2"`, `"allow"`) {
		t.Errorf("both requests were not allowed:\n%s", render(entries))
	}
	stopFrames()
	fs := frames()
	for _, want := range []string{"vetted", "tested"} {
		if _, ok := frameWhere(fs, func(f task.Event) bool {
			return f.Kind == task.EventExec && f.Tool == kindBash && f.Text == want
		}); !ok {
			t.Errorf("no exec frame carries %q; frames:\n%s", want, renderFrames(fs))
		}
	}
}

// EACH QUESTION OF ONE ASK IS ITS OWN ON THE DOCKET: the first under the action's id, the next
// suffixed, so an answer names one question, and Claude reads each under its own key.
func TestEachQuestionIsItsOwnOnTheDocket(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "twoq")
	tk, err := Task(runner.Launch{ID: "run-claude-15", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll}, reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan task.Outcome, 1)
	go func() {
		oc, _ := tk.Run(context.Background())
		done <- oc
	}()
	p := pendingOn(t, reg)
	first := p.Question.ID
	if p.Question.Reason != "Colour: which colour?" {
		t.Fatalf("the first question reads %q", p.Question.Reason)
	}
	reg.Answer(first, task.Resolution{Choice: "red", By: "user:ana"})
	p = pendingOn(t, reg)
	if p.Question.ID != first+"-2" || !strings.HasPrefix(p.Question.Reason, "Size: which size? (several may apply") {
		t.Fatalf("the second question is %q %q, want %s-2 asking the size", p.Question.ID, p.Question.Reason, first)
	}
	reg.Answer(p.Question.ID, task.Resolution{Choice: "m", By: "user:ana"})
	select {
	case oc := <-done:
		if oc.Reason != string(task.Done) {
			t.Fatalf("the run ended %s", oc.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the run did not end")
	}
	if got := tk.Result(); got != "done: red, m" {
		t.Errorf("result %q, want each answer under its own question", got)
	}
	if !logHas(readLog(t, logPath), `"which colour?":"red"`, `"which size?":"m"`) {
		t.Errorf("claude did not get both answers by question:\n%s", render(readLog(t, logPath)))
	}
}

// A WITHDRAWN QUESTION IS NOT A REFUSAL: Claude is told the action was not decided and may be
// proposed again, where a refusal tells it not to retry.
func TestAWithdrawnQuestionReadsAsNotDecided(t *testing.T) {
	if got := refusal("bash", task.WithdrawnNote+": an operator said something"); !strings.Contains(got, "not refused") || strings.Contains(got, "Do not retry") {
		t.Errorf("withdrawn reads %q", got)
	}
	if got := refusal("bash", "no new files"); !strings.Contains(got, "Do not retry") || !strings.Contains(got, "no new files") {
		t.Errorf("refused reads %q", got)
	}
}

// A REQUEST CLAUDE CANCELS LEAVES THE DOCKET: the question about it is withdrawn, nobody has to
// answer for an action Claude no longer waits on, and Claude is told it was not decided.
func TestARequestClaudeCancelsLeavesTheDocket(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "cancelled")
	tk, err := Task(runner.Launch{ID: "run-claude-16", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv, Autonomy: namedGates},
		reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan task.Outcome, 1)
	go func() {
		oc, _ := tk.Run(context.Background())
		done <- oc
	}()
	p := pendingOn(t, reg)
	if p.Tool != kindBash {
		t.Fatalf("the question is about %s", p.Tool)
	}
	select {
	case oc := <-done:
		if oc.Reason != string(task.Done) {
			t.Fatalf("the run ended %s", oc.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the run did not end: the cancelled request's question stood on the docket")
	}
	if n := len(reg.Pending()); n != 0 {
		t.Errorf("%d question(s) still on the docket after the run", n)
	}
	if !logHas(readLog(t, logPath), `"id":"c1"`, `"deny"`, "not refused") {
		t.Errorf("claude was not told the question was withdrawn:\n%s", render(readLog(t, logPath)))
	}
}

// A REQUEST CLAUDE TAKES BACK WHILE IT WAITS ITS TURN IS NOT ASKED ABOUT: it was set aside while
// another call ran, and Claude cancelled it and gave the use a result of its own before the loop
// came to it. Allowing it then would leave the executor waiting on a result already gone past.
func TestARequestTakenBackWhileQueuedIsNotAsked(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "takenback")
	tk, err := Task(runner.Launch{ID: "run-claude-17", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll}, reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if oc, err := tk.Run(ctx); err != nil || oc.Reason != string(task.Done) {
		t.Fatalf("run: %v %s", err, oc.Reason)
	}
	if got := tk.Result(); got != "taken back" {
		t.Errorf("result %q", got)
	}
	if es := waitLog(t, logPath, `"id":"c2"`); !logHas(es, `"id":"c2"`, `"decision":"none"`) {
		t.Errorf("the request Claude took back was answered:\n%s", render(es))
	}
}

// A QUESTION CLAUDE TAKES BACK WHILE THE PERSON IS ASKED LEAVES THE DOCKET, and nothing is
// allowed: Claude no longer waits on the answer.
func TestAQuestionClaudeTakesBackLeavesTheDocket(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "askgone")
	tk, err := Task(runner.Launch{ID: "run-claude-18", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
		Autonomy: allowAll}, reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan task.Outcome, 1)
	go func() {
		oc, _ := tk.Run(context.Background())
		done <- oc
	}()
	if p := pendingOn(t, reg); p.Question.Reason != "which colour?" {
		t.Fatalf("the docket asks %q", p.Question.Reason)
	}
	select {
	case oc := <-done:
		if oc.Reason != string(task.Done) {
			t.Fatalf("the run ended %s", oc.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the run did not end: the question Claude took back stood on the docket")
	}
	if n := len(reg.Pending()); n != 0 {
		t.Errorf("%d question(s) still on the docket after the run", n)
	}
	if es := waitLog(t, logPath, `"id":"q1"`); !logHas(es, `"id":"q1"`, `"decision":"none"`) {
		t.Errorf("the question Claude took back was answered:\n%s", render(es))
	}
}

// A DECISION THAT ARRIVES AFTER CLAUDE TOOK ITS REQUEST BACK ALLOWS NOTHING: the action is
// reported withdrawn, and nothing is written to Claude.
func TestADecisionAfterClaudeCancelledAllowsNothing(t *testing.T) {
	var sent strings.Builder
	d := &driver{rpc: &rpc{stdin: nopWriteCloser{&sent}, cancels: map[string]chan struct{}{}}, answered: map[string]bool{}}
	p, tu, err := d.propose(event{Type: "control_request", RequestID: "r",
		Req: controlRequest{Subtype: "can_use_tool", ToolName: "Bash", Input: map[string]any{"command": "ls"}, ToolUseID: "u"}})
	if err != nil {
		t.Fatal(err)
	}
	d.proposed.Store(p)
	d.rpc.cancelled("r")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, _, err := (&executor{d: d}).Execute(ctx, tu)
	if err != nil || !res.Blocked || !strings.HasPrefix(res.Note, task.WithdrawnNote) {
		t.Errorf("executed as %+v %v, want withdrawn", res, err)
	}
	if sent.Len() != 0 {
		t.Errorf("claude was sent %s", sent.String())
	}
}

// A REQUEST IS CLAUDE'S TO CANCEL UNTIL IT IS SETTLED: answered, allowed or refused, or let go
// of once Claude took it back. A cancel that arrives before this side looks at the request is
// kept for it, one about a request already settled keeps nothing, and every one still open is
// cancelled when Claude goes away, including one this side has not looked at yet.
func TestARequestIsCancelledUntilSettled(t *testing.T) {
	r := &rpc{stdin: nopWriteCloser{io.Discard}, cancels: map[string]chan struct{}{}}
	r.request(event{Type: "control_request", RequestID: "a"})
	r.request(event{Type: "control_request", RequestID: "e"})
	r.request(event{Type: "control_request", RequestID: "w"})
	r.cancelled("w")
	if !isClosed(r.Cancelled("w")) {
		t.Error("a request Claude cancelled before this side looked at it is still waited on")
	}
	if err := r.Allow("a", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := r.respondError("e", "not served"); err != nil {
		t.Fatal(err)
	}
	r.settled("w")
	r.cancelled("a")
	if len(r.cancels) != 0 {
		t.Errorf("settled requests are still kept: %v", r.cancels)
	}
	open := r.Cancelled("b")
	r.cancelAll()
	if !isClosed(open) || !isClosed(r.Cancelled("c")) {
		t.Error("a request of a claude that went away is still waited on")
	}
}

// A GATE QUESTION GOES WITH CLAUDE: a claude that dies while its request stands takes the
// question off the docket, and the run fails with what it said, rather than waiting on an
// answer nobody needs.
func TestAGateQuestionGoesWithClaude(t *testing.T) {
	reg := task.NewRegistry()
	creds, _ := fakeCreds(t, "die")
	tk, err := Task(runner.Launch{ID: "run-claude-19", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv, Autonomy: namedGates},
		reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan task.Outcome, 1)
	go func() {
		oc, _ := tk.Run(context.Background())
		done <- oc
	}()
	pendingOn(t, reg)
	select {
	case oc := <-done:
		if oc.Reason != string(task.Failed) {
			t.Fatalf("the run ended %s", oc.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the run did not end: the question outlived claude")
	}
	if n := len(reg.Pending()); n != 0 {
		t.Errorf("%d question(s) still on the docket after the run", n)
	}
}

// AN ALLOWED CALL THAT NEVER ARRIVES ENDS THE WAIT ON ITS RESULT: Claude failed the call of the
// runner's tool on its own side and moved on, so the next thing it asks is answered.
func TestAnAllowedCallThatNeverArrivesEndsTheWait(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "nocall")
	tk, err := Task(runner.Launch{ID: "run-claude-20", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
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
	if !logHas(readLog(t, logPath), `"id":"c2"`, `"allow"`) {
		t.Errorf("the command after the failed call was not answered:\n%s", render(readLog(t, logPath)))
	}
	stopFrames()
	fs := frames()
	if _, ok := frameWhere(fs, func(f task.Event) bool {
		return f.Kind == task.EventExec && f.Tool == "greet" && strings.Contains(f.Text, "did not reach the runner") && strings.Contains(f.Text, "not connected")
	}); !ok {
		t.Errorf("the call is not reported as one that did not reach the runner; frames:\n%s", renderFrames(fs))
	}
}

// WORDS THAT REACH CLAUDE AFTER ITS TURN ENDED ARE THE NEXT TURN'S: the driver sent them as
// words for the running turn, but Claude had already ended it, so they start a turn of their
// own, and the run goes on to that turn's end rather than ending on the last.
func TestALateSayIsTheNextTurn(t *testing.T) {
	creds, logPath := fakeCreds(t, "late")
	d := &driver{creds: creds, prompt: "work", dir: t.TempDir(),
		env: fakeEnv(), resolver: task.AutoResolver{Who: "test"}}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tu, more, err := d.Next(ctx, task.StepResult{})
	if err != nil || !more || tu.Name != kindBash {
		t.Fatalf("first proposal: %v %v %s", err, more, tu.Name)
	}
	d.Fold([]string{"also do the docs"})
	tu.ID = "run-claude-21~a1"
	if _, _, err := (&executor{d: d}).Execute(ctx, tu); err != nil {
		t.Fatal(err)
	}
	if _, more, err = d.Next(ctx, task.StepResult{Use: tu}); err != nil || more {
		t.Fatalf("the run did not end: %v %v", err, more)
	}
	waitLog(t, logPath, `"user":"also do the docs"`)
	if got := d.Result(); got != "again: also do the docs" {
		t.Errorf("result %q, want the turn the words started", got)
	}
}

// A CONVERSATION WITH CLAUDE: when its turn ends the person is handed the turn, their words are
// Claude's next turn, and the run ends when they end it.
func TestClaudeHoldsAConversation(t *testing.T) {
	reg := task.NewRegistry()
	creds, _ := fakeCreds(t, "trusted")
	tk, err := Task(runner.Launch{ID: "run-claude-30", Prompt: "look around", Agent: &runner.Agent{Name: Agent}, Environment: workEnv,
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
		t.Errorf("result %q, want Claude's answer to the person's words", got)
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

// CLAUDE IS NOT OFFERED A QUESTION THE ENVIRONMENT DOES NOT GIVE: an environment with no
// `question` leaves Claude without its question tool, as it leaves the verse agent without
// `question-ask`.
func TestClaudeIsNotOfferedQuestionsTheEnvironmentDoesNotGive(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "trusted")
	noQuestions := systems.Environment{Systems: systems.Systems{
		"commands": workEnv.Systems["commands"],
		"files":    workEnv.Systems["files"],
	}}
	tk, err := Task(runner.Launch{ID: "run-claude-22", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: noQuestions, Autonomy: allowAll},
		reg, creds, fakeEnv(), t.TempDir(), nil, systemstest.Open(noQuestions))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := tk.Run(ctx); err != nil {
		t.Fatal(err)
	}
	es := readLog(t, logPath)
	if !logHas(es, "argv") || logHas(es, "argv", "AskUserQuestion") {
		t.Errorf("claude was offered its question tool in an environment without questions:\n%s", render(es))
	}
}

// A ROW ON WHERE A WRITE LANDS DECIDES IT: a write through `docs/link → ../db` is denied by a
// row on `db/**`, though the path Claude names is under `docs`, which is allowed.
func TestARowOnWhereAWriteLandsDecidesIt(t *testing.T) {
	reg := task.NewRegistry()
	creds, logPath := fakeCreds(t, "linked")
	w := t.TempDir()
	for _, dir := range []string{"db", "docs"} {
		if err := os.Mkdir(filepath.Join(w, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../db", filepath.Join(w, "docs/link")); err != nil {
		t.Fatal(err)
	}
	tk, err := Task(runner.Launch{ID: "run-claude-23", Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: workEnv, Autonomy: task.Autonomy{
		{Name: "db", Match: &task.Match{System: "files", Verb: "write", Params: map[string]string{"path": "db/**"}}, Then: task.Then{Mode: task.ThenDeny}},
		{Name: "docs", Match: &task.Match{System: "files", Verb: "write", Params: map[string]string{"path": "docs/**"}}, Then: task.Then{Mode: task.ThenAllow}},
		{Match: &task.Match{System: "commands"}, Then: task.Then{Mode: task.ThenAllow}},
	}}, reg, creds, fakeEnv(), w, nil, systemstest.Open(workEnv))
	if err != nil {
		t.Fatal(err)
	}
	frames, stopFrames := collect(tk)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if oc, err := tk.Run(ctx); err != nil || oc.Reason != string(task.Done) {
		t.Fatalf("run: %v %s", err, oc.Reason)
	}
	if !logHas(readLog(t, logPath), `"id":"p1"`, `"deny"`) {
		t.Errorf("the write through the link was not denied:\n%s", render(readLog(t, logPath)))
	}
	stopFrames()
	fs := frames()
	if _, ok := frameWhere(fs, func(f task.Event) bool {
		return f.Kind == task.EventDecision && f.Tool == kindWrite && f.Gate == "db" && !f.Proceed
	}); !ok {
		t.Errorf("the write was not decided by the row on where it lands; frames:\n%s", renderFrames(fs))
	}
}

// A WRITE THROUGH A LINK GOES AHEAD ONLY WHERE IT LANDS WOULD TOO: under "allow docs, deny the
// rest", and, with every write allowed, in an environment or under a runner's ceiling that gives
// writes under `docs/**` alone, a write through `docs/link → ../db` is denied, though the path
// Claude names is allowed.
func TestAWriteThroughALinkIsDecidedWhereItLandsWhereverTheRowStands(t *testing.T) {
	files := func(m task.Then) task.Row { return task.Row{Match: &task.Match{System: "files"}, Then: m} }
	commands := task.Row{Match: &task.Match{System: "commands"}, Then: task.Then{Mode: task.ThenAllow}}
	for i, c := range []struct {
		name     string
		autonomy task.Autonomy
		env      systems.Environment
		ceilings map[string]systems.Ceiling
		by       string
	}{
		{"allow docs, deny the rest", task.Autonomy{
			{Name: "docs", Match: &task.Match{System: "files", Verb: "write", Params: map[string]string{"path": "docs/**"}}, Then: task.Then{Mode: task.ThenAllow}},
			{Name: "rest", Match: &task.Match{System: "files", Verb: "write"}, Then: task.Then{Mode: task.ThenDeny}},
			commands,
		}, workEnv, systemstest.Open(workEnv), task.ByAutonomy},
		{"the environment of docs", task.Autonomy{files(task.Then{Mode: task.ThenAllow}), commands}, systems.Environment{Systems: systems.Systems{
			"commands": workEnv.Systems["commands"],
			"files":    {{Verbs: map[string][]systems.Shape{"write": {{"path": "docs/**"}}}}},
		}}, systemstest.Open(workEnv), task.ByEnvironment},
		{"the ceiling of docs", task.Autonomy{files(task.Then{Mode: task.ThenAllow}), commands}, workEnv, map[string]systems.Ceiling{"files": {
			{Verbs: map[string][]systems.Shape{"write": {{"path": "docs/**"}}}},
		}, "commands": systems.Open()}, task.ByCeiling},
	} {
		t.Run(c.name, func(t *testing.T) {
			reg := task.NewRegistry()
			creds, logPath := fakeCreds(t, "linked")
			w := t.TempDir()
			for _, dir := range []string{"db", "docs"} {
				if err := os.Mkdir(filepath.Join(w, dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink("../db", filepath.Join(w, "docs/link")); err != nil {
				t.Fatal(err)
			}
			tk, err := Task(runner.Launch{ID: fmt.Sprintf("run-claude-3%d", i), Prompt: "x", Agent: &runner.Agent{Name: Agent}, Environment: c.env, Autonomy: c.autonomy},
				reg, creds, fakeEnv(), w, nil, c.ceilings)
			if err != nil {
				t.Fatal(err)
			}
			frames, stopFrames := collect(tk)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if oc, err := tk.Run(ctx); err != nil || oc.Reason != string(task.Done) {
				t.Fatalf("run: %v %s", err, oc.Reason)
			}
			if !logHas(readLog(t, logPath), `"id":"p1"`, `"deny"`) {
				t.Errorf("the write through the link was not denied:\n%s", render(readLog(t, logPath)))
			}
			stopFrames()
			fs := frames()
			if _, ok := frameWhere(fs, func(f task.Event) bool {
				return f.Kind == task.EventDecision && f.Tool == kindWrite && f.By == c.by && !f.Proceed
			}); !ok {
				t.Errorf("the write was not refused %s; frames:\n%s", c.by, renderFrames(fs))
			}
		})
	}
}

// A CUT KEEPS WHOLE RUNES: an edit's text, a line said about a call and a call's output are
// bounded in bytes, and a rune across the bound is left out whole rather than split, so the
// docket and the decision frame carry valid UTF-8.
func TestACutKeepsWholeRunes(t *testing.T) {
	s := strings.Repeat("a", 199) + "é" + "b" // é is bytes 199 and 200
	if got := truncate(s, 200); got != strings.Repeat("a", 199)+"…" {
		t.Errorf("truncate: %q", got)
	}
	if got := firstLine(s + "\nmore"); got != strings.Repeat("a", 199)+"…" {
		t.Errorf("firstLine: %q", got)
	}
	out := "é" + strings.Repeat("b", 3999) // the last 4000 bytes start inside é
	if got := commandOutput(out); got != "…"+strings.Repeat("b", 3999) {
		t.Errorf("commandOutput: %q", got[:8])
	}
}
