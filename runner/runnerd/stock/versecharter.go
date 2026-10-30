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

// THE VERSE SYSTEM'S CHARTER VERBS: the charters read, asked whether some would install,
// installed and removed, as `verse charter` and verse_charter, _check, _install and _remove do.
// Each tool takes what its MCP tool takes, and reads, filters and answers through the same code
// (charterfile, api.Filter), carried by the runner on the op's own route
// (delivery.OpVerseCharterGet, OpVerseCharterCheck, OpVerseCharterInstall,
// OpVerseCharterRemove). What a task installs, verse holds within the task.

var (
	// charterGetTool is `verse charter`, verse_charter.
	charterGetTool = systems.Tool("verse", "charter-get")
	// charterCheckTool is `verse charter --check`, verse_charter_check.
	charterCheckTool = systems.Tool("verse", "charter-check")
	// charterInstallTool is `verse charter -a`, verse_charter_install.
	charterInstallTool = systems.Tool("verse", "charter-install")
	// charterRemoveTool is `verse charter -r`, verse_charter_remove.
	charterRemoveTool = systems.Tool("verse", "charter-remove")
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
	install := map[string]any{
		"at":       text("where, as a kinded path: the address one charter, rule or level is installed at (empty is baseline); with prune, the address the charters stand for"),
		"charters": text("what to install, in tony: one charter (at:, reads:, ttl:, strata:, schedules:, rules:; ttl: how long it stands, as 72h), or a rule, a level or a list of them; several charters are separated by lines of ---, each at its own at:"),
		"prune":    map[string]any{"type": "boolean", "description": "make at and what is under it hold what the charters hold: a package or a rule they leave out goes, in the same step"},
	}
	remove := map[string]any{
		"path": text("what to remove, as a kinded path: a package's address (verse-dev.drift), or one rule, level or schedule of it (verse-dev.drift.rules.watcher). What it wrote stays"),
		"at":   text("where the address ends, when verse cannot tell: a package whose address ends in rules, strata or schedules. path is then read beneath it, and without path the package at at is removed"),
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
			body, err := documents(docs)
			if err != nil {
				return "not checked: " + err.Error(), true, nil
			}
			q := url.Values{"at": {at}}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseCharterCheck,
				Query: q.Encode(), Body: body}))
		},
	}, {
		Name: charterInstallTool,
		Description: "Install charters as the person this task runs for, as `verse charter -a` and " +
			"verse_charter_install do: one at an address, or several each at its own at:, and with prune " +
			"what they leave out at and under the address goes, all in one step. Answers what went where, " +
			"the rules held and the notes on them; verse-charter-check asks first. Each spawn among the " +
			"rules reaches no further than this task does, less installing charters, under autonomy no " +
			"looser and with this task's credential, on any runner: verse refuses one that would. A rule's " +
			"patch and derive are theirs, as any charter's are.",
		Params: install, Required: []string{"charters"}, Step: task.StepVerseCharterInstall,
		// The address the install acts at, which an environment's shape bounds; a call that does
		// not read is no step, and Do says why.
		Fields: func(in map[string]any) (map[string]string, bool) {
			q, defs, err := installOf(in, install)
			if err != nil {
				return nil, false
			}
			at, err := charterfile.InstallAt(q, defs)
			if err != nil {
				return nil, false
			}
			return map[string]string{"at": at}, true
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			q, defs, err := installOf(tu.Input, install)
			if err != nil {
				return "not installed: " + err.Error(), true, nil
			}
			body, err := documents(defs)
			if err != nil {
				return "not installed: " + err.Error(), true, nil
			}
			rep, err := cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseCharterInstall,
				Query: q.Encode(), Body: body})
			if err != nil || !rep.OK {
				return relay(rep, err)
			}
			node, err := codec.ParseNode([]byte(rep.Out), codec.Tony)
			if err != nil {
				return "", false, fmt.Errorf("the verse's answer does not read: %w", err)
			}
			var out strings.Builder
			charterfile.Installed(&out, node, "", true)
			return strings.TrimSpace(out.String()), false, nil
		},
	}, {
		Name: charterRemoveTool,
		Description: "Remove a package, or one rule, level or schedule of one, as the person this task " +
			"runs for, as `verse charter -r` and verse_charter_remove do. What it wrote stays.",
		Params: remove, Required: []string{}, Step: task.StepVerseCharterRemove, Fields: none,
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			p, err := paramsOf(tu.Input, remove)
			if err != nil {
				return "not removed: " + err.Error(), true, nil
			}
			addr, err := entity.PathSegs(p.words["at"])
			if err != nil {
				return "not removed: at: " + err.Error(), true, nil
			}
			reach, err := entity.PathSegs(p.words["path"])
			if err != nil {
				return "not removed: path: " + err.Error(), true, nil
			}
			if len(addr) == 0 && len(reach) == 0 {
				return "not removed: path names what to remove: a package's address, or one with " +
					"rules.<name>, strata.<name> or schedules.<name> after it", true, nil
			}
			split := -1
			if p.words["at"] != "" {
				split = len(addr)
			}
			names := append(addr, reach...)
			q, _ := charterfile.Query(names, split, -1)
			rep, err := cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseCharterRemove, Query: q.Encode()})
			if err != nil || !rep.OK {
				return relay(rep, err)
			}
			return charterfile.Removed(names, split, "", false, true), false, nil
		},
	}}
}

// installOf reads an install's call: the query and the definitions to send
// (charterfile.InstallQuery), as `verse charter -a` reads its flags.
func installOf(in map[string]any, schema map[string]any) (url.Values, []*ir.Node, error) {
	p, err := paramsOf(in, schema)
	if err != nil {
		return nil, nil, err
	}
	segs, err := entity.PathSegs(strings.TrimSpace(p.words["at"]))
	if err != nil {
		return nil, nil, fmt.Errorf("at: %w", err)
	}
	docs, names, err := charterfile.Text(p.words["charters"], codec.Tony)
	if err != nil {
		return nil, nil, err
	}
	return charterfile.InstallQuery(docs, names, entity.JoinSegs(segs), p.flags["prune"])
}

// documents is charters to send, one tony document apiece, as `verse charter` sends them
// (client.PostNodes).
func documents(docs []*ir.Node) (string, error) {
	var body bytes.Buffer
	for i, d := range docs {
		if i > 0 {
			body.WriteString("---\n")
		}
		b, err := codec.NodeBytes(d, codec.Tony)
		if err != nil {
			return "", err
		}
		body.Write(b)
		if !bytes.HasSuffix(b, []byte("\n")) {
			body.WriteByte('\n')
		}
	}
	return body.String(), nil
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
