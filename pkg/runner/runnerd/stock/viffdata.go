package stock

import (
	"fmt"
	"net/url"
	"strings"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/runner/runnerd/delivery"
	"go.viff.dev/viff/pkg/runner/runnerd/viff"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
	"go.viff.dev/viff/pkg/verbs"
)

// THE VIFF SYSTEM'S DATA VERBS: the store at an address, read, written and removed as the person
// the task runs for, as `viff data` and viff_data, viff_data_put and viff_data_rm do. Each tool
// is defined once with its MCP tool and reads a call as it does (verbs.DataGetOf, DataPutOf), and
// the runner carries the request on the op's own route (delivery.OpViffDataGet, OpViffDataPut,
// OpViffDataRm).

var (
	// dataGetTool is `viff data`, viff_data.
	dataGetTool = systems.Tool("viff", "data-get")
	// dataPutTool is `viff data <address> --put`, viff_data_put.
	dataPutTool = systems.Tool("viff", "data-put")
	// dataRmTool is `viff data <address> --rm`, viff_data_rm.
	dataRmTool = systems.Tool("viff", "data-rm")
)

// viffDataTools are the viff system's data tools.
func viffDataTools(cl *delivery.Client) []viff.Tool {
	return []viff.Tool{
		toolOf(cl, verbs.DataGet, task.StepViffDataGet, "not read", func(a verbs.Args) (delivery.Request, error) {
			r, err := verbs.DataGetOf(a)
			if err != nil {
				return delivery.Request{}, err
			}
			return delivery.Request{Op: delivery.OpViffDataGet, Fields: map[string]string{"query": r.Query.Encode()}}, nil
		}, func(a verbs.Args, rep delivery.Reply) (string, bool, error) {
			r, err := verbs.DataGetOf(a)
			if err != nil {
				return "not read: " + err.Error(), true, nil
			}
			node, err := codec.ParseNode([]byte(rep.Out), codec.Tony)
			if err != nil {
				return "", false, fmt.Errorf("the viff instance's answer does not read: %w", err)
			}
			// As viff_data answers it: a listing one kinded path per line, a document trimmed
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
		toolOf(cl, verbs.DataPut, task.StepViffDataPut, "not written", func(a verbs.Args) (delivery.Request, error) {
			body, err := verbs.DataPutOf(a)
			if err != nil {
				return delivery.Request{}, err
			}
			doc, err := codec.Marshal(body, codec.Tony)
			if err != nil {
				return delivery.Request{}, err
			}
			return delivery.Request{Op: delivery.OpViffDataPut, Body: string(doc)}, nil
		}, nil),
		toolOf(cl, verbs.DataRm, task.StepViffDataRm, "not removed", func(a verbs.Args) (delivery.Request, error) {
			q, _, err := removal(a)
			if err != nil {
				return delivery.Request{}, err
			}
			return delivery.Request{Op: delivery.OpViffDataRm, Fields: map[string]string{"query": q.Encode()}}, nil
		}, func(a verbs.Args, _ delivery.Reply) (string, bool, error) {
			_, at, err := removal(a)
			if err != nil {
				return "not removed: " + err.Error(), true, nil
			}
			return api.RemovedLine(at), false, nil
		}),
	}
}

// removal is the removal a data-rm call makes, as `viff data --rm` makes it (api.RemoveQuery),
// and the place it names.
func removal(a verbs.Args) (url.Values, entity.Path, error) {
	segs, err := verbs.Address(a, "at")
	if err != nil {
		return nil, entity.Path{}, err
	}
	return api.RemoveQuery(segs)
}

// tonyText is a document as a tool answers it: tony, without a trailing newline.
func tonyText(node *ir.Node) (string, bool, error) {
	var b strings.Builder
	if err := codec.EncodeNode(&b, node, codec.Tony); err != nil {
		return "", false, err
	}
	return strings.TrimSpace(b.String()), false, nil
}
