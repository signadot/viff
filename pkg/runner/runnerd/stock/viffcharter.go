package stock

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/charterfile"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/runner/runnerd/delivery"
	"go.viff.dev/viff/pkg/runner/runnerd/viff"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
	"go.viff.dev/viff/pkg/verbs"
)

// THE VIFF SYSTEM'S CHARTER VERBS: the charters read, asked whether some would install,
// installed and removed, as `viff charter` and viff_charter, _check, _install and _remove do.
// Each tool is defined once with its MCP tool and reads a call as it does (verbs), filters and
// answers through the same code (charterfile, api.Filter), carried by the runner on the op's own route
// (delivery.OpViffCharterGet, OpViffCharterCheck, OpViffCharterInstall,
// OpViffCharterRemove). What a task installs, viff holds within the task.

var (
	// charterGetTool is `viff charter`, viff_charter.
	charterGetTool = systems.Tool("viff", "charter-get")
	// charterCheckTool is `viff charter --check`, viff_charter_check.
	charterCheckTool = systems.Tool("viff", "charter-check")
	// charterInstallTool is `viff charter -a`, viff_charter_install.
	charterInstallTool = systems.Tool("viff", "charter-install")
	// charterRemoveTool is `viff charter -r`, viff_charter_remove.
	charterRemoveTool = systems.Tool("viff", "charter-remove")
)

// viffCharterTools are the viff system's charter tools.
func viffCharterTools(cl *delivery.Client) []viff.Tool {
	return []viff.Tool{
		toolOf(cl, verbs.CharterGet, task.StepViffCharterGet, "not read", func(a verbs.Args) (delivery.Request, error) {
			r, err := verbs.CharterGetOf(a)
			if err != nil {
				return delivery.Request{}, err
			}
			return delivery.Request{Op: delivery.OpViffCharterGet, Fields: map[string]string{"query": r.Query.Encode()}}, nil
		}, func(a verbs.Args, rep delivery.Reply) (string, bool, error) {
			r, err := verbs.CharterGetOf(a)
			if err != nil {
				return "not read: " + err.Error(), true, nil
			}
			node, err := codec.ParseNode([]byte(rep.Out), codec.Tony)
			if err != nil {
				return "", false, fmt.Errorf("the viff instance's answer does not read: %w", err)
			}
			// As viff_charter answers it: a listing one kinded path per line, a reach into a
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
		toolOf(cl, verbs.CharterCheck, task.StepViffCharterCheck, "not checked", func(a verbs.Args) (delivery.Request, error) {
			q, docs, err := verbs.CharterCheckOf(a)
			if err != nil {
				return delivery.Request{}, err
			}
			body, err := documents(docs)
			if err != nil {
				return delivery.Request{}, err
			}
			return delivery.Request{Op: delivery.OpViffCharterCheck, Body: body, Fields: map[string]string{"query": q.Encode()}}, nil
		}, nil),
		// The install's step is the address it acts at (delivery.StepOf), which an environment's
		// shape bounds.
		toolOf(cl, verbs.CharterInstall, task.StepViffCharterInstall, "not installed", func(a verbs.Args) (delivery.Request, error) {
			q, defs, err := verbs.CharterInstallOf(a)
			if err != nil {
				return delivery.Request{}, err
			}
			body, err := documents(defs)
			if err != nil {
				return delivery.Request{}, err
			}
			return delivery.Request{Op: delivery.OpViffCharterInstall, Body: body, Fields: map[string]string{"query": q.Encode()}}, nil
		}, func(_ verbs.Args, rep delivery.Reply) (string, bool, error) {
			node, err := codec.ParseNode([]byte(rep.Out), codec.Tony)
			if err != nil {
				return "", false, fmt.Errorf("the viff instance's answer does not read: %w", err)
			}
			// A task's install is baseline's, as all its acts on viff are (delivery refuses a
			// scope), so the report names none.
			var out strings.Builder
			charterfile.Installed(&out, node, "", true)
			return strings.TrimSpace(out.String()), false, nil
		}),
		toolOf(cl, verbs.CharterRemove, task.StepViffCharterRemove, "not removed", func(a verbs.Args) (delivery.Request, error) {
			names, split, err := verbs.CharterRemoveOf(a)
			if err != nil {
				return delivery.Request{}, err
			}
			q, _ := charterfile.Query(names, split, -1)
			return delivery.Request{Op: delivery.OpViffCharterRemove, Fields: map[string]string{"query": q.Encode()}}, nil
		}, func(a verbs.Args, _ delivery.Reply) (string, bool, error) {
			names, split, err := verbs.CharterRemoveOf(a)
			if err != nil {
				return "not removed: " + err.Error(), true, nil
			}
			return charterfile.Removed(names, split, "", false, true), false, nil
		}),
	}
}

// documents is charters to send, one tony document apiece, as `viff charter` sends them
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
