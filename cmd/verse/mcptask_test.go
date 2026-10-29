package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pollTool calls a tool until what it answers contains want.
func pollTool(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any, want string) string {
	t.Helper()
	var out string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if out, _ = callTool(t, cs, tool, args); strings.Contains(out, want) {
			return out
		}
	}
	t.Fatalf("%s %v never said %q; last:\n%s", tool, args, want, out)
	return ""
}

// transcript reads a task's transcript from since until the task has ended, and answers its frames
// and the calls it took.
func transcript(t *testing.T, cs *mcp.ClientSession, id, since string) ([]string, int) {
	t.Helper()
	var frames []string
	calls := 0
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		args := map[string]any{"id": id, "timeout": 5}
		if since != "" {
			args["since"] = since
		}
		out, isErr := callTool(t, cs, "verse_task_events", args)
		if isErr {
			t.Fatalf("verse_task_events %v: %s", args, out)
		}
		calls++
		lines := strings.Split(out, "\n")
		last := lines[len(lines)-1]
		for _, l := range lines[:len(lines)-1] {
			if !strings.HasPrefix(l, "nothing in ") {
				frames = append(frames, l)
			}
		}
		cursor, ended := strings.CutSuffix(strings.TrimPrefix(last, "cursor: "), "; the task has ended")
		if ended {
			return frames, calls
		}
		since = cursor
	}
	t.Fatalf("the transcript of %s never ended: %q", id, frames)
	return nil, 0
}

func TestTheTaskToolsRunAScriptAndReadItsTranscript(t *testing.T) {
	serve(t)
	cs := mcpSession(t)
	id, isErr := callTool(t, cs, "verse_task_run", scriptRun("echo hi"))
	if isErr || id == "" || strings.ContainsAny(id, " \n") {
		t.Fatalf("the run answered %q (error=%v), not an id", id, isErr)
	}
	frames, _ := transcript(t, cs, id, "")
	if all := strings.Join(frames, "\n"); !strings.Contains(all, "kind: exec") || !strings.Contains(all, "text: done") {
		t.Fatalf("the transcript:\n%s", all)
	}
	pollTool(t, cs, "verse_task", map[string]any{"id": id, "trim": "{status}"}, "status: done")
	if out, _ := callTool(t, cs, "verse_task", map[string]any{"if": "{status: done}", "trim": "{id}"}); !strings.Contains(out, id) {
		t.Fatalf("the listing filtered to the done tasks:\n%s", out)
	}
}

// A HOST MAY SHOW THE AGENT ONLY THE STRUCTURED ANSWER, as Claude Code does for a tool with an
// output schema, so the frames are in it as well as in the text.
func TestTheTranscriptIsInTheStructuredAnswer(t *testing.T) {
	serve(t)
	cs := mcpSession(t)
	id, _ := callTool(t, cs, "verse_task_run", scriptRun("echo hi"))
	transcript(t, cs, id, "")
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "verse_task_events", Arguments: map[string]any{"id": id, "timeout": 5}})
	if err != nil || res.IsError {
		t.Fatalf("verse_task_events: %v %v", err, res)
	}
	var got struct {
		Frames []string `json:"frames"`
		Cursor string   `json:"cursor"`
		Ended  bool     `json:"ended"`
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if want := text[:strings.LastIndex(text, "\n")]; strings.Join(got.Frames, "\n") != want || !got.Ended {
		t.Fatalf("the structured answer %s, beside the text:\n%s", raw, text)
	}
	if all := strings.Join(got.Frames, "\n"); !strings.Contains(all, "kind: exec") {
		t.Fatalf("the structured frames: %q", got.Frames)
	}
}

// A TRANSCRIPT LONGER THAN AN ANSWER is answered in parts, each ending at a frame, and the parts
// read from their cursors are the whole of it, nothing twice.
func TestATranscriptIsReadInPartsFromItsCursor(t *testing.T) {
	serve(t)
	cs := mcpSession(t)
	id, _ := callTool(t, cs, "verse_task_run", scriptRun("echo one; echo two; echo three"))
	whole, _ := transcript(t, cs, id, "")
	was := mcpAnswerMax
	mcpAnswerMax = 1<<10 + 150 // room for a frame or so a call
	t.Cleanup(func() { mcpAnswerMax = was })
	parts, calls := transcript(t, cs, id, "")
	if calls < 3 || strings.Join(parts, "\n") != strings.Join(whole, "\n") {
		t.Fatalf("in %d calls:\n%s\nwhole:\n%s", calls, strings.Join(parts, "\n"), strings.Join(whole, "\n"))
	}
}

