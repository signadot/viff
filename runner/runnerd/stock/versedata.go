package stock

import (
	"context"
	"fmt"
	"maps"
	"slices"
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
)

// THE VERSE SYSTEM'S DATA VERBS: the verse at an address, read, written and removed as the person
// the task runs for, as `verse data` and verse_data, verse_data_put and verse_data_rm do. Each tool
// takes what its MCP tool takes, read by the same grammar (api.StateQuery, api.DataWrite,
// api.RemoveQuery), and the runner carries the request on the op's own route
// (delivery.OpVerseDataGet, OpVerseDataPut, OpVerseDataRm).

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
	text := func(what string) map[string]any { return map[string]any{"type": "string", "description": what} }
	flag := func(what string) map[string]any { return map[string]any{"type": "boolean", "description": what} }
	none := func(map[string]any) (map[string]string, bool) { return map[string]string{}, true }
	get := map[string]any{
		"at":    text("the address as a kinded path, as a document prints it: demo.pr.1, demo.pr.*.stage; empty is all of the verse they may read"),
		"if":    text("only entities whose payload matches this tony pattern, chosen by verse before anything is sent: {stage: open}"),
		"if_at": text("root the if pattern at this dot-separated field path inside the payload (labels)"),
		"trim":  text("write only the parts of each entity this match document names, as a key set: {id status}"),
		"paths": flag("answer one kinded path per line instead of the document"),
		"depth": map[string]any{"type": "integer", "description": "with paths: walk at most this many levels beneath the address; 0 is the address alone"},
	}
	put := map[string]any{
		"at":     text("where to write, as a kinded path: demo.pr.1, or demo.pr.1.labels.urgent for one field; no wildcards"),
		"value":  text("what to write there, a tony value: {stage: open}, 42, [a, b]. A document is merged into what stands"),
		"create": flag("only if nothing stands there yet: the write loses a race rather than clobbering"),
		"update": flag("only if something already stands there"),
		"expect": text("only if what stands there matches this tony pattern"),
		"grants": text("what the node this write creates holds out, as verse perms --grants takes it: [{read: [ana@example.com]}], or [] for nothing; refused where the node stands"),
	}
	rm := map[string]any{
		"at": text("what to remove, as a kinded path: an entity (demo.pr.1) or one field of one (demo.pr.1.labels.urgent); no wildcards"),
	}
	return []verse.Tool{{
		Name: dataGetTool,
		Description: "Read the verse at an address as the person this task runs for, as `verse data` and " +
			"verse_data do: the document stored there, or with paths the addresses beneath it. What is not " +
			"in their view, because nothing stands there or it is not theirs to read, is answered alike. " +
			"Where they may not read into the address at all, the read is refused, naming the nearest owner " +
			"on record, whom to ask.",
		Params: get, Required: []string{}, Step: task.StepVerseDataGet, Fields: none,
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			p, err := paramsOf(tu.Input, get)
			if err != nil {
				return "not read: " + err.Error(), true, nil
			}
			listing := p.flags["paths"]
			depth := entity.Unbounded
			if d, ok := p.nums["depth"]; ok {
				if !listing || d < 0 {
					return "not read: depth bounds a listing, so it goes with paths, and is never negative", true, nil
				}
				depth = d
			}
			q, kp, err := api.StateQuery(p.words["at"], p.words["if"], p.words["if_at"], listing, depth, codec.Tony)
			if err != nil {
				return "not read: " + err.Error(), true, nil
			}
			var trim *ir.Node
			if src := p.words["trim"]; src != "" {
				if trim, err = codec.ParseNode([]byte(src), codec.Tony); err != nil {
					return "not read: trim: " + err.Error(), true, nil
				}
			}
			rep, err := cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseDataGet, Query: q.Encode()})
			if err != nil || !rep.OK {
				return relay(rep, err)
			}
			node, err := codec.ParseNode([]byte(rep.Out), codec.Tony)
			if err != nil {
				return "", false, fmt.Errorf("the verse's answer does not read: %w", err)
			}
			// As verse_data answers it: a listing one kinded path per line, a document trimmed
			// at each node the address reached.
			if listing {
				var lines []string
				for _, at := range api.ListedPaths(node, kp, depth) {
					lines = append(lines, at.KPath())
				}
				return strings.Join(lines, "\n"), false, nil
			}
			if trim != nil {
				keep := func(n *ir.Node) (*ir.Node, bool, error) { return tony.Trim(trim, n), true, nil }
				if node, err = (entity.Doc{Node: node}).Each(kp, keep); err != nil {
					return "not read: trim: " + err.Error(), true, nil
				}
			}
			return tonyText(node)
		},
	}, {
		Name: dataPutTool,
		Description: "Write a value at an address as the person this task runs for, as `verse data <address> " +
			"--put` and verse_data_put do: merged into what stands there, on their authority; with create, " +
			"update or expect, only on that condition. Answers what stands there after the write.",
		Params: put, Required: []string{"at", "value"}, Step: task.StepVerseDataPut, Fields: none,
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			p, err := paramsOf(tu.Input, put)
			if err != nil {
				return "not written: " + err.Error(), true, nil
			}
			if strings.TrimSpace(p.words["value"]) == "" {
				return "not written: value is what is written there", true, nil
			}
			segs, err := entity.PathSegs(strings.TrimSpace(p.words["at"]))
			if err != nil {
				return "not written: at: " + err.Error(), true, nil
			}
			body, err := api.DataWrite{Payload: p.words["value"], Create: p.flags["create"], Update: p.flags["update"],
				Expect: p.words["expect"], Grants: p.words["grants"]}.Body(segs, codec.Tony)
			if err != nil {
				return "not written: " + err.Error(), true, nil
			}
			doc, err := codec.Marshal(body, codec.Tony)
			if err != nil {
				return "not written: " + err.Error(), true, nil
			}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseDataPut, Body: string(doc)}))
		},
	}, {
		Name: dataRmTool,
		Description: "Remove what stands at an address as the person this task runs for, an entity or one " +
			"field of one, as `verse data <address> --rm` and verse_data_rm do.",
		Params: rm, Required: []string{"at"}, Step: task.StepVerseDataRm, Fields: none,
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			p, err := paramsOf(tu.Input, rm)
			if err != nil {
				return "not removed: " + err.Error(), true, nil
			}
			segs, err := entity.PathSegs(strings.TrimSpace(p.words["at"]))
			if err != nil {
				return "not removed: at: " + err.Error(), true, nil
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
		},
	}}
}

