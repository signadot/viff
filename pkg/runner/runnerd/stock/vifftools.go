package stock

import (
	"context"
	"time"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/runner/runnerd/delivery"
	"go.viff.dev/viff/pkg/runner/runnerd/viff"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
	"go.viff.dev/viff/pkg/verbs"
)

// THE VIFF SYSTEM is viff itself, acted on by a task as the person it runs for would act on it:
// each verb is a verb of `viff` and of `viff mcp`, defined once for both (verbs): its tool takes
// what viff mcp's takes, read and refused alike, carried by the runner to viff's API over the
// task's channel (delivery.OpViffAskCreate, OpViffAskGet, OpViffAskList) and admitted as the
// task's owner. A task's own questions, which it waits on as a task, are the question system's
// (viff.Ask).

// askCreateTool is `viff ask [<name>] -q <question>`, viff_ask_create: an ask raised as the
// person the task runs for. It does not wait: the ask outlives the task, and the rows of its `on`
// run as that person when it settles, which is what a stored continuation is for.
var askCreateTool = systems.Tool("viff", verbs.AskCreate.Name)

// askGetTool is `viff ask <name|id>`, viff_ask_get: one of that person's asks read back,
// so a task that raised one sees whether it was answered.
var askGetTool = systems.Tool("viff", verbs.AskGet.Name)

// askListTool is `viff ask`, viff_ask_list: all of that person's asks.
var askListTool = systems.Tool("viff", verbs.AskList.Name)

// viffTools are the viff system's ask tools, where the runner reaches its viff with a token of
// its own.
func viffTools(cl *delivery.Client) []viff.Tool {
	return []viff.Tool{
		toolOf(cl, verbs.AskCreate, task.StepViffAskCreate, "not raised", func(a verbs.Args) (delivery.Request, error) {
			ask, err := verbs.AskOf(a, time.Now())
			if err != nil {
				return delivery.Request{}, err
			}
			doc, err := codec.Marshal(ask, codec.Tony)
			if err != nil {
				return delivery.Request{}, err
			}
			return delivery.Request{Op: delivery.OpViffAskCreate, Fields: map[string]string{"ask": string(doc)}}, nil
		}, nil),
		toolOf(cl, verbs.AskGet, task.StepViffAskGet, "not read", func(a verbs.Args) (delivery.Request, error) {
			return delivery.Request{Op: delivery.OpViffAskGet, Fields: map[string]string{"ask": a.Word("name_or_id")}}, nil
		}, nil),
		toolOf(cl, verbs.AskList, task.StepViffAskList, "not read", func(verbs.Args) (delivery.Request, error) {
			return delivery.Request{Op: delivery.OpViffAskList}, nil
		}, nil),
	}
}

// toolOf is a verb of the viff system as a task is offered it: its name, description and
// parameters from the definition viff mcp's tool is built from too, and step, what each use is.
// request is the request to viff a use is, from the call verbs.Read reads, and answer what the
// tool says of viff's reply where it is not the reply itself. A call verbs.Read or request
// refuses is refused, its words after not.
//
// THE TASK DECIDES THE STEP THE RUNNER WILL: each use's step is the one delivery.StepOf makes of
// its request, as the runner makes it when the request arrives, so a row on an address or an
// ask's `on` decides the same step on both sides.
func toolOf(cl *delivery.Client, v *verbs.Verb, step, not string, request func(verbs.Args) (delivery.Request, error),
	answer func(verbs.Args, delivery.Reply) (string, bool, error)) viff.Tool {
	read := func(in map[string]any) (verbs.Args, delivery.Request, error) {
		a, err := v.Read(in)
		if err != nil {
			return verbs.Args{}, delivery.Request{}, err
		}
		req, err := request(a)
		return a, req, err
	}
	return viff.Tool{
		Name:        systems.Tool("viff", v.Name),
		Description: v.Description(verbs.Task),
		Params:      v.Properties(verbs.Task),
		Required:    v.Required(),
		Step:        step,
		Build: func(in map[string]any) (task.Step, bool) {
			_, req, err := read(in)
			if err != nil {
				return task.Step{}, false
			}
			st, why := delivery.StepOf(req)
			return st, why == ""
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			a, req, err := read(tu.Input)
			if err != nil {
				return not + ": " + err.Error(), true, nil
			}
			req.Decision = tu.Decision
			rep, err := cl.Ask(req)
			if err != nil || !rep.OK || answer == nil {
				return relay(rep, err)
			}
			return answer(a, rep)
		},
	}
}