// ONE TASK, HELD, LET GO AND CANCELLED through the flow tools, as `verse flow <verb> --task` does.
func TestTheFlowToolsHoldAndCancelOneTask(t *testing.T) {
	serve(t)
	cs := mcpSession(t)
	id, _ := callTool(t, cs, "verse_task_run", scriptRun("sleep 30"))
	t.Cleanup(func() { runCLI(t, "flow", "cancel", "--task", id) })
	pollTool(t, cs, "verse_task", map[string]any{"id": id, "trim": "{status}"}, "status: running")

	if out, isErr := callTool(t, cs, "verse_flow_suspend", map[string]any{"task": id, "why": "a look"}); isErr || !strings.Contains(out, "heldBy") {
		t.Fatalf("suspend: error=%v\n%s", isErr, out)
	}
	pollTool(t, cs, "verse_task", map[string]any{"id": id, "trim": "{status}"}, "status: paused")
	if out, isErr := callTool(t, cs, "verse_flow_resume", map[string]any{"task": id}); isErr {
		t.Fatalf("resume: %s", out)
	}
	pollTool(t, cs, "verse_task", map[string]any{"id": id, "trim": "{status}"}, "status: running")
	if out, isErr := callTool(t, cs, "verse_flow_cancel", map[string]any{"task": id}); isErr || !strings.Contains(out, "affected: 1") {
		t.Fatalf("cancel: error=%v\n%s", isErr, out)
	}
	frames, _ := transcript(t, cs, id, "")
	if all := strings.Join(frames, "\n"); !strings.Contains(all, "text: cancelled") {
		t.Fatalf("the transcript does not end cancelled:\n%s", all)
	}
}

// SAY IS THE CLI'S: words to a script, which has no turn to take them, are refused in the words
// `verse flow say` is refused in.
func TestTheFlowSayToolSaysWhatTheCLISays(t *testing.T) {
	serve(t)
	cs := mcpSession(t)
	id, _ := callTool(t, cs, "verse_task_run", scriptRun("sleep 30"))
	t.Cleanup(func() { runCLI(t, "flow", "cancel", "--task", id) })
	pollTool(t, cs, "verse_task", map[string]any{"id": id, "trim": "{status}"}, "status: running")
	_, cliErr := runCLI(t, "flow", "say", "--task", id, "hurry")
	out, isErr := callTool(t, cs, "verse_flow_say", map[string]any{"task": id, "text": "hurry"})
	if cliErr == nil || !isErr || out != cliErr.Error() {
		t.Fatalf("the tool said %q (error=%v), the CLI %v", out, isErr, cliErr)
	}
}

// scriptRun is verse_task_run's arguments for a spawn of script, with the network a runner here
// builds.
func scriptRun(script string) map[string]any {
	return map[string]any{"spawn": fmt.Sprintf("{script: %q, environment: {systems: {network: {mode: open}}}}", script)}
}

// AN AUTONOMY CHANGE through the tool is taken, as `verse task autonomy` takes it.
func TestTheTaskAutonomyToolReplacesTheAutonomy(t *testing.T) {
	serveKeyless(t)
	cs := mcpSession(t)
	id, _ := callTool(t, cs, "verse_task_run", scriptRun("sleep 30"))
	t.Cleanup(func() { runCLI(t, "flow", "cancel", "--task", id) })
	if out, isErr := callTool(t, cs, "verse_task_autonomy", map[string]any{"id": id, "autonomy": "[{then: ask}]"}); isErr || !strings.HasPrefix(out, "task autonomy "+id) {
		t.Fatalf("task autonomy: error=%v %q", isErr, out)
	}
	pollTool(t, cs, "verse_task", map[string]any{"id": id}, "then: ask")
}

