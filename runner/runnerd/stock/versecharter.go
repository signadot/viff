package stock

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/charterfile"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/verse"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// THE VERSE SYSTEM'S CHARTER VERBS that read: the charters read, and asked whether some would
// install, as `verse charter` and verse_charter and verse_charter_check do. Each tool takes what
// its MCP tool takes, and reads, filters and answers through the same code (charterfile,
// api.Filter), carried by the runner on the op's own route (delivery.OpVerseCharterGet,
// OpVerseCharterCheck).

var (
	// charterGetTool is `verse charter`, verse_charter.
	charterGetTool = systems.Tool("verse", "charter-get")
	// charterCheckTool is `verse charter --check`, verse_charter_check.
	charterCheckTool = systems.Tool("verse", "charter-check")
)

// verseCharterTools are the verse system's charter tools.
func verseCharterTools(cl *delivery.Client) []verse.Tool {
	text := func(what string) map[string]any { return map[string]any{"type": "string", "description": what} }
	none := func(map[string]any) (map[string]string, bool) { return map[string]string{}, true }
	get := map[string]any{
		"path":  text("the charter's path as a kinded path: an address (verse-dev.drift), then a reach into what stands there (verse-dev.drift.rules.watcher); empty is baseline's charter, and what is under it"),
		"at":    text("where the address ends, when verse cannot tell: a package whose address ends in rules, strata or schedules. path is then read beneath it"),
		"depth": map[string]any{"type": "integer", "description": "how far under the address to go: 0 is the address alone, 1 adds its immediate children; without it, everything"},
		"paths": map[string]any{"type": "boolean", "description": "answer one kinded path per rule and level instead of the charter, each a path this tool takes back"},
		"if":    text("keep only the parts matching this tony pattern"),
		"trim":  text("write only the parts this match document names, as a key set: {rules}"),
	}
	check := map[string]any{
		"at":       text("the address the charters would be installed at, as a kinded path, as an install with a prune would take it: what stands at and under it and the charters leave out would go"),
		"charters": text("the charters, in tony, each a document saying its own at: (at or under the address) with reads, ttl, strata, schedules and rules as a charter file does; several are separated by lines of ---"),
	}
	return []verse.Tool{{
		Name: charterGetTool,
		Description: "Read the charter at a path as the person this task runs for would, as `verse charter` " +
			"and verse_charter do: an installed charter as its file (at, reads, ttl, strata, schedules, rules), a " +
			"rule or level of one as it stands, or with paths one line per rule and level. What the file " +
			"cannot say follows it as notes: a rule held, suspended or in trouble, and the charters under " +
			"the address.",
		Params: get, Required: []string{}, Step: task.StepVerseCharterGet, Fields: none,
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			p, err := paramsOf(tu.Input, get)
			if err != nil {
				return "not read: " + err.Error(), true, nil
			}
			addr, err := entity.PathSegs(p.words["at"])
			if err != nil {
				return "not read: at: " + err.Error(), true, nil
			}
			reach, err := entity.PathSegs(p.words["path"])
			if err != nil {
				return "not read: path: " + err.Error(), true, nil
			}
			split := -1
			if p.words["at"] != "" {
				split = len(addr)
			}
			depth := -1
			if d, ok := p.nums["depth"]; ok {
				if d < 0 {
					return "not read: depth is never negative: without it, everything under the address", true, nil
				}
				depth = d
			}
			keep, err := matchDoc("if", p.words["if"])
			if err != nil {
				return "not read: " + err.Error(), true, nil
			}
			trim, err := matchDoc("trim", p.words["trim"])
			if err != nil {
				return "not read: " + err.Error(), true, nil
			}
			q, full := charterfile.Query(append(addr, reach...), split, depth)
			rep, err := cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseCharterGet, Query: q.Encode()})
			if err != nil || !rep.OK {
				return relay(rep, err)
			}
			node, err := codec.ParseNode([]byte(rep.Out), codec.Tony)
			if err != nil {
				return "", false, fmt.Errorf("the verse's answer does not read: %w", err)
			}
			// As verse_charter answers it: a listing one kinded path per line, a reach into a
			// charter as it stands, and a charter as its file with the notes after it.
			if p.flags["paths"] {
				paths, err := charterfile.Paths(node, full)
				if err != nil {
					return "not read: " + err.Error(), true, nil
				}
				lines := make([]string, 0, len(paths))
				for _, at := range paths {
					lines = append(lines, at.KPath())
				}
				return strings.Join(lines, "\n"), false, nil
			}
			f := api.Filter{Keep: keep, Trim: trim}
			if !charterfile.IsNode(node) {
				if node, err = f.Apply(node); err != nil {
					return "not read: " + err.Error(), true, nil
				}
				return tonyText(node)
			}
			files, notes, under, err := charterfile.Read(node, full, "", true)
			if err != nil {
				return "not read: " + err.Error(), true, nil
			}
			var parts []string
			for _, file := range files {
				if file, err = f.Apply(file); err != nil {
					return "not read: " + err.Error(), true, nil
				}
				out, _, err := tonyText(file)
				if err != nil {
					return "", false, err
				}
				parts = append(parts, out)
			}
			if len(under) > 0 {
				here := "here"
				if full != "" {
					here = charterfile.Spelled(full, true)
				}
				notes = append(notes, charterfile.UnderNote(here, under))
			}
			return strings.Join(append(parts, notes...), "\n"), false, nil
		},
	}, {
		Name: charterCheckTool,
		Description: "Ask whether charters would install at an address, as `verse charter --check` and " +
			"verse_charter_check do: each charter's verdict, and what the install would add, change and " +
			"take away. Nothing is installed; a refusal is the answer, not an error.",
		Params: check, Required: []string{"at", "charters"}, Step: task.StepVerseCharterCheck, Fields: none,
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			p, err := paramsOf(tu.Input, check)
			if err != nil {
				return "not checked: " + err.Error(), true, nil
			}
			segs, err := entity.PathSegs(strings.TrimSpace(p.words["at"]))
			if err != nil {
				return "not checked: at: " + err.Error(), true, nil
			}
			at := entity.JoinSegs(segs)
			if at == "" {
				return "not checked: at: a check names the address the charters would be installed at", true, nil
			}
			docs, err := charterfile.CheckDocs(p.words["charters"], codec.Tony)
			if err != nil {
				return "not checked: " + err.Error(), true, nil
			}
			// One document apiece, as `verse charter --check` sends them (client.PostNodes).
			var body bytes.Buffer
			for i, d := range docs {
				if i > 0 {
					body.WriteString("---\n")
				}
				b, err := codec.NodeBytes(d, codec.Tony)
				if err != nil {
					return "not checked: " + err.Error(), true, nil
				}
				body.Write(b)
				if !bytes.HasSuffix(b, []byte("\n")) {
					body.WriteByte('\n')
				}
			}
			q := url.Values{"at": {at}}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseCharterCheck,
				Query: q.Encode(), Body: body.String()}))
		},
	}}
}

// matchDoc is a match document a call gives for name (if, trim), or nil for none.
func matchDoc(name, src string) (*ir.Node, error) {
	if strings.TrimSpace(src) == "" {
		return nil, nil
	}
	n, err := codec.ParseNode([]byte(src), codec.Tony)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}
