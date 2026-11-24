package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The wire to a `codex app-server`: JSON-RPC, one object per line, over the process's stdin
// and stdout. This file is the whole of what knows that format. It starts the process, frames
// the messages, and hands the driver (codex.go) typed Go values: what codex asked, what codex
// said, and what codex answered. encoding/json stops here, which is why this file and no other
// in the runner is on the allowlist in scripts/check-json.sh.
//
// Three kinds of message cross it, and the driver treats them differently:
//
//   - a REQUEST this side makes (initialize, thread/start, turn/start, turn/steer,
//     turn/interrupt), answered by id;
//   - a REQUEST codex makes of this side (an approval, a question for the person), which the
//     driver answers after viff has decided;
//   - a NOTIFICATION from codex (an item started or completed, a turn completed), which the
//     driver narrates onto the read plane.
//
// Codex's requests and notifications arrive in order on one channel, so the driver sees them
// in the order codex emitted them.

// event is one message codex sent: a request (ID set) or a notification (ID empty).
type event struct {
	ID     json.RawMessage // the request id to answer, verbatim; empty for a notification
	Method string
	Params json.RawMessage
}

// isRequest reports whether codex expects an answer to this event.
func (e event) isRequest() bool { return len(e.ID) > 0 }

// rpc is one running app-server and the conversation with it.
type rpc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tail

	wmu sync.Mutex // one writer at a time on stdin
	seq atomic.Int64

	pmu     sync.Mutex
	pending map[int64]chan rpcReply // answers to requests this side made

	// Events is what codex sent that this side did not ask for. Closed when the process's
	// stdout ends, after which Err says why.
	//
	// It is fed through an unbounded queue rather than straight from the reader, so a burst
	// of notifications while this side is not reading them cannot stall the reader — which
	// is also the goroutine that delivers the answers this side waits on.
	Events <-chan event
	events chan event
	qmu    sync.Mutex
	queue  []event
	qwake  chan struct{}

	done chan struct{}
	err  error // why stdout ended, once it has
}

type rpcReply struct {
	result json.RawMessage
	err    *rpcError
}

// rpcError is the error object of a JSON-RPC response.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("%s (%d): %s", e.Message, e.Code, string(e.Data))
	}
	return fmt.Sprintf("%s (%d)", e.Message, e.Code)
}

// wireMessage is every shape a line can take, decoded loosely: a response has `id` and one of
// `result`/`error`; a request has `id` and `method`; a notification has `method` alone.
type wireMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// start starts `argv` and begins reading it. The process is killed when kill is called,
// and when ctx ends.
func startServer(ctx context.Context, argv []string, dir string, env []string) (*rpc, error) {
	if len(argv) == 0 {
		return nil, errors.New("codex: empty argv")
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
		return nil, fmt.Errorf("codex: starting %s: %w", argv[0], err)
	}
	r := &rpc{
		cmd: cmd, stdin: stdin, stderr: tl,
		pending: map[int64]chan rpcReply{},
		events:  make(chan event),
		done:    make(chan struct{}),
		qwake:   make(chan struct{}, 1),
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
			r.events <- *next
			continue
		}
		select {
		case <-r.qwake:
		case <-r.done:
			r.qmu.Lock()
			rest := r.queue
			r.queue = nil
			r.qmu.Unlock()
			for _, e := range rest {
				r.events <- e
			}
			return
		}
	}
}

// read is the one reader of stdout. It routes responses to their callers and everything else
// onto Events, in order.
func (r *rpc) read(stdout io.Reader) {
	defer close(r.done)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024) // a patch or a command's output can be large
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m wireMessage
		if err := json.Unmarshal(line, &m); err != nil {
			r.err = fmt.Errorf("codex: a line that is not JSON-RPC: %s", firstLine(string(line)))
			r.failPending()
			return
		}
		switch {
		case m.Method == "" && len(m.ID) > 0:
			// A response to something this side asked.
			var id int64
			if err := json.Unmarshal(m.ID, &id); err != nil {
				continue // not an id this side issued
			}
			r.pmu.Lock()
			ch, ok := r.pending[id]
			delete(r.pending, id)
			r.pmu.Unlock()
			if ok {
				ch <- rpcReply{result: m.Result, err: m.Error}
			}
		case m.Method != "":
			r.enqueue(event{ID: m.ID, Method: m.Method, Params: m.Params})
		}
	}
	if err := sc.Err(); err != nil {
		r.err = fmt.Errorf("codex: reading its output: %w", err)
	} else {
		r.err = errors.New("codex exited")
	}
	r.failPending()
}