// A SPAWN MEANS WHAT IT SAYS, as `verse task run -f` reads it: a key it does not have is refused
// before anything is sent, and a prompt whose environment gives no system is told so, beside
// whatever the run answers.
func TestTheTaskRunToolReadsTheSpawnAsTheCommandDoes(t *testing.T) {
	serve(t)
	cs := mcpSession(t)
	if out, isErr := callTool(t, cs, "verse_task_run", map[string]any{"spawn": `{script: "echo hi", networks: open}`}); !isErr || !strings.Contains(out, "task run: `spawn` is not a spawn") {
		t.Fatalf("an unknown key: error=%v %q", isErr, out)
	}
	if out, isErr := callTool(t, cs, "verse_task_run", map[string]any{"spawn": "  "}); !isErr || !strings.Contains(out, "takes the spawn") {
		t.Fatalf("no spawn: error=%v %q", isErr, out)
	}
	if out, isErr := callTool(t, cs, "verse_task_run", map[string]any{"spawn": `{prompt: hi, runner: nosuch}`}); !isErr || !strings.Contains(out, `unknown field "runner"`) {
		t.Fatalf("a spawn naming a runner: error=%v %q", isErr, out)
	}
	for _, spawn := range []string{"{prompt: hi}"} {
		if out, _ := callTool(t, cs, "verse_task_run", map[string]any{"spawn": spawn}); !strings.Contains(out, "gives no system") {
			t.Fatalf("%s:\n%s", spawn, out)
		}
	}
}

// fakeVerse is a verse that answers as h does, for what a real one does only when something goes
// wrong; the next session is toward it.
func fakeVerse(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	testAddr = strings.TrimPrefix(ts.URL, "http://")
}

// sseFrames answers the task's events route with frames, and its record with status.
func sseFrames(status string, frames ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/events") {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, f := range frames {
				fmt.Fprintf(w, "data: %s\n\n", f)
			}
			return
		}
		fmt.Fprintf(w, `{"id": "t1", "status": %q}`, status)
	}
}

// A STREAM THAT CLOSES ON A LIVE TASK is not its end: the record says it runs, so the answer says
// to call again, and only a record that says it has ended ends it.
func TestATranscriptStreamClosingIsNotTheTasksEnd(t *testing.T) {
	frame := `{"kind": "say", "task": "t1", "cursor": 4, "text": "working"}`
	fakeVerse(t, sseFrames("running", frame))
	out, isErr := callTool(t, mcpSession(t), "verse_task_events", map[string]any{"id": "t1", "timeout": 1})
	if isErr || strings.Contains(out, "has ended") || !strings.Contains(out, "call again") || !strings.HasSuffix(out, "cursor: 4") {
		t.Fatalf("error=%v\n%s", isErr, out)
	}
	fakeVerse(t, sseFrames("done"))
	out, isErr = callTool(t, mcpSession(t), "verse_task_events", map[string]any{"id": "t1", "since": "9", "timeout": 1})
	if isErr || !strings.HasSuffix(out, "cursor: 9; the task has ended") {
		t.Fatalf("past the end of an ended task: error=%v\n%s", isErr, out)
	}
}

// A GAP MOVES NO CURSOR: it carries the newest, and the frames replayed after it are older. A part
// that holds it holds the frame after it too, however long, and its cursor is that frame's.
func TestATranscriptPartAfterAGapGoesOnFromTheReplay(t *testing.T) {
	gap := `{"kind": "gap", "task": "t1", "cursor": 900}`
	long := fmt.Sprintf(`{"kind": "say", "task": "t1", "cursor": 100, "text": %q}`, strings.Repeat("x", 600))
	next := `{"kind": "say", "task": "t1", "cursor": 101, "text": "after"}`
	fakeVerse(t, sseFrames("running", gap, long, next))
	was := mcpAnswerMax
	mcpAnswerMax = 1<<10 + 100
	t.Cleanup(func() { mcpAnswerMax = was })
	out, _ := callTool(t, mcpSession(t), "verse_task_events", map[string]any{"id": "t1", "since": "5", "timeout": 1})
	if !strings.Contains(out, "xxxx") || strings.Contains(out, "after") || !strings.HasSuffix(out, "cursor: 100") {
		t.Fatalf("the first part:\n%s", out)
	}
}

