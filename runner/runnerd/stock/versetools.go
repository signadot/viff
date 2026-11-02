package stock

import (
	"context"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/verse"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/verbs"
)

// THE VERSE SYSTEM is verse itself, acted on by a task as the person it runs for would act on it:
// each verb is a verb of `verse` and of `verse mcp`, defined once for both (verbs): its tool takes
// what verse mcp's takes, read and refused alike, carried by the runner to verse's API over the
// task's channel (delivery.OpVerseAskCreate, OpVerseAskGet, OpVerseAskList) and admitted as the
// task's owner. A task's own questions, which it waits on as a task, are the question system's
// (verse.Ask).

// askCreateTool is `verse ask [<name>] -q <question>`, verse_ask_create: an ask raised as the
// person the task runs for. It does not wait: the ask outlives the task, and the rows of its `on`
// run as that person when it settles, which is what a stored continuation is for.
var askCreateTool = systems.Tool("verse", verbs.AskCreate.Name)

// askGetTool is `verse ask <name|id>`, verse_ask_get: one of that person's asks read back,
// so a task that raised one sees whether it was answered.
var askGetTool = systems.Tool("verse", verbs.AskGet.Name)

// askListTool is `verse ask`, verse_ask_list: all of that person's asks.
var askListTool = systems.Tool("verse", verbs.AskList.Name)

// verseTools are the verse system's ask tools, where the runner reaches its verse with a token of
// its own.
func verseTools(cl *delivery.Client) []verse.Tool {
	return []verse.Tool{
		toolOf(verbs.AskCreate, task.StepVerseAskCreate, "not raised", func(ctx context.Context, tu task.ToolUse, a verbs.Args) (string, bool, error) {
			ask, err := verbs.AskOf(a, time.Now())
			if err != nil {
				return "not raised: " + err.Error(), true, nil
			}
			doc, err := codec.Marshal(ask, codec.Tony)
			if err != nil {
				return "not raised: " + err.Error(), true, nil
			}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseAskCreate, Ask: string(doc)}))
		}),
		toolOf(verbs.AskGet, task.StepVerseAskGet, "not read", func(ctx context.Context, tu task.ToolUse, a verbs.Args) (string, bool, error) {
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseAskGet, Ask: a.Word("name_or_id")}))
		}),
		toolOf(verbs.AskList, task.StepVerseAskList, "not read", func(ctx context.Context, tu task.ToolUse, a verbs.Args) (string, bool, error) {
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseAskList}))
		}),
	}
}

// toolOf is a verb of the verse system as a task is offered it: its name, description and
// parameters from the definition verse mcp's tool is built from too, and step, what each use is.
// A call verbs.Read refuses is refused, its words after not; do is given the rest. A use has no
// fields unless the tool says them (withFields).
func toolOf(v *verbs.Verb, step, not string, do func(context.Context, task.ToolUse, verbs.Args) (string, bool, error)) verse.Tool {
	return verse.Tool{
		Name:        systems.Tool("verse", v.Name),
		Description: v.Description(verbs.Task),
		Params:      v.Properties(verbs.Task),
		Required:    v.Required(),
		Step:        step,
		Fields:      func(map[string]any) (map[string]string, bool) { return map[string]string{}, true },
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			a, err := v.Read(tu.Input)
			if err != nil {
				return not + ": " + err.Error(), true, nil
			}
			return do(ctx, tu, a)
		},
	}
}
