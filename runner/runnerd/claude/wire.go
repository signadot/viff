package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The wire to a headless Claude Code, `claude -p --input-format stream-json --output-format
// stream-json --permission-prompt-tool stdio`: one JSON object per line, over the process's
// stdin and stdout. This file is the whole of what knows that format. It starts the process,
// frames the messages, and hands the driver (claude.go) typed Go values: what Claude asked, what
// Claude said, and what Claude answered. encoding/json stops here, which is why this file and no
// other in the runner is on the allowlist in scripts/check-json.sh.
//
// Three kinds of message cross it, and the driver treats them differently:
//
//   - a CONTROL REQUEST this side makes (`initialize`, `interrupt`), answered by request id
//     with a control response;
//   - a CONTROL REQUEST Claude makes of this side (`can_use_tool`, before a tool call its
//     permission mode does not already allow; `mcp_message`, a JSON-RPC message for the MCP
//     server this side declared), which the driver answers after verse has decided;
//   - a MESSAGE of the session (`system`, `assistant`, `user`, `result`, `rate_limit_event`),
//     which the driver narrates onto the read plane and reads the turn's end from.
//
// Claude's requests and messages arrive in order on one channel, so the driver sees them in
// the order Claude emitted them.

// event is one message Claude sent.
type event struct {
	Type    string // control_request | system | assistant | user | result | rate_limit_event | …
	Subtype string // the message's own subtype, where it has one (system, result)
	// RequestID and Req are set for a control_request.
	RequestID string
	Req       controlRequest
	// Content is an assistant or user message's blocks.
	Content []block
	// Replay is set on a user message this side sent, echoed back when Claude takes it up
	// (`--replay-user-messages`): into the running turn at its next step, or as the start of
	// a turn of its own once the running one has ended.
	Replay bool
	// Result is the result message's fields.
	Result result
	// ParentToolUseID is set on a message a sub-agent produced; this runner offers none, so
	// it is read for nothing but completeness.
	ParentToolUseID string
}

// isRequest reports whether Claude expects an answer to this event.
func (e event) isRequest() bool { return e.Type == "control_request" }

// controlRequest is what Claude asks of this side.
type controlRequest struct {
	Subtype string `json:"subtype"` // can_use_tool | mcp_message | hook_callback | …
	// can_use_tool
	ToolName    string         `json:"tool_name"`
	Input       map[string]any `json:"input"`
	ToolUseID   string         `json:"tool_use_id"`
	BlockedPath string         `json:"blocked_path"`
	// mcp_message
	ServerName string          `json:"server_name"`
	Message    json.RawMessage `json:"message"`
	// Unread is why the request did not decode; the fields above are then only those a
	// refusal needs.
	Unread string `json:"-"`
}

// mcpMessage is the JSON-RPC message inside an `mcp_message` control request.
type mcpMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// mcpCall is `tools/call`'s params: the tool, its arguments, and the tool use they are for.
type mcpCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	Meta      struct {
		ToolUseID string `json:"claudecode/toolUseId"`
	} `json:"_meta"`
}

// block is one content block of an assistant or user message, in the fields this driver reads.
type block struct {
	Type string `json:"type"` // text | thinking | tool_use | tool_result
	Text string `json:"text"`
	// tool_use
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
	// tool_result
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // a string, or blocks
	IsError   bool            `json:"is_error"`
}

