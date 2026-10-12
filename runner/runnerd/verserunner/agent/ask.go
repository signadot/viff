package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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
//
// ITS CLOCK IS THE MODEL'S TO SET. `within` bounds the wait, and with options `default` is what
// silence settles on; at the deadline the model reads that nobody answered, and goes on. Without
// `within` the question waits until it is answered, which is what a conversation needs: a person
// is handed the turn and may take it whenever they come back.
const askTool = "ask"

// replyOption is the one outcome a question has when the model named none: an answer in words.
const replyOption = "reply"

// askFor builds the tool over the resolver a task's questions go to. The tool result names who
// answered, because the model is reading somebody's words and should know whose.
func askFor(resolver task.Resolver) Tool {
	return Tool{
		Name: askTool,
		Description: "Say something to the person this task is running for and wait for their " +
			"reply; it is this tool's result. Your turn pauses until they answer, which may take " +
			"a while. Use it whenever you need a decision or information only they have, and in " +
			"a conversation after EVERY answer, to hand them the turn — stopping instead ends the " +
			"task. `options` lists outcomes for them to choose between; without it they reply in " +
			"words.",
		Params: map[string]any{
			"question": map[string]any{"type": "string",
				"description": "what you want to know, or say before handing over the turn"},
			"options": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
				"description": "outcomes to choose between, by short id; omit for an answer in words"},
			"within": map[string]any{"type": "string",
				"description": "how long to wait for an answer, as a duration (30m, 24h); omit to wait until they answer"},
			"default": map[string]any{"type": "string",
				"description": "with options and within: the option taken if nobody answers in time"},
			"answerers": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
				"description": "who may answer, as grantees (user:ana@example.com, user:*, group:owners); omit for the person this task runs for"},
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
			deadline, err := deadlineOf(tu.Input, opts)
			if err != nil {
				return "", false, err
			}
			// The action's own id, which already carries the task's as its prefix — how an
			// answer finds the task that asked — and cannot collide with the supervisor's
			// `~d<n>` decisions.
			origin, _, _ := strings.Cut(tu.ID, task.GateSep)
			// WHOM IT IS HELD OUT TO: the person this task runs for, unless the call names
			// answerers. A charter's call list asks for a party the charter names, which for
			// a system charter is the one way its question reaches anybody: the system is
			// nobody's docket.
			answerers := strsOf(tu.Input, "answerers")
			if len(answerers) == 0 {
				answerers = []string{task.PrincipalAnswerer}
			}
			q := task.GateQuestion{
				ID: tu.ID, Origin: origin, Gate: askTool, Mode: task.Ask,
				Reason: question, Options: opts, Deadline: deadline,
				Answerers: answerers,
			}
			res, err := resolver.Resolve(ctx, q, tu)
			if err != nil {
				return "", false, err // the context ended: the question has left the docket
			}
			return answerText(res, deadline), false, nil
		},
	}
}

// strsOf reads a list of strings the call named under key, blank ones dropped.
func strsOf(in map[string]any, key string) []string {
	var out []string
	if raw, ok := in[key].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out
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

// deadlineOf reads the clock the model set: nil for none, which waits until the question is
// answered. A default is an option the question offers, and only means anything with a clock.
func deadlineOf(in map[string]any, opts []task.Option) (*task.Deadline, error) {
	within, dflt := strings.TrimSpace(str(in, "within")), strings.TrimSpace(str(in, "default"))
	if within == "" {
		if dflt != "" {
			return nil, errors.New("ask: a default is what silence settles on, so it needs within")
		}
		return nil, nil
	}
	if d, err := time.ParseDuration(within); err != nil || d <= 0 {
		return nil, fmt.Errorf("ask: within %q: how long to wait, as a duration (30m, 24h)", within)
	}
	if dflt != "" && !slices.ContainsFunc(opts, func(o task.Option) bool { return o.ID == dflt && o.ID != replyOption }) {
		return nil, fmt.Errorf("ask: default %q is not one of the options", dflt)
	}
	return &task.Deadline{Within: within, Default: dflt}, nil
}

// answerText renders an answer as the model reads it: who, and what they said or chose; or, where
// the clock ran out, that nobody answered, and what that settled on.
func answerText(res task.Resolution, d *task.Deadline) string {
	words := strings.TrimSpace(strings.Join(res.Steer, "\n"))
	if res.By == task.ByDeadline {
		within := ""
		if d != nil {
			within = " within " + d.Within
		}
		if res.Choice == "" || res.Choice == task.NoOption {
			return "nobody answered" + within
		}
		return fmt.Sprintf("nobody answered%s, so it settled on %s", within, res.Choice)
	}
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
