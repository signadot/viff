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

	"github.com/signadot/verse/task"
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
		http:       &http.Client{Timeout: modelCallTimeout},
	}
}

// modelCallTimeout bounds ONE model call. It is generous because it is a backstop against a
// stalled connection, not a latency budget: a completion over a large conversation legitimately
// takes minutes, and 120s — the first guess — killed a task that had done 43 boundaries of real
// work, on one slow response. Without streaming there is no way to tell slow from stuck, so the
// number errs toward slow and the retry below covers stuck.
const modelCallTimeout = 5 * time.Minute

// transient marks a failure worth trying again: the request did not reach a decision, or the far
// side said "not now". The distinction is the whole of the retry policy — a bad key, a malformed
// request and a refused tool schema are all permanent, and retrying them spends a task's budget
// on a result that cannot change.
type transient struct{ err error }

func (t transient) Error() string { return t.err.Error() }
func (t transient) Unwrap() error { return t.err }

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
		// Nothing came back: a timeout, a reset, a DNS failure. None of them says the request
		// was wrong, which is what makes them worth repeating.
		return chatMessage{}, "", transient{err}
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
		err := fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(raw), 300))
		// 429 and 5xx are the far side saying "not now" and "not me"; everything else in the
		// 4xx range is this request being wrong, and asking again will not fix it.
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return chatMessage{}, "", transient{err}
		}
		return chatMessage{}, "", err
	}
	return out.Choices[0].Message, out.Choices[0].FinishReason, nil
}

// modelRetries is how many EXTRA attempts a transient model failure gets, and the backoff before
// each. Small on purpose: this covers a hiccup, not an outage, and a task that keeps failing
// should fail while somebody still has the context to care.
var modelBackoff = []time.Duration{2 * time.Second, 8 * time.Second}

// modelWaitNotice is how often a call still in flight says so on the read plane, and
// modelTotalBudget bounds the whole retried sequence.
//
// Both exist because the retry, on its own, made a stall INVISIBLE FOR LONGER. Before it, a stuck
// call failed in two minutes; after, three attempts at five minutes each is a quarter of an hour
// in which a task emits nothing at all and cannot be told from one that has died. A drive watched
// exactly that and reported "it appears hung" — which was the correct reading of the evidence
// available, and is findings 6 and 7 of that drive with a new cause.
//
// So a long call announces itself while it is still going, rather than only when it fails, and
// the sequence stops asking once the budget is spent. The point of a retry is a hiccup; past a
// few minutes it is not a hiccup and somebody should be told rather than waited out.
var (
	modelWaitNotice  = 30 * time.Second
	modelTotalBudget = 8 * time.Minute
)

// completeWithRetry is the model call, repeated when the failure says nothing about the request.
//
// It exists because one slow response used to be terminal for the whole task. Forty-three
// boundaries of work — reads, reasoning, a finding half-written — ended on a client timeout, and
// the run was not resumable because a task's conversation lives in its driver. Against that, two
// retries costing ten seconds are free.
//
// Each attempt is announced on the read plane. A run that pauses for ten seconds and says nothing
// is indistinguishable from one that has hung, which is the complaint findings 6 and 7 of the
// drive make about everything else.
func (d *llmDriver) completeWithRetry(ctx context.Context) (chatMessage, string, error) {
	var last error
	started := time.Now()
	for attempt := 0; ; attempt++ {
		msg, finish, err := d.completeAloud(ctx)
		if err == nil {
			return msg, finish, nil
		}
		last = err
		var t transient
		if !errors.As(err, &t) || attempt >= len(modelBackoff) {
			break
		}
		if spent := time.Since(started); spent > modelTotalBudget {
			last = fmt.Errorf("%w (giving up after %s of retrying: this is not a hiccup)", err,
				spent.Round(time.Second))
			break
		}
		wait := modelBackoff[attempt]
		note := fmt.Sprintf("the model call failed and is being retried in %s (attempt %d of %d): %v",
			wait, attempt+2, len(modelBackoff)+1, err)
		fmt.Fprintf(d.log, "   ↻ %s\n", note)
		if d.emit != nil {
			d.emit(task.Frame{Kind: task.EvSay, Text: note})
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			// A cancelled task must not keep asking. The cancel is the better account of why
			// this ended than the failure that was being retried.
			return chatMessage{}, "", ctx.Err()
		}
	}
	return chatMessage{}, "", fmt.Errorf("the model call failed %d time(s) and this task cannot "+
		"continue without it: %w", len(modelBackoff)+1, last)
}