// text is a tool result's content as text, whichever shape it came in.
func (b block) text() string {
	if len(b.Content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(b.Content, &s) == nil {
		return s
	}
	var blocks []block
	if json.Unmarshal(b.Content, &blocks) == nil {
		var parts []string
		for _, bl := range blocks {
			if bl.Text != "" {
				parts = append(parts, bl.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(b.Content)
}

// result is the `result` message: the turn is over, and this is how.
type result struct {
	Subtype    string  // success | error_during_execution | error_max_turns | …
	IsError    bool    `json:"is_error"`
	Result     string  `json:"result"`
	StopReason string  `json:"stop_reason"`
	CostUSD    float64 `json:"total_cost_usd"`
}

// wireMessage is every shape a line can take, decoded loosely.
type wireMessage struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	RequestID       string          `json:"request_id"`
	Request         json.RawMessage `json:"request"`
	Response        json.RawMessage `json:"response"`
	Message         json.RawMessage `json:"message"`
	ParentToolUseID string          `json:"parent_tool_use_id"`
	IsReplay        bool            `json:"isReplay"`
	// result
	IsError    bool            `json:"is_error"`
	Result     json.RawMessage `json:"result"` // a string, or absent
	StopReason string          `json:"stop_reason"`
	CostUSD    float64         `json:"total_cost_usd"`
}

// controlResponse is Claude's answer to a control request this side made.
type controlResponse struct {
	Subtype   string          `json:"subtype"` // success | error
	RequestID string          `json:"request_id"`
	Error     string          `json:"error"`
	Response  json.RawMessage `json:"response"`
}

// rpc is one running Claude and the conversation with it.
type rpc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tail
	stray  *tail // what Claude printed on stdout that was not a message

	wmu sync.Mutex // one writer at a time on stdin
	seq atomic.Int64

	pmu     sync.Mutex
	pending map[string]chan controlResponse // answers to control requests this side made

	// Events is what Claude sent that this side did not ask for. Closed when the process's
	// stdout ends, after which Err says why.
	//
	// It is fed through an unbounded queue rather than straight from the reader, so a burst
	// of messages while this side is not reading them cannot stall the reader — which is also
	// the goroutine that delivers the answers this side waits on.
	Events <-chan event
	events chan event
	qmu    sync.Mutex
	queue  []event
	qwake  chan struct{}

	done chan struct{}
	err  error // why stdout ended, once it has
	// stop is closed by Close: the pump stops delivering, since nobody reads Events after it.
	stop chan struct{}

	// cancels is a channel per request Claude made and still waits on, closed when Claude
	// cancels it (control_cancel_request) or goes away; made as the request is read, and
	// forgotten once it is answered or this side lets go of one Claude took back (settled). A
	// cancel of a request no longer kept is about one already settled, and changes nothing.
	cmu     sync.Mutex
	cancels map[string]chan struct{}
	gone    bool // the stream has ended: every request still open is cancelled
}

// Cancelled is closed when Claude no longer waits on the request requestID: it cancelled it,
// or it went away.
func (r *rpc) Cancelled(requestID string) <-chan struct{} {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	return r.cancelCh(requestID)
}

// cancelCh is requestID's channel, made if it is new; closed already once the stream is over.
// r.cmu is held.
func (r *rpc) cancelCh(requestID string) chan struct{} {
	ch, ok := r.cancels[requestID]
	if !ok {
		ch = make(chan struct{})
		if r.gone {
			close(ch)
		}
		r.cancels[requestID] = ch
	}
	return ch
}

// request hands the pump a request Claude made, which is Claude's to cancel from here on.
func (r *rpc) request(e event) {
	r.Cancelled(e.RequestID)
	r.enqueue(e)
}

// cancelled records that Claude cancelled requestID.
func (r *rpc) cancelled(requestID string) {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	if ch, ok := r.cancels[requestID]; ok {
		closeOnce(ch)
	}
}

// cancelAll records that Claude went away: no request of its is waited on any more.
func (r *rpc) cancelAll() {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	r.gone = true
	for _, ch := range r.cancels {
		closeOnce(ch)
	}
}

// settled forgets requestID's channel: a request answered, or one Claude took back that this
// side has let go of, is no longer Claude's to cancel.
func (r *rpc) settled(requestID string) {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	delete(r.cancels, requestID)
}

func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// startProcess starts argv and begins reading it. The process is killed when Close is called,
// and when ctx ends.
func startProcess(ctx context.Context, argv []string, dir string, env []string) (*rpc, error) {
	if len(argv) == 0 {
		return nil, errors.New("claude: empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	// Stdout is read by this side as a stream, so Wait does not block on a pipe copy; stderr
	// is a bounded tail for the error message when the process dies.
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	tl := &tail{max: 4096}
	cmd.Stderr = tl
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude: starting %s: %w", argv[0], err)
	}
	r := &rpc{
		cmd: cmd, stdin: stdin, stderr: tl, stray: &tail{max: 4096},
		pending: map[string]chan controlResponse{},
		events:  make(chan event),
		done:    make(chan struct{}),
		stop:    make(chan struct{}),
		qwake:   make(chan struct{}, 1),
		cancels: map[string]chan struct{}{},
	}
	r.Events = r.events
	go r.pump()
	go r.read(stdout)
	return r, nil
}

// enqueue hands an event to the pump.
func (r *rpc) enqueue(e event) {
	r.qmu.Lock()
	r.queue = append(r.queue, e)
	r.qmu.Unlock()
	select {
	case r.qwake <- struct{}{}:
	default:
	}
}

// pump delivers queued events on Events, in order, and closes it once the reader is done and
// the queue is empty.
func (r *rpc) pump() {
	defer close(r.events)
	for {
		r.qmu.Lock()
		var next *event
		if len(r.queue) > 0 {
			e := r.queue[0]
			r.queue = r.queue[1:]
			next = &e
		}
		r.qmu.Unlock()
		if next != nil {
			select {
			case r.events <- *next:
			case <-r.stop:
				return
			}
			continue
		}
		select {
		case <-r.qwake:
		case <-r.stop:
			return
		case <-r.done:
			r.qmu.Lock()
			rest := r.queue
			r.queue = nil
			r.qmu.Unlock()
			for _, e := range rest {
				select {
				case r.events <- e:
				case <-r.stop:
					return
				}
			}
			return
		}
	}
}

// read is the one reader of stdout. It routes control responses to their callers and
// everything else onto Events, in order.
func (r *rpc) read(stdout io.Reader) {
	defer close(r.done)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024) // a file's content or a command's output can be large
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m wireMessage
		if err := json.Unmarshal(line, &m); err != nil {
			if e, ok := unreadRequest(line, err); ok {
				// A request whose line does not read as this side expects, in any field: still
				// a request Claude waits on an answer to.
				r.request(e)
				continue
			}
			// A line that is not a message (a warning, say) is not the end of the stream;
			// the messages go on after it.
			r.stray.Write(append(line, '\n'))
			continue
		}
		switch m.Type {
		case "control_response":
			var cr controlResponse
			if err := json.Unmarshal(m.Response, &cr); err != nil {
				// Its caller still hears an answer, rather than waiting on one forever.
				var loose struct {
					RequestID string `json:"request_id"`
				}
				_ = json.Unmarshal(m.Response, &loose)
				cr = controlResponse{Subtype: "error", RequestID: loose.RequestID, Error: "an answer this side cannot read: " + err.Error()}
			}
			r.pmu.Lock()
			ch, ok := r.pending[cr.RequestID]
			delete(r.pending, cr.RequestID)
			r.pmu.Unlock()
			if ok {
				ch <- cr
			}
		case "control_request":
			// Enqueued whatever its request decodes to: Claude waits on an answer to every
			// request it makes, and one this side cannot read is still one it must refuse.
			e := event{Type: m.Type, RequestID: m.RequestID}
			if err := json.Unmarshal(m.Request, &e.Req); err != nil {
				e.Req = unreadable(m.Request, err)
			}
			r.request(e)
		case "control_cancel_request":
			// Claude no longer waits on a request it made: the turn moved on without it.
			r.cancelled(m.RequestID)
		case "assistant", "user":
			e := event{Type: m.Type, ParentToolUseID: m.ParentToolUseID, Replay: m.IsReplay}
			var msg struct {
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(m.Message, &msg) == nil && len(msg.Content) > 0 {
				// A user message's content is a string where a person typed it, blocks where
				// it carries tool results; only the latter is read here. Block by block, so
				// one this side cannot read (an input that is no object) costs that block
				// alone and not the text beside it.
				var raws []json.RawMessage
				_ = json.Unmarshal(msg.Content, &raws)
				for _, raw := range raws {
					var b block
					if json.Unmarshal(raw, &b) != nil {
						var loose struct {
							Type string `json:"type"`
							ID   string `json:"id"`
							Name string `json:"name"`
						}
						_ = json.Unmarshal(raw, &loose)
						b = block{Type: loose.Type, ID: loose.ID, Name: loose.Name}
					}
					e.Content = append(e.Content, b)
				}
			}
			r.enqueue(e)
		case "result":
			e := event{Type: m.Type, Subtype: m.Subtype, Result: result{
				Subtype: m.Subtype, IsError: m.IsError, StopReason: m.StopReason, CostUSD: m.CostUSD}}
			_ = json.Unmarshal(m.Result, &e.Result.Result)
			r.enqueue(e)
		default:
			r.enqueue(event{Type: m.Type, Subtype: m.Subtype})
		}
	}
	if err := sc.Err(); err != nil {
		r.err = fmt.Errorf("claude: reading its output: %w", err)
	} else {
		r.err = errors.New("claude exited")
	}
	r.failPending()
	r.cancelAll()
}

// unreadRequest is a control request whose line did not decode as a message, as an event the
// driver refuses: its id, read as text whatever its JSON type, and why it did not read.
func unreadRequest(line []byte, err error) (event, bool) {
	var loose struct {
		Type      string          `json:"type"`
		RequestID json.RawMessage `json:"request_id"`
	}
	if json.Unmarshal(line, &loose) != nil || loose.Type != "control_request" || len(loose.RequestID) == 0 {
		return event{}, false
	}
	id := string(loose.RequestID)
	_ = json.Unmarshal(loose.RequestID, &id)
	var req struct {
		Request json.RawMessage `json:"request"`
	}
	_ = json.Unmarshal(line, &req)
	return event{Type: loose.Type, RequestID: id, Req: unreadable(req.Request, err)}, true
}

// unreadable is a request that did not decode, in what a refusal needs: which kind, and for
// which use, so the refusal is recorded against the use and its result is not narrated as a
// call that ran.
func unreadable(raw json.RawMessage, err error) controlRequest {
	var loose struct {
		Subtype   string `json:"subtype"`
		ToolName  string `json:"tool_name"`
		ToolUseID string `json:"tool_use_id"`
	}
	_ = json.Unmarshal(raw, &loose)
	return controlRequest{Subtype: loose.Subtype, ToolName: loose.ToolName, ToolUseID: loose.ToolUseID, Unread: err.Error()}
}

// failPending wakes every caller waiting on an answer that is now never coming.
func (r *rpc) failPending() {
	r.pmu.Lock()
	defer r.pmu.Unlock()
	for id, ch := range r.pending {
		ch <- controlResponse{Subtype: "error", Error: "claude went away before answering"}
		delete(r.pending, id)
	}
}

// Err is why the stream ended, with what the process said on stderr; "" while it runs.
func (r *rpc) Err() string {
	select {
	case <-r.done:
	default:
		return ""
	}
	msg := "claude exited"
	if r.err != nil {
		msg = r.err.Error()
	}
	if s := strings.TrimSpace(r.stderr.String()); s != "" {
		msg += ": " + lastLine(s)
	} else if s := strings.TrimSpace(r.stray.String()); s != "" {
		msg += ": " + lastLine(s)
	}
	return msg
}

// Control makes a control request and waits for its answer, or for ctx. params are the
// request's fields beside its subtype.
func (r *rpc) Control(ctx context.Context, subtype string, params map[string]any) (json.RawMessage, error) {
	id := "verse-" + strconv.FormatInt(r.seq.Add(1), 10)
	ch := make(chan controlResponse, 1)
	r.pmu.Lock()
	r.pending[id] = ch
	r.pmu.Unlock()
	req := map[string]any{"subtype": subtype}
	maps.Copy(req, params)
	if err := r.write(map[string]any{"type": "control_request", "request_id": id, "request": req}); err != nil {
		r.pmu.Lock()
		delete(r.pending, id)
		r.pmu.Unlock()
		return nil, fmt.Errorf("claude: %s: %w", subtype, err)
	}
	answer := func(rep controlResponse) (json.RawMessage, error) {
		if rep.Subtype == "error" {
			return nil, fmt.Errorf("claude: %s: %s", subtype, rep.Error)
		}
		return rep.Response, nil
	}
	select {
	case rep := <-ch:
		return answer(rep)
	case <-r.done:
		// The stream is over: an answer it carried is already here, and none is coming.
		select {
		case rep := <-ch:
			return answer(rep)
		default:
			r.pmu.Lock()
			delete(r.pending, id)
			r.pmu.Unlock()
			return nil, fmt.Errorf("claude: %s: claude went away before answering", subtype)
		}
	case <-ctx.Done():
		r.pmu.Lock()
		delete(r.pending, id)
		r.pmu.Unlock()
		return nil, ctx.Err()
	}
}

// SendUser puts a user message into the session: the next turn's prompt when the session is
// idle, and words the running turn takes up at its next step when it is not.
func (r *rpc) SendUser(text string) error {
	return r.write(map[string]any{
		"type":               "user",
		"message":            map[string]any{"role": "user", "content": text},
		"parent_tool_use_id": nil,
		"session_id":         "",
	})
}

// Allow answers a `can_use_tool` request yes, with the input the call runs with.
func (r *rpc) Allow(requestID string, input map[string]any) error {
	return r.respond(requestID, map[string]any{"behavior": "allow", "updatedInput": input})
}

// Deny answers a `can_use_tool` request no, with why, which the model reads as the call's result.
func (r *rpc) Deny(requestID, why string) error {
	return r.respond(requestID, map[string]any{"behavior": "deny", "message": why})
}

// MCPRespond answers an `mcp_message` request that carried the JSON-RPC request m: result is
// what the MCP server says. A notification is acknowledged with an empty result and no id, as
// the Agent SDKs acknowledge one.
func (r *rpc) MCPRespond(requestID string, m mcpMessage, result any) error {
	if m.isNotification() {
		return r.respond(requestID, map[string]any{"mcp_response": map[string]any{"jsonrpc": "2.0", "result": map[string]any{}}})
	}
	return r.respond(requestID, map[string]any{"mcp_response": map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result}})
}

// MCPError refuses an `mcp_message` request's JSON-RPC request.
func (r *rpc) MCPError(requestID string, m mcpMessage, code int, msg string) error {
	return r.respond(requestID, map[string]any{"mcp_response": map[string]any{"jsonrpc": "2.0", "id": m.ID,
		"error": map[string]any{"code": code, "message": msg}}})
}

// isNotification reports whether m expects no answer.
func (m mcpMessage) isNotification() bool { return len(m.ID) == 0 }

// protocolVersion is the version the client proposed on `initialize`, which this side speaks
// back: it uses nothing of the protocol a version could take away.
func (m mcpMessage) protocolVersion() string {
	var p struct {
		Version string `json:"protocolVersion"`
	}
	if json.Unmarshal(m.Params, &p) == nil && p.Version != "" {
		return p.Version
	}
	return "2025-06-18"
}

// settings is the `--settings` document the runner starts Claude with: an `ask` rule on each tool
// named, so Claude asks this side before every use of it. Without it, Claude runs the commands
// its own list calls read-only (`date`, `ls`, …) without a prompt, and a `commands` row would
// never see them; a rule that asks outranks that allowance.
func settings(ask []string) string {
	b, _ := json.Marshal(map[string]any{"permissions": map[string]any{"ask": ask}})
	return string(b)
}

// mcpConfig is the `--mcp-config` document declaring this side's server: an `sdk` server, which
// Claude reaches through the control protocol on the same pipe rather than a process or a URL.
func mcpConfig(server string) string {
	b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{server: map[string]any{"type": "sdk", "name": server}}})
	return string(b)
}

