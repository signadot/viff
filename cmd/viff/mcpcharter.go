package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.viff.dev/viff/pkg/charterfile"
	"go.viff.dev/viff/pkg/verbs"
)

// The charter tools mirror `viff charter`: its read, --check, -a and -r (-R stays on the command
// line), verbs a runner's viff system offers too, defined once (verbs); and the commands that say what the viff is: `viff status`, `viff schema`, and `viff
// flow`'s listing and `flow show`.

type statusIn struct {
	If   string `json:"if,omitempty" jsonschema:"keep only the parts matching this tony pattern"`
	Trim string `json:"trim,omitempty" jsonschema:"write only the parts this match document names, as a key set: {fired firings}"`
}

// schemaName is what a definition's name may be, so that one reaches no other route.
var schemaName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type schemaIn struct {
	Name string `json:"name,omitempty" jsonschema:"one definition by name (viff-entity); without it, the whole schema: the top-level paths, the shape served at each, and every definition they name"`
}

type flowIn struct {
	flowSelection
	If   string `json:"if,omitempty" jsonschema:"keep only the rows matching this tony pattern: {kind: suspension}"`
	Trim string `json:"trim,omitempty" jsonschema:"write only the parts of each row this match document names, as a key set: {task charter subject}"`
}

type flowShowIn struct {
	Subject string `json:"subject" jsonschema:"the subject, an address, with * open in any segment: demo.pr.1, demo.pr.*"`
	Active  *bool  `json:"active,omitempty" jsonschema:"true: only subjects with a live task; false: only those without one; without it, every subject"`
}

func (m *mcpServer) addCharterTools(s *mcp.Server) {
	addVerb(m, s, verbs.CharterGet, m.charter)
	addVerb(m, s, verbs.CharterCheck, m.charterCheck)
	addVerb(m, s, verbs.CharterInstall, m.charterInstall)
	addVerb(m, s, verbs.CharterRemove, m.charterRemove)
	addTool(m, s, &mcp.Tool{
		Name: "viff_status",
		Description: "What the viff is and what it runs, as `viff status` says: the store's rev, live " +
			"flows, gates, suspensions, tasks, firings and what went wrong at them, and what is installed.",
		Annotations: readOnly(),
	}, m.status)
	addTool(m, s, &mcp.Tool{
		Name:        "viff_schema",
		Description: "The schema the viff publishes about itself, as `viff schema` prints it, or one definition of it.",
		Annotations: readOnly(),
	}, m.schema)
	addTool(m, s, &mcp.Tool{
		Name: "viff_flow",
		Description: "The live tasks and the suspensions a selection names, as `viff flow` lists them: " +
			"each task with the charter and rule that spawned it and its subject, each suspension with " +
			"what it holds. Without a selection, all of them.",
		Annotations: readOnly(),
	}, m.flow)
	addTool(m, s, &mcp.Tool{
		Name: "viff_flow_show",
		Description: "A subject's work as a tree, as `viff flow show` draws it: which rule acted on it, " +
			"what that spawned, what followed, and what suspends it.",
		Annotations: readOnly(),
	}, m.flowShow)
}

func (m *mcpServer) charter(ctx context.Context, a verbs.Args) (*mcp.CallToolResult, any, error) {
	r, err := verbs.CharterGetOf(a)
	if err != nil {
		return nil, nil, err
	}
	resp, err := m.c.With(ctx).Get("/charter", r.Query)
	if err != nil {
		return nil, nil, fmt.Errorf("charter: %w", err)
	}
	var buf bytes.Buffer
	if r.Paths && resp.StatusCode < http.StatusBadRequest {
		if err := renderCharterPaths(&buf, resp, r.Full, true); err != nil {
			return nil, nil, err
		}
		return answer(&buf)
	}
	node, err := charterNode(resp, m.out)
	if err != nil {
		return nil, nil, err
	}
	f := filters{keep: r.Keep, project: r.Trim}
	if !charterfile.IsNode(node) {
		if err := renderIR(&buf, node, f, m.out, false); err != nil {
			return nil, nil, err
		}
		return answer(&buf)
	}
	var notes bytes.Buffer
	if err := renderCharterFile(&buf, &notes, node, r.Full, f, m.out, false, asKPaths); err != nil {
		return nil, nil, err
	}
	res, _, err := answer(&buf)
	if err != nil {
		return nil, nil, err
	}
	// The notes are a block of their own, as the command says them on stderr, so the first block
	// stays the charter's file in the server's format.
	if notes.Len() > 0 {
		res.Content = append(res.Content, &mcp.TextContent{Text: strings.TrimRight(notes.String(), "\n")})
	}
	return res, nil, nil
}