// completeAloud is one attempt, saying so periodically while it is still in flight.
//
// A model call is the one step of the loop that emits nothing until it is over, and it is also the
// longest. Everything else — a read, a write, a gate — puts a frame on the read plane the moment it
// happens, so silence during a call reads as death rather than as thought.
func (d *llmDriver) completeAloud(ctx context.Context) (chatMessage, string, error) {
	done := make(chan struct{})
	// The interval is read HERE rather than inside the goroutine, and that is not a style
	// preference. Nothing waits for that goroutine — `close(done)` tells it to stop but
	// establishes no ordering with anything it already did — so a read inside it is unordered
	// against every later write to the package variable. The tests shorten these to run in
	// milliseconds, so the detector sees one test's leftover ticker racing the next test's
	// setup. Reading on the caller's goroutine puts the read in the call's own order, which is
	// also what the value means: how often THIS call should announce itself, decided when it
	// starts rather than whenever the goroutine happens to be scheduled.
	every := modelWaitNotice
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		started := time.Now()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				note := fmt.Sprintf("still waiting on the model (%s)",
					time.Since(started).Round(time.Second))
				fmt.Fprintf(d.log, "   … %s\n", note)
				if d.emit != nil {
					d.emit(task.Frame{Kind: task.EvSay, Text: note})
				}
			}
		}
	}()
	msg, finish, err := d.client.complete(ctx, d.messages, d.tools)
	close(done)
	return msg, finish, err
}

// llmDriver adapts the model's turn-based tool calling onto the task loop's
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
	client   completer
	tools    []toolDef
	messages []chatMessage
	pending  []toolCall
	idx      int
	last     *toolCall
	started  bool
	frame    string // system prompt; empty is legal (a driver built without one)
	prompt   string
	log      io.Writer
	emit     func(task.Frame) // publish narration onto the task read plane; nil = log only
	folding  []chatMessage    // messages waiting to land, folded in before the next model turn
}

func (d *llmDriver) Next(ctx context.Context, prev task.StepResult) (task.ToolUse, bool, error) {
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
			// loop with an error, which is the task's Failed path (the same one failDriver
			// takes). Without it the model has no structural way to distinguish "I did it"
			// from "I could not" — the loop below ends on "no more tool calls" either way — so
			// a task that could not open the file it was asked to review completes as
			// `done` with prose explaining the failure. A tool call is a discrete branch the
			// model must choose, which is a far better signal than hoping prose gets read.
			//
			// It raises the floor; it is not a guarantee. A model can still stop without
			// calling it. What does not depend on the model's honesty is the charter naming
			// what it expects (see the deref in trigger/deref.go): an agent that failed
			// cannot produce the value, so the firing fails whether or not it admitted
			// anything.
			if tc.Function.Name == yieldTool {
				return d.yield(tc)
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
		if len(d.folding) > 0 { // conversation lands here — tool calls drained, results already appended
			d.messages = append(d.messages, d.folding...)
			d.folding = nil
		}
		msg, finish, err := d.completeWithRetry(ctx)
		if err != nil {
			return task.ToolUse{}, false, err
		}
		d.messages = append(d.messages, msg)
		if c := strings.TrimSpace(msg.Content); c != "" {
			fmt.Fprintf(d.log, "\n🗣  %s\n", c)
			if d.emit != nil { // put the agent's words on the read plane (attach sees them)
				d.emit(task.Frame{Kind: task.EvSay, Text: c})
			}
		}
		if finish != "tool_calls" || len(msg.ToolCalls) == 0 {
			// The model has stopped talking, which is the only way it can stop, and that is
			// ALL this reports. Whether the TASK is finished is a different question, and it
			// is not the driver's: with a child still working, "the model stopped" means "it
			// is waiting". The answer contract used to be checked right here, which is why it
			// could certify a draft written while a child was still editing the tree it
			// described. The loop now asks when the input has actually gone quiet
			// (task.AnswerCheck), and a repair comes back through the same channel every
			// other message into this conversation does.
			return task.ToolUse{}, false, nil
		}
		d.pending = msg.ToolCalls
		d.idx = 0
	}
}

// Fold buffers messages as user turns (implements task.Conversation). They land at the next
// model turn, after any in-flight tool calls and their results — so the message sequence stays
// valid (never a user message between an assistant tool_calls and its tool results).
//
// Whose words these are is not this driver's business: an operator, a finished child, a
// resolver, or the runtime asking for a repair all arrive here, and the read plane is where
// they stay told apart.
func (d *llmDriver) Fold(messages []string) {
	for _, m := range messages {
		d.folding = append(d.folding, chatMessage{Role: "user", Content: m})
	}
}