// respond answers a control request Claude made.
func (r *rpc) respond(requestID string, response map[string]any) error {
	r.settled(requestID)
	return r.write(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": requestID, "response": response}})
}

// respondError answers a control request Claude made with an error: one this side does not
// serve.
func (r *rpc) respondError(requestID, why string) error {
	r.settled(requestID)
	return r.write(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "error", "request_id": requestID, "error": why}})
}

func (r *rpc) write(msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	r.wmu.Lock()
	defer r.wmu.Unlock()
	_, err = r.stdin.Write(append(b, '\n'))
	return err
}

// Close ends the process: stdin closed, then a kill if it does not leave on its own. Wait comes
// before the reader is waited for: a process Claude started may still hold its stdout, and Wait
// closes this side of the pipe once Claude itself has exited (cmd.WaitDelay bounds the stderr
// copy, which such a process may hold too), so the reader sees its end whoever else kept the
// pipe open.
func (r *rpc) Close() {
	close(r.stop)
	// Not under wmu: a write Claude stopped reading is what Close has to get past, and closing
	// the pipe under it is what ends that write.
	_ = r.stdin.Close()
	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
	}
	// Killed whether or not the stream has ended: a reader that gave up on the stream leaves a
	// process writing into a pipe nobody drains, which never exits on its own. A process already
	// gone makes this an error nobody needs.
	if r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	_ = r.cmd.Wait()
	<-r.done
}