// AN AUTONOMY IS CHECKED FIRST: one `task autonomy` refuses is refused before the identity that
// would send it is asked about, by the command and by the tool, in the same words.
func TestAnAutonomyIsCheckedBeforeWhoSendsIt(t *testing.T) {
	rows := "[{then: ask}, {then: allow}]"
	_, cliErr := runCLI(t, "task", "autonomy", "t1", rows, "--as", "!!")
	if cliErr == nil || !strings.Contains(cliErr.Error(), "matches every call") {
		t.Fatalf("the command: %v", cliErr)
	}
	out, isErr := callTool(t, mcpSession(t), "verse_task_autonomy", map[string]any{"id": "t1", "autonomy": rows})
	if !isErr || out != cliErr.Error() {
		t.Fatalf("the tool said %q (error=%v), the command %v", out, isErr, cliErr)
	}
}

// AN EMPTY AUTONOMY IS NOT AN AUTONOMY: refused, as `verse task autonomy` refuses no rows.
func TestTheTaskAutonomyToolRefusesAnEmptyAutonomy(t *testing.T) {
	for _, a := range []string{"", "  "} {
		out, isErr := callTool(t, mcpSession(t), "verse_task_autonomy", map[string]any{"id": "t1", "autonomy": a})
		if !isErr || !strings.Contains(out, "wants the whole autonomy") {
			t.Fatalf("autonomy %q: error=%v %q", a, isErr, out)
		}
	}
}

// A PART NEVER ENDS AT A GAP: one met mid-stream, with no room for the frame after it, is left to
// the next call, which meets it again with that frame.
func TestATranscriptPartDoesNotEndAtAGap(t *testing.T) {
	first := `{"kind": "say", "task": "t1", "cursor": 1, "text": "first"}`
	gap := `{"kind": "gap", "task": "t1", "dropped": 3}`
	long := fmt.Sprintf(`{"kind": "say", "task": "t1", "cursor": 5, "text": %q}`, strings.Repeat("x", 600))
	fakeVerse(t, sseFrames("running", first, gap, long))
	was := mcpAnswerMax
	mcpAnswerMax = 1<<10 + 100
	t.Cleanup(func() { mcpAnswerMax = was })
	out, _ := callTool(t, mcpSession(t), "verse_task_events", map[string]any{"id": "t1", "timeout": 1})
	if lines := strings.Split(out, "\n"); len(lines) != 2 || !strings.Contains(lines[0], "first") || lines[1] != "cursor: 1" {
		t.Fatalf("the part:\n%s", out)
	}
}

// A FRAME TOO LONG TO READ is said, rather than read as the stream closing: every call from
// before it would stop at it again.
func TestATranscriptFrameTooLongIsSaid(t *testing.T) {
	huge := fmt.Sprintf(`{"kind": "say", "task": "t1", "cursor": 2, "text": %q}`, strings.Repeat("x", frameMax))
	fakeVerse(t, sseFrames("running", `{"kind": "say", "task": "t1", "cursor": 1, "text": "ok"}`, huge))
	out, isErr := callTool(t, mcpSession(t), "verse_task_events", map[string]any{"id": "t1", "timeout": 1})
	if !isErr || !strings.Contains(out, "longer than") {
		t.Fatalf("error=%v %q", isErr, out)
	}
}

// A TOOL ON ONE TASK WANTS ONE: no id, or one with a slash, which would carry the request to
// another route, is refused before anything is sent.
func TestTheTaskToolsRefuseAnIdNoTaskHas(t *testing.T) {
	cs := mcpSession(t)
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"verse_task_events", map[string]any{"id": ""}},
		{"verse_task", map[string]any{"id": "x/../../asks"}},
		{"verse_task_autonomy", map[string]any{"id": "", "autonomy": "[{then: ask}]"}},
		{"verse_flow_cancel", map[string]any{"task": "x/../../asks"}},
	} {
		if out, isErr := callTool(t, cs, c.tool, c.args); !isErr || !strings.Contains(out, "no task's id") {
			t.Errorf("%s %v: error=%v %q", c.tool, c.args, isErr, out)
		}
	}
}