func toToolUse(tc toolCall) task.ToolUse {
	var args map[string]any
	if tc.Function.Arguments != "" {
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
	}
	tu := task.ToolUse{Name: tc.Function.Name, Input: args}
	if tc.Function.Name == "call_agent" { // becomes a gated, async sub-task call
		agent, _ := args["agent"].(string)
		prompt, _ := args["prompt"].(string)
		tu.CallAgent = &task.CallAgentRequest{
			Agent:  agent,
			Prompt: prompt,
			Grant:  task.Grant{Allow: []task.GateMatch{{}}}, // demo: full scope; tighten later
		}
	}
	return tu
}

// Result implements task.Resulter — the driver's final assistant message,
// folded into the parent when this (sub-)task completes.
func (d *llmDriver) Result() string {
	for i := len(d.messages) - 1; i >= 0; i-- {
		if d.messages[i].Role == "assistant" && strings.TrimSpace(d.messages[i].Content) != "" {
			return d.messages[i].Content
		}
	}
	return ""
}

func resultContent(prev task.StepResult) string {
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
// exactly the ones named — plus yieldTool, always.
//
// fail is not optional because the loop's contract depends on it. It is the only structural
// way an agent can say "I could not": a driver that simply stops is read as SUCCESS by
// everything downstream, so a spawn that withheld fail would turn every failure into a
// completed task with an excuse in its result. A charter deref'ing $result is exactly
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
	// yield is not optional, and is the one tool that never was. It carries the only
	// structural way to say "I could not", which everything downstream depends on — a task that
	// merely stops reads as success — and now also the only way to wait. A spawn that withheld
	// it would turn every failure into a completed task with an excuse, and every wait into
	// whatever the model could improvise.
	if !seen[yieldTool] {
		out = append(out, byName[yieldTool])
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
		// HOW TO WAIT is half of this description, and it has now been got wrong twice in
		// opposite directions.
		//
		// The loop joins children only when a turn ENDS, so ending the turn is the correct way
		// to wait and a free one: it blocks in Go, costs no tokens, and the model is called
		// again the moment a child reports. Told only that a result "comes back when it
		// finishes", a model reached for the wait it could see — `run_bash {command: "sleep
		// 120"}` — and paid a round trip per poll to learn nothing (git-issue
		// 91r674qzh12ks35vfnn0: eighteen minutes, twenty-five boundaries, mostly asleep).
		//
		// So this said "END YOUR TURN: say what you are waiting for and stop, without calling a
		// tool", and a drive watched a model do the first half and not the second — narrating
		// "Still waiting for server_go_reviewer and test_go_reviewer" at every boundary while
		// spawning `noop`, `noop2` … `noop10` to fill them. A turn carries content AND tool
		// calls, so "say it and stop" asks for a thing the model has no way to commit to, and
		// waiting stayed the one thing it wanted with no verb for it.
		//
		// Hence `wait` below, and this description points at it rather than describing a
		// discipline. Advice that has failed twice is not advice that needs stating a third
		// time.
		mk("call_agent", "Delegate a self-contained subtask to a sub-agent that runs concurrently. "+
			"Give a short agent name and a full prompt describing the task. Use for independent "+
			"subtasks you can run in parallel. TO WAIT FOR THEIR RESULTS, call "+yieldTool+" with "+
			"reason \""+yieldWaiting+"\" — that is what it is for, and it is free. Never poll: not "+
			"with sleep, and not by delegating filler work. A turn that is still running is a turn "+
			"whose sub-agents cannot report to you, so anything you do to pass the time delays the "+
			"results rather than waiting for them.",
			map[string]any{"agent": s, "prompt": s}, []string{"agent", "prompt"}),
		// ONE verb for every way a turn can end, because the model had none and kept building
		// them out of whatever was to hand. The reason is enumerated rather than free text: it
		// is the difference between failing and pausing, and a field a model can waffle into is
		// how a terminal tool became a scratchpad.
		mk(yieldTool, "Hand control back to the loop that is running you. This ENDS YOUR TURN — "+
			"the only way to do that deliberately — and says why, which decides what happens next:\n"+
			"  \""+yieldWaiting+"\": you have delegated work and have nothing to do until it comes "+
			"back. Costs nothing and takes no time from your sub-agents; you are called again the "+
			"moment one reports, with its result. This is the ONLY way to wait. Do not sleep, do "+
			"not re-run commands to pass the time, and do not delegate filler work — all of those "+
			"keep your turn running, which is what stops a result from reaching you.\n"+
			"  \""+yieldDone+"\": the work is finished and your last message is the answer.\n"+
			"  \""+yieldCannot+"\": you could NOT do it — a file you needed was missing, a command "+
			"failed, the request was impossible. Say why in `note`. This is the only reason that "+
			"fails the task, and it must be this call: simply stopping is read as SUCCESS by "+
			"everything downstream, so an explanation in your final message is not enough.",
			map[string]any{
				"reason": map[string]any{"type": "string",
					"enum":        []string{yieldWaiting, yieldDone, yieldCannot},
					"description": "why control is being handed back"},
				"note": map[string]any{"type": "string",
					"description": "for " + yieldCannot + ", what went wrong"},
			}, []string{"reason"}),
	}
}

