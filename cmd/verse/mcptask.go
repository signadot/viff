package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/task"
)

// The task tools mirror `verse task`: the tasks in the runtime, one task's transcript, running
// one, and replacing a running task's autonomy. Controlling one task — holding it, saying something
// to it, letting it go on, cancelling it — is `verse flow`'s, by --task, and so are those tools.

type taskIn struct {
	ID     string `json:"id,omitempty" jsonschema:"one task by its id, as it stands; without it, the tasks in the runtime"`
	Runner string `json:"runner,omitempty" jsonschema:"only the tasks running on this runner (verse.runners.<name>, or the bare name)"`
	If     string `json:"if,omitempty" jsonschema:"keep only the tasks matching this tony pattern: {status: running}"`
	Trim   string `json:"trim,omitempty" jsonschema:"write only the parts of each task this match document names, as a key set: {id status}"`
}

type taskEventsIn struct {
	ID      string `json:"id" jsonschema:"the task's id"`
	Since   string `json:"since,omitempty" jsonschema:"the cursor a previous verse_task_events answered: the frames after it. Without it, the transcript from its start, as far as the task still keeps it"`
	Timeout int    `json:"timeout,omitempty" jsonschema:"seconds to wait for a frame when there is none after since, before answering with nothing; 300 by default, at most 3600"`
}

type taskEventsOutput struct {
	Frames []string `json:"frames" jsonschema:"the frames after since, in order, as verse task attach prints them; empty at the timeout"`
	Cursor string   `json:"cursor" jsonschema:"pass as since to the next verse_task_events"`
	Ended  bool     `json:"ended" jsonschema:"the task has ended: nothing will follow"`
	Closed bool     `json:"closed,omitempty" jsonschema:"the stream closed before the task ended: call again from the cursor"`
}

type taskRunIn struct {
	Spawn string `json:"spawn" jsonschema:"the spawn to run, as tony, as a charter writes one (prompt or script, agent, environment, autonomy, answer, runner, subject, …). It means only what it says: an environment that gives no system gives the agent no tool, and one that names no network has none"`
	Name  string `json:"name,omitempty" jsonschema:"a label for the run; the id is derived from it and answered"`
}

type taskAutonomyIn struct {
	ID       string `json:"id" jsonschema:"the running task's id"`
	Autonomy string `json:"autonomy" jsonschema:"the whole new autonomy, as a tony list of rows; it decides only within the task's environment"`
}

type flowTaskIn struct {
	Task string `json:"task" jsonschema:"the task, by its id"`
}

// flowSelection is what a flow tool acts on, as `verse flow`'s flags give it: one task, or a
// charter and a subject, the one left out being every one.
type flowSelection struct {
	Task      string `json:"task,omitempty" jsonschema:"one task, by its id; combines with neither charter nor subject"`
	Charter   string `json:"charter,omitempty" jsonschema:"<address>[:<rule>]: an installed charter, or one rule of it; :<rule> is a baseline rule, * every charter"`
	Subject   string `json:"subject,omitempty" jsonschema:"an address, with * open in any segment: the work about it"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"the charter at charter and every charter beneath it"`
}

func (s flowSelection) flags() flowFlags {
	return flowFlags{Task: s.Task, Charter: s.Charter, Subject: s.Subject, Recursive: s.Recursive}
}

type flowSuspendIn struct {
	flowSelection
	Why string `json:"why,omitempty" jsonschema:"what the hold is for; a suspension of a charter or a subject must say"`
}

type flowSayIn struct {
	flowSelection
	Text string `json:"text" jsonschema:"what to say; every live task the selection names hears it at its next boundary"`
}

