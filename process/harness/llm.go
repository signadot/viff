package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/signadot/verse/process"
)

// THIS FILE IS THE ONE PLACE VERSE STILL SPEAKS JSON, deliberately. The schema below is
// OpenAI's, not verse's: it is what the remote endpoint accepts and returns, so it is not
// ours to re-render. The json: tags and encoding/json calls here are correct and must stay —
// a sweep that "finishes the tony migration" by converting them would break every model
// call. Everything verse itself defines lives in codec.
//
// OpenAI-compatible Chat Completions wire types. Served here through the Portkey
// gateway, but the shape is the ubiquitous OpenAI standard — point PORTKEY_BASE_URL
// at any compatible endpoint to swap providers without touching the loop.

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type toolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type chatRequest struct {
	Model      string        `json:"model"`
	Messages   []chatMessage `json:"messages"`
	Tools      []toolDef     `json:"tools,omitempty"`
	ToolChoice string        `json:"tool_choice,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// llmClient is a zero-dependency Chat Completions client. Credentials come from
// the environment (PORTKEY_API_KEY / PORTKEY_VIRTUAL_KEY) — never hardcoded.
type llmClient struct {
	url        string
	portkeyKey string
	virtualKey string
	model      string
	http       *http.Client
}

// newLLMClient builds a client from the environment (the default when no explicit
// creds are supplied).
func newLLMClient() *llmClient {
	return clientFrom(os.Getenv("PORTKEY_BASE_URL"), os.Getenv("PORTKEY_API_KEY"),
		os.Getenv("PORTKEY_VIRTUAL_KEY"), os.Getenv("OPENAI_MODEL"))
}

// clientFrom builds a client from explicit creds (e.g. resolved verse config),
// applying the base-URL and model defaults for any empty value.
func clientFrom(base, key, virtualKey, model string) *llmClient {
	if base == "" {
		base = "https://api.portkey.ai/v1"
	}
	if model == "" {
		model = "gpt-4o"
	}
	return &llmClient{
		url:        strings.TrimRight(base, "/") + "/chat/completions",
		portkeyKey: key,
		virtualKey: virtualKey,
		model:      model,
		http:       &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *llmClient) complete(ctx context.Context, msgs []chatMessage, tools []toolDef) (chatMessage, string, error) {
	body, _ := json.Marshal(chatRequest{Model: c.model, Messages: msgs, Tools: tools, ToolChoice: "auto"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return chatMessage{}, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-portkey-api-key", c.portkeyKey)
	if c.virtualKey != "" {
		req.Header.Set("x-portkey-virtual-key", c.virtualKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return chatMessage{}, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return chatMessage{}, "", fmt.Errorf("decode (http %d): %s", resp.StatusCode, truncate(string(raw), 300))
	}
	if out.Error != nil {
		return chatMessage{}, "", fmt.Errorf("api: %s", out.Error.Message)
	}
	if resp.StatusCode >= 300 || len(out.Choices) == 0 {
		return chatMessage{}, "", fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	return out.Choices[0].Message, out.Choices[0].FinishReason, nil
}

// llmDriver adapts the model's turn-based tool calling onto the process loop's
// one-boundary-at-a-time Driver: a model turn can emit several tool calls, so it
// dispatches them FIFO and only requests the next model turn once all their
// results are collected. A gate-DENIED action is fed back as a tool error, which
// the model sees and re-plans around — the gate steering a real agent.
// completer is the model-call seam (satisfied by *llmClient) — an interface so
// the driver can be unit-tested with a fake.
type completer interface {
	complete(ctx context.Context, msgs []chatMessage, tools []toolDef) (chatMessage, string, error)
}

type llmDriver struct {
	client       completer
	tools        []toolDef
	messages     []chatMessage
	pending      []toolCall
	idx          int
	last         *toolCall
	started      bool
	frame        string // system prompt; empty is legal (a driver built without one)
	prompt       string
	log          io.Writer
	emit         func(process.Frame) // publish narration onto the process read plane; nil = log only
	pendingSteer []chatMessage       // injected instructions, folded in before the next model turn
}

func (d *llmDriver) Next(ctx context.Context, prev process.StepResult) (process.ToolUse, bool, error) {
	if d.last != nil { // record the result of the tool call we dispatched last
		d.messages = append(d.messages, chatMessage{
			Role: "tool", ToolCallID: d.last.ID, Name: d.last.Function.Name,
			Content: resultContent(prev),
		})
		d.last = nil
	}
	for {
		if d.idx < len(d.pending) { // dispatch the next buffered tool call
			tc := d.pending[d.idx]
			d.idx++
			// `fail` is a TERMINAL tool: it is not dispatched to the executor, it ends the
			// loop with an error, which is the process's Failed path (the same one failDriver
			// takes). Without it the model has no structural way to distinguish "I did it"
			// from "I could not" — the loop below ends on "no more tool calls" either way — so
			// a process that could not open the file it was asked to review completes as
			// `done` with prose explaining the failure. A tool call is a discrete branch the
			// model must choose, which is a far better signal than hoping prose gets read.
			//
			// It raises the floor; it is not a guarantee. A model can still stop without
			// calling it. What does not depend on the model's honesty is the charter naming
			// what it expects (see the deref in trigger/deref.go): an agent that failed
			// cannot produce the value, so the firing fails whether or not it admitted
			// anything.
			if tc.Function.Name == failTool {
				return process.ToolUse{}, false, errors.New(failReason(tc))
			}
			d.last = &tc
			return toToolUse(tc), true, nil
		}
		if !d.started {
			d.started = true
			// The frame is per-driver, not a package constant: it is what the spawn (or,
			// failing that, defaultFrame) says this agent's task is to be read in. An empty
			// frame sends no system message at all rather than an empty one — a blank
			// system turn is a claim about the agent's standing instructions, and "none"
			// is the honest wire form of having none.
			if d.frame != "" {
				d.messages = append(d.messages, chatMessage{Role: "system", Content: d.frame})
			}
			d.messages = append(d.messages, chatMessage{Role: "user", Content: d.prompt})
		}
		if len(d.pendingSteer) > 0 { // re-prompt folds in here — pending drained, results already appended
			d.messages = append(d.messages, d.pendingSteer...)
			d.pendingSteer = nil
		}
		msg, finish, err := d.client.complete(ctx, d.messages, d.tools)
		if err != nil {
			return process.ToolUse{}, false, err
		}
		d.messages = append(d.messages, msg)
		if c := strings.TrimSpace(msg.Content); c != "" {
			fmt.Fprintf(d.log, "\n🗣  %s\n", c)
			if d.emit != nil { // put the agent's words on the read plane (attach sees them)
				d.emit(process.Frame{Kind: process.EvSay, Text: c})
			}
		}
		if finish != "tool_calls" || len(msg.ToolCalls) == 0 {
			return process.ToolUse{}, false, nil // model is done
		}
		d.pending = msg.ToolCalls
		d.idx = 0
	}
}

// Steer buffers injected instructions as user messages (implements
// process.Steerable). They fold into the conversation at the next model turn,
// after any in-flight tool calls and their results — so the message sequence
// stays valid (never a user message between an assistant tool_calls and its
// tool results).
func (d *llmDriver) Steer(instructions []string) {
	for _, m := range instructions {
		d.pendingSteer = append(d.pendingSteer, chatMessage{Role: "user", Content: m})
	}
}

func toToolUse(tc toolCall) process.ToolUse {
	var args map[string]any
	if tc.Function.Arguments != "" {
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
	}
	tu := process.ToolUse{Name: tc.Function.Name, Input: args}
	if tc.Function.Name == "call_agent" { // becomes a gated, async sub-process call
		agent, _ := args["agent"].(string)
		prompt, _ := args["prompt"].(string)
		tu.CallAgent = &process.CallAgentRequest{
			Agent:  agent,
			Prompt: prompt,
			Grant:  process.Grant{Allow: []process.GateMatch{{}}}, // demo: full scope; tighten later
		}
	}
	return tu
}

// Result implements process.Resulter — the driver's final assistant message,
// folded into the parent when this (sub-)process completes.
func (d *llmDriver) Result() string {
	for i := len(d.messages) - 1; i >= 0; i-- {
		if d.messages[i].Role == "assistant" && strings.TrimSpace(d.messages[i].Content) != "" {
			return d.messages[i].Content
		}
	}
	return ""
}

func resultContent(prev process.StepResult) string {
	switch {
	case prev.Blocked:
		return "DENIED by supervisor gate: " + prev.Note +
			". Do not retry this exact action; choose a different approach or explain why you cannot proceed."
	case prev.Err != nil:
		return "ERROR: " + prev.Err.Error()
	case prev.Output == "":
		return "(ok)"
	default:
		return prev.Output
	}
}

// selectTools returns the tool definitions to offer: all of them when only is empty, else
// exactly the ones named — plus failTool, always.
//
// fail is not optional because the loop's contract depends on it. It is the only structural
// way an agent can say "I could not": a driver that simply stops is read as SUCCESS by
// everything downstream, so a spawn that withheld fail would turn every failure into a
// completed process with an excuse in its result. A charter deref'ing $result is exactly
// the caller that cannot afford that.
//
// An unknown name is an error rather than a skip. Quietly offering fewer tools than the
// charter asked for is the failure mode this change exists to remove: a field that reads
// back as written and does something else.
func selectTools(only []string) ([]toolDef, error) {
	all := toolDefs()
	if len(only) == 0 {
		return all, nil
	}
	byName := make(map[string]toolDef, len(all))
	for _, td := range all {
		byName[td.Function.Name] = td
	}
	var out []toolDef
	seen := map[string]bool{}
	for _, n := range only {
		td, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("no such tool %q (have %s)", n, strings.Join(toolNames(all), ", "))
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, td)
		}
	}
	if !seen[failTool] {
		out = append(out, byName[failTool])
	}
	return out, nil
}

// toolNames lists what is on offer, in order — used by the frame, so the prompt cannot name
// a tool the request does not actually carry.
func toolNames(tds []toolDef) []string {
	out := make([]string, 0, len(tds))
	for _, td := range tds {
		out = append(out, td.Function.Name)
	}
	return out
}

func toolDefs() []toolDef {
	s := map[string]any{"type": "string"}
	mk := func(name, desc string, props map[string]any, req []string) toolDef {
		var t toolDef
		t.Type = "function"
		t.Function.Name = name
		t.Function.Description = desc
		t.Function.Parameters = map[string]any{"type": "object", "properties": props, "required": req}
		return t
	}
	return []toolDef{
		mk("read_file", "Read a UTF-8 text file within the working directory.",
			map[string]any{"path": s}, []string{"path"}),
		mk("list_dir", "List directory entries within the working directory.",
			map[string]any{"path": s}, []string{"path"}),
		mk("write_file", "Create or overwrite a text file within the working directory.",
			map[string]any{"path": s, "content": s}, []string{"path", "content"}),
		mk("run_bash", "Run a bash command in the working directory; returns combined stdout/stderr.",
			map[string]any{"command": s}, []string{"command"}),
		mk("call_agent", "Delegate a self-contained subtask to a sub-agent that runs concurrently. Give a short agent name and a full prompt describing the task; its result comes back when it finishes. Use for independent subtasks you can run in parallel.",
			map[string]any{"agent": s, "prompt": s}, []string{"agent", "prompt"}),
		mk(failTool, "Declare that you could NOT complete the task, and stop. Call this instead of "+
			"finishing with an explanation when the task could not be done — a file you needed was "+
			"missing, a command failed, the request was impossible. Simply stopping is read as "+
			"SUCCESS by everything downstream, so an explanation in your final message is not enough: "+
			"it must be this call. Give the reason.",
			map[string]any{"reason": s}, []string{"reason"}),
	}
}

// failTool is the terminal tool by which a model declares failure. Named once because the
// driver matches on it and the tool definition declares it, and a drift between the two
// would silently turn the fail branch back off.
const failTool = "fail"

// failReason pulls the model's stated reason out of the fail call, falling back to a fixed
// message: a fail with unparseable arguments is still a fail, and must not be swallowed into
// a successful completion because the JSON was malformed.
func failReason(tc toolCall) string {
	var args struct {
		Reason string `json:"reason"`
	}
	if tc.Function.Arguments != "" {
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
	}
	if strings.TrimSpace(args.Reason) == "" {
		return "the agent declared failure without giving a reason"
	}
	return args.Reason
}