// yieldTool is the one thing a model could not previously say: HAND CONTROL BACK.
//
// It had exits and no verb for them. "I am done" was implicit — stop emitting tool calls — and
// worked. "I could not" was `fail`. "I am waiting for my sub-agents" was nothing at all, and a
// model with nothing to say and no way to say it reaches for whatever is to hand: first
// `run_bash sleep 120`, then eleven sub-agents called `noop`, and once for `fail` itself, with
// the reason "placeholder - not actually failing, retracting" — a terminal tool used as a
// scratchpad, discarding a review whose children had both reported clean.
//
// Both are the same defect. Naming the mechanism rather than one of its uses is what stops the
// model approximating it: yielding is a thing it can now DO, so it does not have to build one
// out of a sleep, a sub-agent, or a failure.
const yieldTool = "yield"

// Why a turn is being handed back. Only `cannot` fails the run — see yield for why the
// unrecognised case deliberately does not.
const (
	yieldWaiting = "waiting"
	yieldDone    = "done"
	yieldCannot  = "cannot"
)

// yieldHint is how the answer contract's repair tells a model to give up honestly, rather than
// inventing a value to satisfy a shape. It is a phrase rather than a bare tool name because the
// tool needs its argument to mean anything.
const yieldHint = yieldTool + ` with reason "` + yieldCannot + `"`

// yield hands the turn back, and decides only one thing: whether that is a failure.
//
// `cannot` is the sole failing reason, and it must be said EXPLICITLY. The two ways to get this
// wrong are opposite and both have been paid for: treat an unrecognised reason as failure and a
// model experimenting discards finished work (the `fail`-as-scratchpad run); treat a failure as
// an ordinary stop and a task that could not do its job reports `done` with an excuse in its
// result, which is the floor `fail` was introduced to raise.
//
// So the reason is enumerated in the schema, `cannot` is the only failing value, and anything
// unrecognised yields with the misunderstanding written into the tool result. The model gets
// another turn either way: it can yield properly or produce its answer, and nothing is silently
// discarded or silently declared a success. As `fail` always did, this raises the floor without
// claiming to be a guarantee — a model can still simply stop.
func (d *llmDriver) yield(tc toolCall) (task.ToolUse, bool, error) {
	reason, note := yieldArgs(tc)
	if reason == yieldCannot {
		if note == "" {
			note = "the agent yielded as unable to continue without giving a reason"
		}
		return task.ToolUse{}, false, errors.New(note)
	}
	said := "Control is back with the loop."
	switch reason {
	case yieldWaiting:
		said += " You will be called again as soon as a sub-agent reports, with its result."
	case yieldDone:
		said += " Your last message stands as this task's answer."
	default:
		said += fmt.Sprintf(" (Reason %q is not one of %q, %q or %q. If you could not complete "+
			"the task, say so with %s — stopping is otherwise read as success.)",
			reason, yieldWaiting, yieldDone, yieldCannot, yieldHint)
	}
	d.messages = append(d.messages, chatMessage{
		Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: said,
	})
	// Said on the read plane, because a yield is otherwise INVISIBLE there. The polling it
	// replaces was at least legible — a watcher saw exec frames going past — and a boundary
	// with nothing in it reads as a task that has stopped. That is the complaint the model-call
	// notice exists to answer, arriving by a second route, and the fix is the same: a task that
	// is deliberately quiet should say so rather than only look it.
	note = strings.TrimSpace(note)
	line := "yielded (" + reason + ")"
	if note != "" {
		line += ": " + note
	}
	fmt.Fprintf(d.log, "   … %s\n", line)
	if d.emit != nil {
		d.emit(task.Frame{Kind: task.EvSay, Text: line})
	}
	return task.ToolUse{}, false, nil
}

// yieldArgs reads the call's arguments, tolerating a model that sent none: an unparseable yield
// is still a yield, and must not be swallowed into something else because the JSON was bad.
func yieldArgs(tc toolCall) (reason, note string) {
	var args struct {
		Reason string `json:"reason"`
		Note   string `json:"note"`
	}
	if tc.Function.Arguments != "" {
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
	}
	return strings.TrimSpace(args.Reason), strings.TrimSpace(args.Note)
}
