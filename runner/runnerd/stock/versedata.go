package stock

import (
	"context"
	"fmt"
	"strings"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/verse"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/verbs"
)

// THE VERSE SYSTEM'S DATA VERBS: the verse at an address, read, written and removed as the person
// the task runs for, as `verse data` and verse_data, verse_data_put and verse_data_rm do. Each tool
// is defined once with its MCP tool and reads a call as it does (verbs.DataGetOf, DataPutOf), and
// the runner carries the request on the op's own route (delivery.OpVerseDataGet, OpVerseDataPut,
// OpVerseDataRm).

var (
	// dataGetTool is `verse data`, verse_data.
	dataGetTool = systems.Tool("verse", "data-get")
	// dataPutTool is `verse data <address> --put`, verse_data_put.
	dataPutTool = systems.Tool("verse", "data-put")
	// dataRmTool is `verse data <address> --rm`, verse_data_rm.
	dataRmTool = systems.Tool("verse", "data-rm")
)

// verseDataTools are the verse system's data tools.
func verseDataTools(cl *delivery.Client) []verse.Tool {
	return []verse.Tool{
		toolOf(verbs.DataGet, task.StepVerseDataGet, "not read", func(ctx context.Context, tu task.ToolUse, a verbs.Args) (string, bool, error) {
			r, err := verbs.DataGetOf(a)
			if err != nil {
				return "not read: " + err.Error(), true, nil
			}
			rep, err := cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseDataGet, Query: r.Query.Encode()})
			if err != nil || !rep.OK {
				return relay(rep, err)
			}
			node, err := codec.ParseNode([]byte(rep.Out), codec.Tony)
			if err != nil {
				return "", false, fmt.Errorf("the verse's answer does not read: %w", err)
			}
			// As verse_data answers it: a listing one kinded path per line, a document trimmed
			// at each node the address reached.
			if r.Paths {
				var lines []string
				for _, at := range api.ListedPaths(node, r.At, r.Depth) {
					lines = append(lines, at.KPath())
				}
				return strings.Join(lines, "\n"), false, nil
			}
			if r.Trim != nil {
				keep := func(n *ir.Node) (*ir.Node, bool, error) { return tony.Trim(r.Trim, n), true, nil }
				if node, err = (entity.Doc{Node: node}).Each(r.At, keep); err != nil {
					return "not read: trim: " + err.Error(), true, nil
				}
			}
			return tonyText(node)
		}),
		toolOf(verbs.DataPut, task.StepVerseDataPut, "not written", func(ctx context.Context, tu task.ToolUse, a verbs.Args) (string, bool, error) {
			body, err := verbs.DataPutOf(a)
			if err != nil {
				return "not written: " + err.Error(), true, nil
			}
			doc, err := codec.Marshal(body, codec.Tony)
			if err != nil {
				return "not written: " + err.Error(), true, nil
			}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseDataPut, Body: string(doc)}))
		}),
		toolOf(verbs.DataRm, task.StepVerseDataRm, "not removed", func(ctx context.Context, tu task.ToolUse, a verbs.Args) (string, bool, error) {
			segs, err := verbs.Address(a, "at")
			if err != nil {
				return "not removed: " + err.Error(), true, nil
			}
			q, at, err := api.RemoveQuery(segs)
			if err != nil {
				return "not removed: " + err.Error(), true, nil
			}
			rep, err := cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseDataRm, Query: q.Encode()})
			if err != nil || !rep.OK {
				return relay(rep, err)
			}
			return api.RemovedLine(at), false, nil
		}),
	}
}

// tonyText is a document as a tool answers it: tony, without a trailing newline.
func tonyText(node *ir.Node) (string, bool, error) {
	var b strings.Builder
	if err := codec.EncodeNode(&b, node, codec.Tony); err != nil {
		return "", false, err
	}
	return strings.TrimSpace(b.String()), false, nil
}