// failPending wakes every caller waiting on an answer that is now never coming.
func (r *rpc) failPending() {
	r.pmu.Lock()
	defer r.pmu.Unlock()
	for id, ch := range r.pending {
		ch <- rpcReply{err: &rpcError{Message: "codex went away before answering"}}
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
	msg := "codex exited"
	if r.err != nil {
		msg = r.err.Error()
	}
	if s := strings.TrimSpace(r.stderr.String()); s != "" {
		msg += ": " + lastLine(s)
	}
	return msg
}

// Call makes a request and waits for its answer, or for ctx.
func (r *rpc) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := r.seq.Add(1)
	ch := make(chan rpcReply, 1)
	r.pmu.Lock()
	r.pending[id] = ch
	r.pmu.Unlock()
	if err := r.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		r.pmu.Lock()
		delete(r.pending, id)
		r.pmu.Unlock()
		return nil, fmt.Errorf("codex: %s: %w", method, err)
	}
	select {
	case rep := <-ch:
		if rep.err != nil {
			return nil, fmt.Errorf("codex: %s: %w", method, rep.err)
		}
		return rep.result, nil
	case <-ctx.Done():
		r.pmu.Lock()
		delete(r.pending, id)
		r.pmu.Unlock()
		return nil, ctx.Err()
	}
}

// CallInto is Call with the answer decoded into out.
func (r *rpc) CallInto(ctx context.Context, method string, params, out any) error {
	raw, err := r.Call(ctx, method, params)
	if err != nil {
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("codex: %s answered something this does not read: %w", method, err)
	}
	return nil
}

// Notify sends a notification: a message with no answer.
func (r *rpc) Notify(method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	return r.write(msg)
}

