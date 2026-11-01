package stock

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/charterfile"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/verse"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/verbs"
)

// THE VERSE SYSTEM'S CHARTER VERBS: the charters read, asked whether some would install,
// installed and removed, as `verse charter` and verse_charter, _check, _install and _remove do.
// Each tool is defined once with its MCP tool and reads a call as it does (verbs), filters and
// answers through the same code (charterfile, api.Filter), carried by the runner on the op's own route
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
	install := toolOf(verbs.CharterInstall, task.StepVerseCharterInstall, "not installed", func(ctx context.Context, tu task.ToolUse, a verbs.Args) (string, bool, error) {
		q, defs, err := verbs.CharterInstallOf(a)
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
		// A task's install is baseline's, as all its acts on verse are (delivery refuses a
		// scope), so the report names none.
		var out strings.Builder
		charterfile.Installed(&out, node, "", true)
		return strings.TrimSpace(out.String()), false, nil
	})
	// The address the install acts at, which an environment's shape bounds; a call that does
	// not read is no step, and Do says why.
	install.Fields = func(in map[string]any) (map[string]string, bool) {
		a, err := verbs.CharterInstall.Read(in)
		if err != nil {
			return nil, false
		}
		q, defs, err := verbs.CharterInstallOf(a)
		if err != nil {
			return nil, false
		}
		at, err := charterfile.InstallAt(q, defs)
		if err != nil {
			return nil, false
		}
		return map[string]string{"at": at}, true
	}
	return []verse.Tool{
		toolOf(verbs.CharterGet, task.StepVerseCharterGet, "not read", func(ctx context.Context, tu task.ToolUse, a verbs.Args) (string, bool, error) {
			r, err := verbs.CharterGetOf(a)
			if err != nil {
				return "not read: " + err.Error(), true, nil
			}
			rep, err := cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseCharterGet, Query: r.Query.Encode()})
			if err != nil || !rep.OK {
				return relay(rep, err)
			}
			node, err := codec.ParseNode([]byte(rep.Out), codec.Tony)
			if err != nil {
				return "", false, fmt.Errorf("the verse's answer does not read: %w", err)
			}
			// As verse_charter answers it: a listing one kinded path per line, a reach into a
			// charter as it stands, and a charter as its file with the notes after it.
			if r.Paths {
				paths, err := charterfile.Paths(node, r.Full)
				if err != nil {
					return "not read: " + err.Error(), true, nil
				}
				lines := make([]string, 0, len(paths))
				for _, at := range paths {
					lines = append(lines, at.KPath())
				}
				return strings.Join(lines, "\n"), false, nil
			}
			f := api.Filter{Keep: r.Keep, Trim: r.Trim}
			if !charterfile.IsNode(node) {
				if node, err = f.Apply(node); err != nil {
					return "not read: " + err.Error(), true, nil
				}
				return tonyText(node)
			}
			files, notes, under, err := charterfile.Read(node, r.Full, "", true)
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
				if r.Full != "" {
					here = charterfile.Spelled(r.Full, true)
				}
				notes = append(notes, charterfile.UnderNote(here, under))
			}
			return strings.Join(append(parts, notes...), "\n"), false, nil
		}),
		toolOf(verbs.CharterCheck, task.StepVerseCharterCheck, "not checked", func(ctx context.Context, tu task.ToolUse, a verbs.Args) (string, bool, error) {
			q, docs, err := verbs.CharterCheckOf(a)
			if err != nil {
				return "not checked: " + err.Error(), true, nil
			}
			body, err := documents(docs)
			if err != nil {
				return "not checked: " + err.Error(), true, nil
			}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseCharterCheck,
				Query: q.Encode(), Body: body}))
		}),
		install,
		toolOf(verbs.CharterRemove, task.StepVerseCharterRemove, "not removed", func(ctx context.Context, tu task.ToolUse, a verbs.Args) (string, bool, error) {
			names, split, err := verbs.CharterRemoveOf(a)
			if err != nil {
				return "not removed: " + err.Error(), true, nil
			}
			q, _ := charterfile.Query(names, split, -1)
			rep, err := cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpVerseCharterRemove, Query: q.Encode()})
			if err != nil || !rep.OK {
				return relay(rep, err)
			}
			return charterfile.Removed(names, split, "", false, true), false, nil
		}),
	}
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
