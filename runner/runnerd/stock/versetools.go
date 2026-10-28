package stock

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/verse"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// THE VERSE SYSTEM is verse itself, acted on by a task as the person it runs for would act on it:
// each verb is a verb of `verse` and of `verse mcp`, its tool spelled as the MCP tool is and taking
// what that tool takes, carried by the runner to verse's API over the task's channel
// (delivery.OpVerseAskCreate) and admitted as the task's owner. A task's own questions, which it
// waits on as a task, are the question system's (verse.Ask).

// askCreateTool is `verse ask <name> -q <question>`, verse_ask_create: an ask raised as the
// person the task runs for. It does not wait: the ask outlives the task, and its `then` and `else`
// run as that person when it settles, which is what a stored continuation is for.
var askCreateTool = systems.Tool("verse", "ask-create")

// verseTools are the verse system's tools, where the runner reaches its verse with a token of its
// own.
func verseTools(cl *delivery.Client) []verse.Tool {
	text := func(what string) map[string]any { return map[string]any{"type": "string", "description": what} }
	held := " A spawn reaches no further than this task does, its autonomy is no looser, and it runs on " +
		"this task's runner with this task's model credential."
	return []verse.Tool{{
		Name: askCreateTool,
		Description: "Ask somebody something, as the person this task runs for, as `verse ask <name> -q " +
			"<question>` and verse_ask_create do: the question, who is asked, when it settles if nobody " +
			"answers, and what follows a yes or a no, which runs on their authority. It does not wait: the " +
			"call answers the ask's id, and the ask stands after this task ends. To ask something and wait " +
			"for the answer yourself, use question-ask.",
		Params: map[string]any{
			"name":       text("what you call the ask; its id is derived from it and answered. Omit for this task's id"),
			"question":   text("what is asked, in the words the answerers read"),
			"to":         text("who is asked: a name or a comma-separated list, in the grammar a grant uses (user:ana@example.com, ana@example.com, group:leads); absent, anyone may answer"),
			"due":        text("when it settles if nobody answers: an RFC 3339 date or a duration from now (48h)"),
			"on_silence": text("with due: what it settles on if nobody answers by then, yes or no (the default); yes runs the then"),
			"then":       text("what follows a yes, on their authority, as a tony document of one key: {spawn: <a spawn, as a charter writes one>}, {answer: {ask: <id>, choice: <option>}} or {ask: {name: …, question: …}}." + held),
			"else":       text("what follows a no, or silence settling on no, on their authority; one key, as then takes." + held),
			"about":      text("the path the ask is about, as a kinded path"),
			"keep":       text("how long it stands once settled, as a duration (72h); absent, a week"),
			"label":      text("what the ask says about itself, name=value, comma-separated (team=payments,kind=deploy)"),
		},
		Required: []string{"question"},
		Step:     task.StepVerseAskCreate,
		Fields:   func(map[string]any) (map[string]string, bool) { return map[string]string{}, true },
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			question := strings.TrimSpace(str(tu.Input, "question"))
			if question == "" {
				return "not raised: an ask needs a question", true, nil
			}
			origin, _, _ := strings.Cut(tu.ID, task.GateSep)
			ask := map[string]any{"name": cmp.Or(strings.TrimSpace(str(tu.Input, "name")), origin), "question": question}
			for k, field := range map[string]string{"about": "about", "due": "due", "on_silence": "onSilence", "keep": "keep"} {
				if v := strings.TrimSpace(str(tu.Input, k)); v != "" {
					ask[field] = v
				}
			}
			// A due as a duration is a date from now, as `verse ask --due 48h` makes it: verse
			// keeps a date.
			if d, err := time.ParseDuration(strings.TrimSpace(str(tu.Input, "due"))); err == nil {
				ask["due"] = time.Now().Add(d).UTC().Format(time.RFC3339)
			}
			if to := listIn(tu.Input, "to"); len(to) > 0 {
				ask["to"] = to
			}
			for _, k := range []string{"then", "else"} {
				doc, err := docIn(tu.Input, k)
				if err != nil {
					return "not raised: " + err.Error(), true, nil
				}
				if doc != nil {
					ask[k] = doc
				}
			}
			labels, err := labelsIn(tu.Input, "label")
			if err != nil {
				return "not raised: " + err.Error(), true, nil
			}
			if len(labels) > 0 {
				ask["labels"] = labels
			}
			doc, err := codec.Marshal(ask, codec.Tony)
			if err != nil {
				return "not raised: " + err.Error(), true, nil
			}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseAskCreate, Ask: string(doc)}))
		},
	}}
}

// listIn is a list the call named under key, as verse_ask_create takes it, a comma-separated
// string, or as a list a script's verse-call-tool already made of it.
func listIn(in map[string]any, key string) []string {
	if s, ok := in[key].(string); ok {
		var out []string
		for _, v := range strings.Split(s, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	return verse.StrsOf(in, key)
}

// docIn is a document the call named under key: tony, as verse_ask_create takes it, or the object
// a script's verse-call-tool already read it as. nil where the call named none.
func docIn(in map[string]any, key string) (map[string]any, error) {
	switch v := in[key].(type) {
	case map[string]any:
		if len(v) == 0 {
			return nil, nil
		}
		return v, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, nil
		}
		var doc map[string]any
		if err := codec.Unmarshal([]byte(v), &doc, codec.Tony); err != nil || len(doc) == 0 {
			return nil, fmt.Errorf("%s is one key naming what runs: {spawn: …}, {answer: …} or {ask: …}", key)
		}
		return doc, nil
	}
	return nil, nil
}

// labelsIn is the labels the call named under key: name=value, comma-separated, as
// verse_ask_create takes them, or the object a script's verse-call-tool already made of them.
func labelsIn(in map[string]any, key string) (map[string]any, error) {
	switch v := in[key].(type) {
	case map[string]any:
		return v, nil
	case string:
		out := map[string]any{}
		for _, kv := range strings.Split(v, ",") {
			if kv = strings.TrimSpace(kv); kv == "" {
				continue
			}
			name, val, ok := strings.Cut(kv, "=")
			if !ok || strings.TrimSpace(name) == "" {
				return nil, fmt.Errorf("label %q is name=value", kv)
			}
			out[strings.TrimSpace(name)] = strings.TrimSpace(val)
		}
		return out, nil
	}
	return nil, nil
}