func (m *mcpServer) charterCheck(ctx context.Context, a verbs.Args) (*mcp.CallToolResult, any, error) {
	q, docs, err := verbs.CharterCheckOf(a)
	if err != nil {
		return nil, nil, err
	}
	resp, err := charterCheckVia(m.c.With(ctx), q, docs)
	if err != nil {
		return nil, nil, err
	}
	var buf bytes.Buffer
	if err := renderBody(&buf, resp, filters{}, m.out, false); err != nil {
		return nil, nil, err
	}
	return answer(&buf)
}

func (m *mcpServer) charterInstall(ctx context.Context, a verbs.Args) (*mcp.CallToolResult, any, error) {
	q, defs, err := verbs.CharterInstallOf(a)
	if err != nil {
		return nil, nil, err
	}
	resp, err := m.c.With(ctx).PostNodes("/charter", q, defs)
	if err != nil {
		return nil, nil, fmt.Errorf("install: %w", err)
	}
	// The report names the scope the install was made in, this server's, as a task's names
	// none: its acts are baseline's.
	var buf bytes.Buffer
	if _, err := installAnswer(&buf, resp, m.c.Scope, asKPaths); err != nil {
		return nil, nil, fmt.Errorf("install: %w", err)
	}
	return answer(&buf)
}

func (m *mcpServer) charterRemove(ctx context.Context, a verbs.Args) (*mcp.CallToolResult, any, error) {
	names, split, err := verbs.CharterRemoveOf(a)
	if err != nil {
		return nil, nil, err
	}
	said, err := charterRmVia(m.c.With(ctx), names, split, false, asKPaths)
	if err != nil {
		return nil, nil, fmt.Errorf("remove: %w", err)
	}
	return sentence(said), nil, nil
}

func (m *mcpServer) status(ctx context.Context, _ *mcp.CallToolRequest, in statusIn) (*mcp.CallToolResult, any, error) {
	f, err := filtersOf(in.If, in.Trim)
	if err != nil {
		return nil, nil, err
	}
	return m.get(ctx, "status", "/status", nil, f)
}

func (m *mcpServer) schema(ctx context.Context, _ *mcp.CallToolRequest, in schemaIn) (*mcp.CallToolResult, any, error) {
	path := "/schema"
	if in.Name != "" {
		if !schemaName.MatchString(in.Name) {
			return nil, nil, fmt.Errorf("%q is no definition's name: a name is letters, digits, "+
				"'.', '_' and '-' (viff-entity)", in.Name)
		}
		path += "/" + in.Name
	}
	return m.get(ctx, "schema", path, nil, filters{})
}

func (m *mcpServer) flow(ctx context.Context, _ *mcp.CallToolRequest, in flowIn) (*mcp.CallToolResult, any, error) {
	f, err := filtersOf(in.If, in.Trim)
	if err != nil {
		return nil, nil, err
	}
	sel := in.flags()
	if err := toolSelection(sel, ""); err != nil {
		return nil, nil, fmt.Errorf("flow: %w", err)
	}
	return m.get(ctx, "flow", "/flows", sel.query(), f)
}

func (m *mcpServer) flowShow(ctx context.Context, _ *mcp.CallToolRequest, in flowShowIn) (*mcp.CallToolResult, any, error) {
	if in.Subject == "" {
		return nil, nil, fmt.Errorf("flow show draws one subject's work: name it (demo.pr.1, or demo.pr.*)")
	}
	q := url.Values{"subject": {in.Subject}}
	if in.Active != nil {
		q.Set("active", fmt.Sprint(*in.Active))
	}
	return m.get(ctx, "flow show", "/flow/tree", q, filters{})
}