// params is a call's parameters as its tool's schema types them: strings, booleans and integers.
type params struct {
	words map[string]string
	flags map[string]bool
	nums  map[string]int
}

// paramsOf reads in by schema, each parameter as the type it says, absent or null as none. One of
// another type, or one the tool does not take, is refused, naming it, rather than read as nothing.
func paramsOf(in, schema map[string]any) (params, error) {
	p := params{words: map[string]string{}, flags: map[string]bool{}, nums: map[string]int{}}
	for _, k := range slices.Sorted(maps.Keys(in)) {
		v := in[k]
		if v == nil {
			continue
		}
		s, ok := schema[k].(map[string]any)
		if !ok {
			return p, fmt.Errorf("%s is not a parameter this tool takes", k)
		}
		switch s["type"] {
		case "string":
			w, ok := v.(string)
			if !ok {
				return p, fmt.Errorf("%s is a string, as verse mcp's tool takes it, not %T", k, v)
			}
			p.words[k] = w
		case "boolean":
			b, ok := v.(bool)
			if !ok {
				return p, fmt.Errorf("%s is a boolean, as verse mcp's tool takes it, not %T", k, v)
			}
			p.flags[k] = b
		case "integer":
			n, ok := numberOf(v)
			if !ok {
				return p, fmt.Errorf("%s is an integer, as verse mcp's tool takes it, not %v", k, v)
			}
			p.nums[k] = n
		}
	}
	return p, nil
}

// numberOf is an integer however it arrived: an int, the codec's int64, or a float that is one.
func numberOf(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), n == float64(int(n))
	}
	return 0, false
}

// tonyText is a document as a tool answers it: tony, without a trailing newline.
func tonyText(node *ir.Node) (string, bool, error) {
	var b strings.Builder
	if err := codec.EncodeNode(&b, node, codec.Tony); err != nil {
		return "", false, err
	}
	return strings.TrimSpace(b.String()), false, nil
}
