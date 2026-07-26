package harness

import (
	"bytes"
	"context"
	"encoding/json"
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
			d.last = &tc
			return toToolUse(tc), true, nil
		}
		if !d.started {
			d.started = true
			d.messages = append(d.messages,
				chatMessage{Role: "system", Content: systemPrompt},
				chatMessage{Role: "user", Content: d.prompt})
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
	}
}