func (m *mcpServer) addTaskTools(s *mcp.Server) {
	addTool(m, s, &mcp.Tool{
		Name:        "verse_task",
		Description: "The tasks in the runtime, as `verse task` lists them, or with id one task as it stands: its status, its result, why it failed.",
		Annotations: readOnly(),
	}, m.task)
	addTool(m, s, &mcp.Tool{
		Name: "verse_task_events",
		Description: "One task's transcript, as `verse task attach` prints it: its words, its actions, its " +
			"questions and its status, frame by frame. Answers the frames after since at once, or waits for " +
			"the next up to the timeout, and always with a cursor to pass back as since.",
		Annotations: readOnly(),
	}, m.taskEvents)
	addTool(m, s, &mcp.Tool{
		Name: "verse_task_run",
		Description: "Run a task, as you, as `verse task run -f` does: a spawn, an agent given a prompt or a " +
			"script, with the environment it works in and the autonomy that decides its steps. The " +
			"environment is one the runner builds: verse_data at verse.runners.<name>, under described, " +
			"says what each offers. Answers the task's id; follow it with verse_task_events.",
		Annotations: writes(false),
	}, m.taskRun)
	addTool(m, s, &mcp.Tool{
		Name: "verse_task_autonomy",
		Description: "Replace a running task's autonomy, as `verse task autonomy` does. The new autonomy decides " +
			"its later steps, approves nothing already waiting, and decides only within the task's environment.",
		Annotations: writes(false),
	}, m.taskAutonomy)

	addTool(m, s, &mcp.Tool{
		Name: "verse_flow_suspend",
		Description: "Hold one task at its next boundary, or suspend a charter's work about a subject, until " +
			"it is resumed, as `verse flow suspend` does. A suspension holds the live tasks it names and " +
			"every firing it would start; of charter and subject, the one left out is every one.",
		Annotations: writes(false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in flowSuspendIn) (*mcp.CallToolResult, any, error) {
		return m.flowVerb(ctx, "suspend", in.flags(), api.FlowSelect{Why: in.Why})
	})
	addTool(m, s, &mcp.Tool{
		Name: "verse_flow_resume",
		Description: "Let a held task go on, or lift the suspension the same charter and subject made, as " +
			"`verse flow resume` does.",
		Annotations: writes(false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in flowSelection) (*mcp.CallToolResult, any, error) {
		return m.flowVerb(ctx, "resume", in.flags(), api.FlowSelect{})
	})
	addTool(m, s, &mcp.Tool{
		Name: "verse_flow_say",
		Description: "Say something, as you, to every live task a selection names, as `verse flow say` does: " +
			"each hears it at its next boundary.",
		Annotations: writes(false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in flowSayIn) (*mcp.CallToolResult, any, error) {
		return m.flowVerb(ctx, "say", in.flags(), api.FlowSelect{Text: in.Text, By: m.me})
	})
	addTool(m, s, &mcp.Tool{
		Name: "verse_flow_cancel",
		Description: "Cancel one task, as `verse flow cancel --task` does. It does not come back; a charter's or " +
			"a subject's work is suspended, then its tasks cancelled one by one.",
		Annotations: writes(true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in flowTaskIn) (*mcp.CallToolResult, any, error) {
		return m.flowVerb(ctx, "cancel", flowFlags{Task: in.Task}, api.FlowSelect{})
	})
}

func (m *mcpServer) task(ctx context.Context, _ *mcp.CallToolRequest, in taskIn) (*mcp.CallToolResult, any, error) {
	if in.ID != "" && in.Runner != "" {
		return nil, nil, fmt.Errorf("runner narrows the listing, and takes no id")
	}
	if in.ID != "" {
		if err := named(in.ID); err != nil {
			return nil, nil, err
		}
	}
	f, err := filtersOf(in.If, in.Trim)
	if err != nil {
		return nil, nil, err
	}
	path, q := "/tasks", url.Values{}
	if in.ID != "" {
		path = "/tasks/" + in.ID
	} else if in.Runner != "" {
		q.Set("runner", in.Runner)
	}
	return m.get(ctx, "task", path, q, f)
}

// eventsQuiet is how long a transcript read waits after a frame for the next, before it answers
// what it has. A var so a test can say.
var eventsQuiet = 300 * time.Millisecond

func (m *mcpServer) taskEvents(ctx context.Context, _ *mcp.CallToolRequest, in taskEventsIn) (*mcp.CallToolResult, taskEventsOutput, error) {
	if err := named(in.ID); err != nil {
		return nil, taskEventsOutput{}, err
	}
	since := in.Since
	if since == "" {
		since = "0"
	}
	if _, err := strconv.ParseInt(since, 10, 64); err != nil {
		return nil, taskEventsOutput{}, fmt.Errorf("since %q is not a cursor verse_task_events answered", in.Since)
	}
	timeout := waitDefault
	if in.Timeout > 0 {
		timeout = min(time.Duration(in.Timeout)*time.Second, waitMax)
	}
	// The stream is this call's: it ends when the call answers, and what it has not read is
	// replayed to the next call from the cursor.
	stream, stop := context.WithCancel(ctx)
	resp, err := m.c.With(stream).Get("/tasks/"+in.ID+"/events", url.Values{"since": {since}})
	if err != nil {
		stop()
		return nil, taskEventsOutput{}, fmt.Errorf("task events: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		resp.Body.Close()
		stop()
		if why := recordedFailureVia(m.c.With(ctx), in.ID); why != "" {
			return nil, taskEventsOutput{}, fmt.Errorf("task %s: %s", in.ID, why)
		}
		return nil, taskEventsOutput{}, fmt.Errorf("task events %s: %s", in.ID, strings.TrimSpace(string(body)))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		resp.Body.Close()
		stop()
		return nil, taskEventsOutput{}, fmt.Errorf("task events %s: not an event stream (Content-Type %q)", in.ID, ct)
	}
	frames, short := scanFrames(resp.Body)
	defer func() {
		// Ending the request ends the body, which ends the scan; what it had read is drained so
		// it is not left sending to nobody.
		stop()
		resp.Body.Close()
		go func() {
			for range frames {
			}
		}()
	}()
	in2 := streamFmt(resp)
	budget := mcpAnswerMax - 1<<10
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var quiet <-chan time.Time
	out := taskEventsOutput{Frames: []string{}, Cursor: since}
	size, kept := 0, 0 // the length of the frames answered, and how many of them are not gaps
	lastGap := false   // the last frame answered is a gap with no frame after it yet
read:
	for {
		select {
		case data, ok := <-frames:
			// A STREAM THAT CLOSES has said all it will, which is not that the task has ended: a
			// verse restarting or a proxy timing out closes it too. The task's record says which.
			if !ok {
				if err := short(); err != nil {
					return nil, taskEventsOutput{}, fmt.Errorf("task events %s, after cursor %s: %w", in.ID, out.Cursor, err)
				}
				out.Ended = taskEnded(m.c.With(ctx), in.ID)
				out.Closed = !out.Ended
				break read
			}
			line := renderFrame(data, in2, m.out, false, true)
			var ev task.Event
			_ = codec.Unmarshal([]byte(data), &ev, in2)
			// A GAP IS NOT A PLACE IN THE TRANSCRIPT: it carries the newest cursor, and the frames
			// replayed after it are older. It moves no cursor, and is answered with the frame
			// after it, so a part never ends at one.
			gap := ev.Kind == task.EventGap
			// A transcript longer than an answer is answered in parts, each ending at a frame:
			// the next call goes on from the cursor.
			if kept > 0 && !gap && size+len(line)+1 > budget {
				break read
			}
			lastGap = gap
			out.Frames = append(out.Frames, line)
			size += len(line) + 1
			if gap {
				continue
			}
			kept++
			if ev.Cursor > 0 {
				out.Cursor = strconv.FormatInt(ev.Cursor, 10)
			}
			if ev.Kind == task.EventStatus && isTerminalStatus(ev.Text) {
				out.Ended = true
				break read
			}
			quiet = time.After(eventsQuiet)
		case <-quiet:
			break read
		case <-deadline.C:
			break read
		case <-ctx.Done():
			return nil, taskEventsOutput{}, ctx.Err()
		}
	}
	// A part that would end at a gap ends before it: the next call, from the same cursor, is
	// answered the gap again, with the frames after it.
	if lastGap && !out.Closed && !out.Ended {
		out.Frames = out.Frames[:len(out.Frames)-1]
	}
	var buf strings.Builder
	for _, f := range out.Frames {
		fmt.Fprintln(&buf, f)
	}
	switch {
	case out.Closed:
		fmt.Fprintln(&buf, "the stream closed before the task ended; call again from the cursor")
	case len(out.Frames) == 0 && !out.Ended:
		fmt.Fprintf(&buf, "nothing in %s\n", timeout)
	}
	fmt.Fprintf(&buf, "cursor: %s", out.Cursor)
	if out.Ended {
		buf.WriteString("; the task has ended")
	}
	return sentence(buf.String()), out, nil
}

// named refuses a tool on one task that was given no task, or an id no task has: one with a
// slash, which would carry the request to another route.
func named(id string) error {
	if id == "" || strings.Contains(id, "/") {
		return fmt.Errorf("%q is no task's id: pass one as verse_task_run answered it or verse_task lists it", id)
	}
	return nil
}

// taskEnded is whether the task's record says it has ended. A record not read says it has not, and
// the next call from the cursor asks again.
func taskEnded(c *client.Client, id string) bool {
	var snap api.Task
	return taskSnapshotVia(c, id, &snap) == nil && snap.Status.Terminal()
}

func (m *mcpServer) taskRun(ctx context.Context, _ *mcp.CallToolRequest, in taskRunIn) (*mcp.CallToolResult, any, error) {
	if err := m.nobody(); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(in.Spawn) == "" {
		return nil, nil, fmt.Errorf("task run takes the spawn to run")
	}
	spec, err := readSpawn([]byte(in.Spawn), mcpIn, "`spawn`")
	if err != nil {
		return nil, nil, err
	}
	// Said with the run's answer whatever it is, as the command says it before the run.
	note := noSystem(spec)
	id, err := spawnVia(m.c.With(ctx), api.Spawn{Spawn: spec, Name: in.Name, Principal: m.me})
	if err != nil {
		if note != "" {
			err = fmt.Errorf("%w\n%s", err, note)
		}
		return nil, nil, err
	}
	res := sentence(id)
	// Apart from the id, so the first block is still only the id.
	if note != "" {
		res.Content = append(res.Content, &mcp.TextContent{Text: note})
	}
	return res, nil, nil
}

func (m *mcpServer) taskAutonomy(ctx context.Context, _ *mcp.CallToolRequest, in taskAutonomyIn) (*mcp.CallToolResult, any, error) {
	if err := m.nobody(); err != nil {
		return nil, nil, err
	}
	if err := named(in.ID); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(in.Autonomy) == "" {
		return nil, nil, fmt.Errorf("task autonomy wants the whole autonomy: its rows")
	}
	body := api.SetAutonomy{By: m.me}
	if err := codec.UnmarshalStrict([]byte(in.Autonomy), &body.Autonomy, mcpIn); err != nil {
		return nil, nil, fmt.Errorf("%q is not an autonomy, a list of rows: %w", in.Autonomy, err)
	}
	if err := checkAutonomy(body.Autonomy); err != nil {
		return nil, nil, err
	}
	said, err := setAutonomyVia(m.c.With(ctx), in.ID, body)
	if err != nil {
		return nil, nil, err
	}
	return sentence(said), nil, nil
}

// toolSelection refuses what the server refuses of a selection, before it is sent and in the
// tool's words, since the server's name the command's flags: a task beside anything else, and
// recursive without a charter.
//
// Between the two, a verb's usage, as the command says it (unnarrowed), so a selection that names
// nothing is told what it may name rather than what recursive wants. The listing has no verb.
func toolSelection(sel flowFlags, verb string) error {
	if sel.Task != "" && (sel.Charter != "" || sel.Subject != "" || sel.Recursive) {
		return fmt.Errorf("task names one run, and combines with neither charter, subject nor recursive")
	}
	if verb != "" {
		if want := sel.unnarrowed(verb, func(name string) string { return name }); want != "" {
			return errors.New(want)
		}
	}
	if sel.Recursive && sel.Charter == "" {
		return fmt.Errorf("recursive takes the charter at charter and every charter beneath it, so it wants charter")
	}
	return nil
}

// flowVerb is `verse flow <verb>` on a selection, with what the verb adds.
func (m *mcpServer) flowVerb(ctx context.Context, verb string, sel flowFlags, add api.FlowSelect) (*mcp.CallToolResult, any, error) {
	if err := m.nobody(); err != nil {
		return nil, nil, err
	}
	if err := toolSelection(sel, verb); err != nil {
		return nil, nil, fmt.Errorf("flow %s: %w", verb, err)
	}
	if sel.Task != "" {
		if err := named(sel.Task); err != nil {
			return nil, nil, err
		}
	}
	// What the verb adds, after the selection, as the command asks for it.
	switch {
	case verb == "suspend" && sel.Task == "" && add.Why == "":
		return nil, nil, fmt.Errorf("flow suspend: a suspension says what it is for: give why")
	case verb == "say" && add.Text == "":
		return nil, nil, fmt.Errorf("flow say: say wants the text to say")
	}
	body := sel.body()
	body.Why, body.Text, body.By = add.Why, add.Text, add.By
	resp, err := postFlowVia(m.c.With(ctx), verb, body)
	if err != nil {
		return nil, nil, err
	}
	var buf bytes.Buffer
	if err := renderBody(&buf, resp, filters{}, m.out, false); err != nil {
		return nil, nil, err
	}
	return answer(&buf)
}
