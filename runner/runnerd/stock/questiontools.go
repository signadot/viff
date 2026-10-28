package stock

import (
	"cmp"
	"context"
	"strings"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/verse"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// A TASK RAISES AN ASK AS ITS OWNER DOES WITH `verse ask` (`question-raise`, delivery.OpQuestionRaise):
// the same words, whom it is put to, its clock, and what a yes and a no start, carried by the
// runner to verse over the task's channel. It does not wait: the ask outlives the task, and its
// `then` and `else` run as the task's owner when it settles, which is what a stored continuation
// is for. A conversation's turn is `question-ask`, which waits and goes with the task.
var raiseTool = systems.Tool("question", "raise")

// raiseTools are the question tools a task has where the runner reaches its verse with a token of
// its own.
func raiseTools(cl *delivery.Client) []verse.Tool {
	obj := func(what string) map[string]any { return map[string]any{"type": "object", "description": what} }
	strs := func(what string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": what}
	}
	text := func(what string) map[string]any { return map[string]any{"type": "string", "description": what} }
	follow := "one key: {spawn: {prompt or script, agent, environment, autonomy}}, a task started on the " +
		"answer; {answer: {ask, choice, say}}, an answer to another ask; or {ask: {…}}, another ask raised then. " +
		"It runs as the person this task runs for. A spawn reaches no further than this task does, its " +
		"autonomy is no looser, and it runs on this task's runner with this task's model credential"
	return []verse.Tool{{
		Name: raiseTool,
		Description: "Raise an ask for the person this task runs for, as they would with `verse ask`, and go on " +
			"without waiting: the call answers the ask's id. The ask stands on the docket after this task " +
			"ends, until it is answered or its `due` passes. Use `then` for what a yes starts and `else` for " +
			"what a no starts; verse runs them when the ask settles. To ask something and wait for the " +
			"answer yourself, use question-ask instead.",
		Params: map[string]any{
			"name":      text("a short name the ask's id is made from (deploy-ok); omit for this task's id"),
			"question":  text("what is asked, as a yes-or-no question"),
			"to":        strs("whom it is put to, as grantees (user:ana@example.com, group:release); omit for whoever may answer what it is about"),
			"about":     text("the address it is about, where it is about something in verse"),
			"due":       text("when it stops waiting: a duration (48h) or a time (RFC3339)"),
			"onSilence": text("what it settles on when `due` passes and nobody answered: no (the default) or yes"),
			"keep":      text("how long it stands once settled, as a duration; omit for a week"),
			"labels":    obj("what the ask says about itself for whoever watches asks (team: payments)"),
			"then":      obj("what a yes starts: " + follow),
			"else":      obj("what a no starts, a refusal or silence settling on no: " + follow),
		},
		Required: []string{"question"},
		Step:     task.StepQuestionRaise,
		Fields:   func(map[string]any) (map[string]string, bool) { return map[string]string{}, true },
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			question := strings.TrimSpace(str(tu.Input, "question"))
			if question == "" {
				return "not raised: an ask needs a question", true, nil
			}
			origin, _, _ := strings.Cut(tu.ID, task.GateSep)
			ask := map[string]any{"name": cmp.Or(strings.TrimSpace(str(tu.Input, "name")), origin), "question": question}
			for _, k := range []string{"about", "due", "onSilence", "keep"} {
				if v := strings.TrimSpace(str(tu.Input, k)); v != "" {
					ask[k] = v
				}
			}
			// A due as a duration is a date from now, as `verse ask --due 48h` makes it: verse
			// keeps a date.
			if d, err := time.ParseDuration(str(tu.Input, "due")); err == nil {
				ask["due"] = time.Now().Add(d).UTC().Format(time.RFC3339)
			}
			if to := verse.StrsOf(tu.Input, "to"); len(to) > 0 {
				ask["to"] = to
			}
			for _, k := range []string{"labels", "then", "else"} {
				if v, ok := tu.Input[k].(map[string]any); ok && len(v) > 0 {
					ask[k] = v
				}
			}
			doc, err := codec.Marshal(ask, codec.Tony)
			if err != nil {
				return "not raised: " + err.Error(), true, nil
			}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpQuestionRaise, Ask: string(doc)}))
		},
	}}
}