// Respond answers a request codex made.
func (r *rpc) Respond(id json.RawMessage, result any) error {
	return r.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// RespondError refuses a request codex made, for a request this side does not understand.
func (r *rpc) RespondError(id json.RawMessage, code int, msg string) error {
	return r.write(map[string]any{"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg}})
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

// Close ends the process: stdin closed, then a kill if it does not leave on its own.
func (r *rpc) Close() {
	r.wmu.Lock()
	_ = r.stdin.Close()
	r.wmu.Unlock()
	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
		if r.cmd.Process != nil {
			_ = r.cmd.Process.Kill()
		}
		<-r.done
	}
	_ = r.cmd.Wait()
}

// Decode reads an event's params into out.
func (e event) Decode(out any) error {
	if len(e.Params) == 0 {
		return nil
	}
	return json.Unmarshal(e.Params, out)
}

// The slices of codex's documents this driver reads. Anything codex says beyond these fields
// is ignored rather than decoded, so a new field upstream is not a breaking change.

// threadStart is what `thread/start` answers: the thread this task talks to.
type threadStart struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
	Model string `json:"model"`
}

// turn is a turn as `turn/start` answers and `turn/completed` reports it.
type turn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type turnStarted struct {
	Turn turn `json:"turn"`
}

type turnCompleted struct {
	ThreadID string `json:"threadId"`
	Turn     turn   `json:"turn"`
}

// item is a thread item in the fields this driver reads, whatever its type.
type item struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`
	// agentMessage, plan
	Text string `json:"text"`
	// commandExecution
	Command          string `json:"command"`
	Cwd              string `json:"cwd"`
	AggregatedOutput string `json:"aggregatedOutput"`
	ExitCode         *int   `json:"exitCode"`
	// fileChange
	Changes []fileChange `json:"changes"`
	// webSearch
	Query string `json:"query"`
	// mcpToolCall, dynamicToolCall
	Server string `json:"server"`
	Tool   string `json:"tool"`
}

type fileChange struct {
	Path string `json:"path"`
	Kind struct {
		Type     string `json:"type"` // add | delete | update
		MovePath string `json:"move_path"`
	} `json:"kind"`
}

type itemEvent struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Item     item   `json:"item"`
}

// commandApproval is `item/commandExecution/requestApproval`.
type commandApproval struct {
	ItemID     string  `json:"itemId"`
	TurnID     string  `json:"turnId"`
	ApprovalID *string `json:"approvalId"`
	Command    *string `json:"command"`
	Cwd        *string `json:"cwd"`
	Kind       string  `json:"kind"` // command | writeStdin
	Reason     *string `json:"reason"`
	// CommandActions is codex's reading of the command, for a command the request itself
	// does not carry whole.
	CommandActions []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	} `json:"commandActions"`
}

// commandText is the command as a person should read it.
func (a commandApproval) commandText() string {
	if a.Command != nil && *a.Command != "" {
		return *a.Command
	}
	var parts []string
	for _, c := range a.CommandActions {
		if c.Command != "" {
			parts = append(parts, c.Command)
		}
	}
	return strings.Join(parts, " | ")
}

// fileChangeApproval is `item/fileChange/requestApproval`. The changes themselves are on
// the item that `item/started` announced, by itemId.
type fileChangeApproval struct {
	ItemID    string  `json:"itemId"`
	TurnID    string  `json:"turnId"`
	Reason    *string `json:"reason"`
	GrantRoot *string `json:"grantRoot"`
}

// userInput is `item/tool/requestUserInput`: questions the model has for the person.
type userInput struct {
	ItemID     string `json:"itemId"`
	TurnID     string `json:"turnId"`
	IsBlocking bool   `json:"isBlocking"`
	Questions  []struct {
		ID       string `json:"id"`
		Header   string `json:"header"`
		Question string `json:"question"`
		IsOther  bool   `json:"isOther"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"questions"`
}

// kindPermissions is `item/permissions/requestApproval`: the model asking for more than its
// sandbox allows, for the rest of the turn.
type permissionsRequest struct {
	ItemID      string  `json:"itemId"`
	TurnID      string  `json:"turnId"`
	Cwd         string  `json:"cwd"`
	Reason      *string `json:"reason"`
	Permissions struct {
		Network *struct {
			Enabled *bool `json:"enabled"`
		} `json:"network"`
		FileSystem *struct {
			Entries []struct {
				Access string `json:"access"` // read | write | deny
				Path   struct {
					Type    string `json:"type"` // path | glob_pattern | special
					Path    string `json:"path"`
					Pattern string `json:"pattern"`
				} `json:"path"`
			} `json:"entries"`
			Read  []string `json:"read"`
			Write []string `json:"write"`
		} `json:"fileSystem"`
	} `json:"permissions"`
	// Raw is the request's `permissions` verbatim, so an approval grants exactly what was
	// asked and this side never has to re-render a profile it only partly reads.
	Raw json.RawMessage `json:"-"`
	// Other is the kinds of permission the request asks for beside the network and the file
	// system, which this side does not read: granted only as a widening is.
	Other []string `json:"-"`
}

// decodePermissions reads the request and keeps its profile verbatim for the answer.
func decodePermissions(e event) (permissionsRequest, error) {
	var p permissionsRequest
	if err := e.Decode(&p); err != nil {
		return p, err
	}
	var outer struct {
		Permissions json.RawMessage `json:"permissions"`
	}
	if err := e.Decode(&outer); err != nil {
		return p, err
	}
	p.Raw = outer.Permissions
	var kinds map[string]json.RawMessage
	if err := json.Unmarshal(p.Raw, &kinds); err == nil {
		for k, v := range kinds {
			if k != "network" && k != "fileSystem" && string(v) != "null" {
				p.Other = append(p.Other, k)
			}
		}
		sort.Strings(p.Other)
	}
	return p, nil
}

// permissionsAnswer is the reply to a permissions request: the profile granted, which is
// either the whole of what was asked or nothing.
func permissionsAnswer(p permissionsRequest, grant bool) map[string]any {
	if grant && len(p.Raw) > 0 {
		return map[string]any{"permissions": p.Raw, "scope": "turn"}
	}
	return map[string]any{"permissions": map[string]any{}, "scope": "turn"}
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// toolCall is `item/tool/call`: codex asking this side to run a tool this side declared on
// `thread/start` (dynamicTools). Arguments arrive as the JSON the model wrote; a model that
// wrote them as a string of JSON is read the same way.
type toolCall struct {
	ThreadID  string         `json:"threadId"`
	TurnID    string         `json:"turnId"`
	CallID    string         `json:"callId"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"-"`
}

func decodeToolCall(e event) (toolCall, error) {
	var tc toolCall
	if err := e.Decode(&tc); err != nil {
		return tc, err
	}
	var raw struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := e.Decode(&raw); err != nil {
		return tc, err
	}
	tc.Arguments = map[string]any{}
	if len(raw.Arguments) == 0 || string(raw.Arguments) == "null" {
		return tc, nil
	}
	if err := json.Unmarshal(raw.Arguments, &tc.Arguments); err != nil {
		var s string
		if json.Unmarshal(raw.Arguments, &s) != nil || json.Unmarshal([]byte(s), &tc.Arguments) != nil {
			return tc, fmt.Errorf("tool %s: arguments are neither an object nor a string of one", tc.Tool)
		}
	}
	return tc, nil
}

// toolCallAnswer is the reply to `item/tool/call`: whether it succeeded, and what it said, as
// the one content kind this side produces.
func toolCallAnswer(ok bool, text string) map[string]any {
	return map[string]any{
		"success":      ok,
		"contentItems": []map[string]any{{"type": "inputText", "text": text}},
	}
}

// dynamicToolSpec is one tool as `thread/start` declares it to codex.
func dynamicToolSpec(name, description string, params map[string]any, required []string) map[string]any {
	if required == nil {
		required = []string{}
	}
	if params == nil {
		params = map[string]any{}
	}
	return map[string]any{
		"type": "function", "name": name, "description": description,
		"inputSchema": map[string]any{"type": "object", "properties": params, "required": required},
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
