package stock

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
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
			// Every parameter is a string, as verse_ask_create takes it; one that is not is
			// refused rather than read as nothing, which for `to` would put the ask to anyone.
			w, err := wordsOf(tu.Input)
			if err != nil {
				return "not raised: " + err.Error(), true, nil
			}
			question := strings.TrimSpace(w["question"])
			if question == "" {
				return "not raised: an ask needs a question", true, nil
			}
			origin, _, _ := strings.Cut(tu.ID, task.GateSep)
			// The words as `verse ask` and verse_ask_create read them (api.NewAsk).
			spec := api.AskSpec{Name: cmp.Or(strings.TrimSpace(w["name"]), origin), Question: question,
				To: w["to"], Due: w["due"], OnSilence: strings.TrimSpace(w["on_silence"]),
				About: strings.TrimSpace(w["about"]), Keep: strings.TrimSpace(w["keep"]), Label: w["label"]}
			if spec.Then, err = docOf("then", w["then"]); err != nil {
				return "not raised: " + err.Error(), true, nil
			}
			if spec.Else, err = docOf("else", w["else"]); err != nil {
				return "not raised: " + err.Error(), true, nil
			}
			ask, err := api.NewAsk(spec, time.Now())
			if err != nil {
				return "not raised: " + err.Error(), true, nil
			}
			doc, err := codec.Marshal(ask, codec.Tony)
			if err != nil {
				return "not raised: " + err.Error(), true, nil
			}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseAskCreate, Ask: string(doc)}))
		},
	}}
}

// wordsOf is the call's parameters as the words verse_ask_create takes: each a string, absent or
// null as none. A parameter that is anything else is refused, naming it.
func wordsOf(in map[string]any) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range slices.Sorted(maps.Keys(in)) {
		switch v := in[k].(type) {
		case nil:
		case string:
			out[k] = v
		default:
			return nil, fmt.Errorf("%s is a string, as verse_ask_create takes it, not %T", k, v)
		}
	}
	return out, nil
}

// docOf reads a then or an else: a tony document of one key naming what runs, as
// verse_ask_create takes it. nil where the call named none.
func docOf(key, src string) (entity.Payload, error) {
	if strings.TrimSpace(src) == "" {
		return nil, nil
	}
	n, err := codec.ParseNode([]byte(src), codec.Tony)
	if err != nil || !entity.IsObj(n) {
		return nil, fmt.Errorf("%s is one key naming what runs: {spawn: …}, {answer: …} or {ask: …}", key)
	}
	return n, nil
}