// mcp decodes the JSON-RPC message of an `mcp_message` request.
func (c controlRequest) mcp() (mcpMessage, error) {
	var m mcpMessage
	if err := json.Unmarshal(c.Message, &m); err != nil {
		return m, fmt.Errorf("claude: an MCP message this does not read: %w", err)
	}
	return m, nil
}

// decodeCall reads `tools/call`'s params.
func decodeCall(params json.RawMessage) (mcpCall, error) {
	var c mcpCall
	if err := json.Unmarshal(params, &c); err != nil {
		return c, fmt.Errorf("claude: a tool call this does not read: %w", err)
	}
	if c.Arguments == nil {
		c.Arguments = map[string]any{}
	}
	return c, nil
}

// mcpToolSpec is a tool as `tools/list` declares it: the runner's tool, its parameters as the
// JSON schema MCP speaks.
func mcpToolSpec(name, description string, params map[string]any, required []string) map[string]any {
	schema := map[string]any{"type": "object", "properties": params}
	if params == nil {
		schema["properties"] = map[string]any{}
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{"name": name, "description": description, "inputSchema": schema}
}

// mcpCallResult is the reply to `tools/call`: what the tool said, as text, and whether it failed.
func mcpCallResult(ok bool, text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": !ok}
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	return truncate(s, 200)
}
