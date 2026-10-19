// Package modeltest serves a SCRIPTED model on the wire the llm driver speaks (OpenAI-compatible
// Chat Completions, runner/runnerd/verserunner/agent/llm.go), so a test outside package agent
// can run a real task — a real loop, real tools, a real answer contract — against a model
// whose every reply the test decides. It is the double for the one thing a test cannot own:
// what the model says.
//
// Point a harness at it with LLMBaseURL = (*Model).URL and any non-empty key.
package modeltest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Message is one message of the conversation as the driver sent it.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is a tool call on the wire.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Request is what the driver asked: the conversation so far and the tools on offer.
type Request struct {
	// VirtualKey is the Portkey virtual key the request was sent under (its header).
	VirtualKey string    `json:"-"`
	Model      string    `json:"model"`
	Messages   []Message `json:"messages"`
	Tools      []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools,omitempty"`
}

// System is the frame the task was given.
func (r Request) System() string {
	for _, m := range r.Messages {
		if m.Role == "system" {
			return m.Content
		}
	}
	return ""
}

// Prompt is the task's prompt: the first user message.
func (r Request) Prompt() string {
	for _, m := range r.Messages {
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
}

// Results are the tool results the conversation holds, in order.
func (r Request) Results() []string {
	var out []string
	for _, m := range r.Messages {
		if m.Role == "tool" {
			out = append(out, m.Content)
		}
	}
	return out
}

// Offered says whether a tool of that name was on offer.
func (r Request) Offered(name string) bool {
	for _, t := range r.Tools {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}

// Reply is the model's next message: text, or tool calls.
type Reply struct {
	Text  string
	Calls []Call
}

// Call is one tool call the model makes.
type Call struct {
	Name string
	Args map[string]any
}

// Say is a reply of text.
func Say(text string) Reply { return Reply{Text: text} }

// Use is a reply calling one tool.
func Use(name string, args map[string]any) Reply {
	return Reply{Calls: []Call{{Name: name, Args: args}}}
}

// Model is the scripted endpoint.
type Model struct {
	URL    string
	mu     sync.Mutex
	script func(Request) Reply
	seen   []Request
	calls  int
}

// New serves a model answering each request with script's reply. It stops with the test.
func New(t testing.TB, script func(Request) Reply) *Model {
	m := &Model{script: script}
	srv := httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(srv.Close)
	m.URL = srv.URL
	return m
}

// Seen is every request the model was sent, in order.
func (m *Model) Seen() []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Request(nil), m.seen...)
}

func (m *Model) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.VirtualKey = r.Header.Get("x-portkey-virtual-key")
	m.mu.Lock()
	m.seen = append(m.seen, req)
	m.calls++
	n := m.calls
	script := m.script
	m.mu.Unlock()
	reply := script(req)
	msg := Message{Role: "assistant", Content: reply.Text}
	finish := "stop"
	for i, c := range reply.Calls {
		args, err := json.Marshal(c.Args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tc := ToolCall{ID: fmt.Sprintf("call_%d_%d", n, i), Type: "function"}
		tc.Function.Name, tc.Function.Arguments = c.Name, string(args)
		msg.ToolCalls = append(msg.ToolCalls, tc)
		finish = "tool_calls"
	}
	resp := map[string]any{"choices": []any{map[string]any{"message": msg, "finish_reason": finish}}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
