package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/signadot/verse/task"
)

// askTool is how a task hands the turn to a person. The model calls it with what it wants to
// know; the task blocks on a question — the same record a gate poses, on the same docket,
// answered from the same places — and the answer is the tool result the model reads next.
//
// It is a tool rather than a way of stopping because a stop is read as SUCCESS by everything
// downstream (see yield): a model that answered a question and stopped has ended its task, with
// the person's reply arriving at nothing. A conversation is a run of asks, and it ends when the
// model stops without one.
//
// It is the loop's own wait. A question posted through the resolver is withdrawn when the
// context ends, orphaned with its task on a restart, bounded by its own deadline, relayed to
// every runner, and listed for whoever answers gates — none of which this tool has to do.
const askTool = "ask"

// replyOption is the one outcome a question has when the model named none: an answer in words.
const replyOption = "reply"

// askFor builds the tool over the resolver a task's questions go to. The tool result names who
// answered, because the model is reading somebody's words and should know whose.
func askFor(resolver task.Resolver) Tool {
	return Tool{
		Name: askTool,
		Description: "Ask the person this task is running for something, and wait for their " +
			"answer; it is this tool's result. Your turn pauses until they reply, which may take " +
			"a while. Use it when you need a decision or information only they have — and, when " +
			"the task is a conversation, after each answer, to hand them the turn. `options` " +
			"lists outcomes for them to choose between; without it they answer in words.",
		Params: map[string]any{
			"question": map[string]any{"type": "string",
				"description": "what you want to know, or say before handing over the turn"},
			"options": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
				"description": "outcomes to choose between, by short id; omit for an answer in words"},
		},
		Required: []string{"question"},
		// One effect, with the verb that says what this is: a question, which no grant bounds
		// and no vector gates (task.ToolUse.IsQuestion).
		Derive: func(tu *task.ToolUse) {
			tu.Effects = []task.Effect{{Verb: task.Question}}
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			question := strings.TrimSpace(str(tu.Input, "question"))
			if question == "" {
				return "", false, errors.New("ask: a question is required")
			}
			opts := optionsOf(tu.Input)
			// The action's own id, which already carries the task's as its prefix — how an
			// answer finds the task that asked — and cannot collide with the supervisor's
			// `~d<n>` decisions.
			origin, _, _ := strings.Cut(tu.ID, task.GateSep)
			q := task.GateQuestion{
				ID: tu.ID, Origin: origin, Gate: askTool, Mode: task.Ask,
				Reason: question, Options: opts,
			}
			res, err := resolver.Resolve(ctx, q, tu)
			if err != nil {
				return "", false, err // the context ended: the question has left the docket
			}
			return answerText(res), false, nil
		},
	}
}

// optionsOf reads the outcomes the model named, or the one outcome an unnamed question has.
func optionsOf(in map[string]any) []task.Option {
	var out []task.Option
	if raw, ok := in["options"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, task.Option{ID: strings.TrimSpace(s)})
			}
		}
	}
	if len(out) == 0 {
		return []task.Option{{ID: replyOption, Says: "answer in words"}}
	}
	return out
}

// answerText renders an answer as the model reads it: who, and what they said or chose.
func answerText(res task.Resolution) string {
	words := strings.TrimSpace(strings.Join(res.Steer, "\n"))
	switch {
	case res.Choice == "" || res.Choice == replyOption:
		if words == "" {
			return res.By + " answered with no words"
		}
		return res.By + ": " + words
	case words != "":
		return fmt.Sprintf("%s chose %s: %s", res.By, res.Choice, words)
	default:
		return fmt.Sprintf("%s chose %s", res.By, res.Choice)
	}
}
