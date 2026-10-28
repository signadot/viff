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

	"github.com/signadot/verse/charterfile"
	"github.com/signadot/verse/entity"
)

// The charter tools mirror `verse charter`: its read, --check, -a and -r (-R stays on the command
// line); and the commands that say what the verse is: `verse status`, `verse schema`, and `verse
// flow`'s listing and `flow show`.

type charterIn struct {
	Path  string `json:"path,omitempty" jsonschema:"the charter's path as a kinded path: an address (verse-dev.drift), then a reach into what stands there (verse-dev.drift.rules.watcher); empty is baseline's charter, and what is under it"`
	At    string `json:"at,omitempty" jsonschema:"where the address ends, when the server cannot tell: a package whose address ends in rules, strata or schedules. path is then read beneath it"`
	Depth *int   `json:"depth,omitempty" jsonschema:"how far under the address to go: 0 is the address alone, 1 adds its immediate children; without it, everything"`
	Paths bool   `json:"paths,omitempty" jsonschema:"answer one kinded path per rule and level instead of the charter, each a path this tool takes back"`
	If    string `json:"if,omitempty" jsonschema:"keep only the parts matching this tony pattern"`
	Trim  string `json:"trim,omitempty" jsonschema:"write only the parts this match document names, as a key set: {rules}"`
}

type charterCheckIn struct {
	At       string `json:"at" jsonschema:"the address the charters would be installed at, as a kinded path, as an install with a prune would take it: what stands at and under it and the charters leave out would go"`
	Charters string `json:"charters" jsonschema:"the charters, in tony, each a document saying its own at: (at or under the address) with reads, ttl, strata, schedules and rules as a charter file does; several are separated by lines of ---"`
}

type statusIn struct {
	If   string `json:"if,omitempty" jsonschema:"keep only the parts matching this tony pattern"`
	Trim string `json:"trim,omitempty" jsonschema:"write only the parts this match document names, as a key set: {fired firings}"`
}

// schemaName is what a definition's name may be, so that one reaches no other route.
var schemaName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type charterInstallIn struct {
	At       string `json:"at,omitempty" jsonschema:"where, as a kinded path: the address one charter, rule or level is installed at (empty is baseline); with prune, the address the charters stand for"`
	Charters string `json:"charters" jsonschema:"what to install, in tony: one charter (at:, reads:, ttl:, strata:, schedules:, rules:; ttl: how long it stands, as 72h), or a rule, a level or a list of them; several charters are separated by lines of ---, each at its own at:"`
	Prune    bool   `json:"prune,omitempty" jsonschema:"make at and what is under it hold what the charters hold: a package or a rule they leave out goes, in the same step"`
}

type charterRemoveIn struct {
	Path string `json:"path,omitempty" jsonschema:"what to remove, as a kinded path: a package's address (verse-dev.drift), or one rule, level or schedule of it (verse-dev.drift.rules.watcher). What it wrote stays"`
	At   string `json:"at,omitempty" jsonschema:"where the address ends, when the server cannot tell: a package whose address ends in rules, strata or schedules. path is then read beneath it, and without path the package at at is removed"`
}

type schemaIn struct {
	Name string `json:"name,omitempty" jsonschema:"one definition by name (verse-entity); without it, the whole schema: the top-level paths, the shape served at each, and every definition they name"`
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
	addTool(m, s, &mcp.Tool{
		Name: "verse_charter",
		Description: "Read the charter at a path, as `verse charter` does: an installed charter as its " +
			"file (at, reads, ttl, strata, schedules, rules), a rule or level of one as it stands, or with " +
			"paths one line per rule and level. What the file cannot say follows it as notes: a rule " +
			"held, suspended or in trouble, and the charters under the address.",
		Annotations: readOnly(),
	}, m.charter)
	addTool(m, s, &mcp.Tool{
		Name: "verse_charter_check",
		Description: "Ask whether charters would install at an address, as `verse charter --check` " +
			"does: each charter's verdict, and what the install would add, change and take away. " +
			"Nothing is installed; a refusal is the answer, not an error.",
		Annotations: readOnly(),
	}, m.charterCheck)
	addTool(m, s, &mcp.Tool{
		Name: "verse_charter_install",
		Description: "Install charters, as `verse charter -a` does, on your authority: one at an address, or " +
			"several each at its own at:, and with prune what they leave out at and under the address goes. " +
			"Answers what went where, the rules held and the notes on them; verse_charter_check asks first.",
		Annotations: writes(true),
	}, m.charterInstall)
	addTool(m, s, &mcp.Tool{
		Name: "verse_charter_remove",
		Description: "Remove a package, or one rule, level or schedule of one, as `verse charter -r` does. What it wrote " +
			"stays. A place and everything under it is removed from the command line, with -R.",
		Annotations: writes(true),
	}, m.charterRemove)
	addTool(m, s, &mcp.Tool{
		Name: "verse_status",
		Description: "What the verse is and what it runs, as `verse status` says: the store's rev, live " +
			"flows, gates, suspensions, tasks, firings and what went wrong at them, and what is installed.",
		Annotations: readOnly(),
	}, m.status)
	addTool(m, s, &mcp.Tool{
		Name:        "verse_schema",
		Description: "The schema the verse publishes about itself, as `verse schema` prints it, or one definition of it.",
		Annotations: readOnly(),
	}, m.schema)
	addTool(m, s, &mcp.Tool{
		Name: "verse_flow",
		Description: "The live tasks and the suspensions a selection names, as `verse flow` lists them: " +
			"each task with the charter and rule that spawned it and its subject, each suspension with " +
			"what it holds. Without a selection, all of them.",
		Annotations: readOnly(),
	}, m.flow)
	addTool(m, s, &mcp.Tool{
		Name: "verse_flow_show",
		Description: "A subject's work as a tree, as `verse flow show` draws it: which rule acted on it, " +
			"what that spawned, what followed, and what suspends it.",
		Annotations: readOnly(),
	}, m.flowShow)
}

func (m *mcpServer) charter(ctx context.Context, _ *mcp.CallToolRequest, in charterIn) (*mcp.CallToolResult, any, error) {
	addr, err := entity.PathSegs(in.At)
	if err != nil {
		return nil, nil, fmt.Errorf("at: %w", err)
	}
	reach, err := entity.PathSegs(in.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("path: %w", err)
	}
	split := -1
	if in.At != "" {
		split = len(addr)
	}
	depth := -1
	if in.Depth != nil {
		if *in.Depth < 0 {
			return nil, nil, fmt.Errorf("depth is never negative: without it, everything under the address")
		}
		depth = *in.Depth
	}
	f, err := filtersOf(in.If, in.Trim)
	if err != nil {
		return nil, nil, err
	}
	q, full := charterfile.Query(append(addr, reach...), split, depth)
	resp, err := m.c.With(ctx).Get("/charter", q)
	if err != nil {
		return nil, nil, fmt.Errorf("charter: %w", err)
	}
	var buf bytes.Buffer
	if in.Paths && resp.StatusCode < http.StatusBadRequest {
		if err := renderCharterPaths(&buf, resp, full, true); err != nil {
			return nil, nil, err
		}
		return answer(&buf)
	}
	node, err := charterNode(resp, m.out)
	if err != nil {
		return nil, nil, err
	}
	if !charterfile.IsNode(node) {
		if err := renderIR(&buf, node, f, m.out, false); err != nil {
			return nil, nil, err
		}
		return answer(&buf)
	}
	var notes bytes.Buffer
	if err := renderCharterFile(&buf, &notes, node, full, f, m.out, false, asKPaths); err != nil {
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

func (m *mcpServer) charterCheck(ctx context.Context, _ *mcp.CallToolRequest, in charterCheckIn) (*mcp.CallToolResult, any, error) {
	segs, err := entity.PathSegs(in.At)
	if err != nil {
		return nil, nil, fmt.Errorf("at: %w", err)
	}
	docs, err := charterfile.CheckDocs(in.Charters, mcpIn)
	if err != nil {
		return nil, nil, err
	}
	resp, err := charterCheckVia(m.c.With(ctx), entity.JoinSegs(segs), docs)
	if err != nil {
		return nil, nil, err
	}
	var buf bytes.Buffer
	if err := renderBody(&buf, resp, filters{}, m.out, false); err != nil {
		return nil, nil, err
	}
	return answer(&buf)
}

func (m *mcpServer) charterInstall(ctx context.Context, _ *mcp.CallToolRequest, in charterInstallIn) (*mcp.CallToolResult, any, error) {
	if err := m.nobody(); err != nil {
		return nil, nil, err
	}
	segs, err := entity.PathSegs(in.At)
	if err != nil {
		return nil, nil, fmt.Errorf("at: %w", err)
	}
	at := entity.JoinSegs(segs)
	docs, names, err := charterfile.Text(in.Charters, mcpIn)
	if err != nil {
		return nil, nil, err
	}
	q, defs, err := charterfile.InstallQuery(docs, names, at, in.Prune)
	if err != nil {
		return nil, nil, err
	}
	resp, err := m.c.With(ctx).PostNodes("/charter", q, defs)
	if err != nil {
		return nil, nil, fmt.Errorf("install: %w", err)
	}
	var buf bytes.Buffer
	if err := installAnswer(&buf, resp, m.c.Scope, asKPaths); err != nil {
		return nil, nil, fmt.Errorf("install: %w", err)
	}
	return answer(&buf)
}

func (m *mcpServer) charterRemove(ctx context.Context, _ *mcp.CallToolRequest, in charterRemoveIn) (*mcp.CallToolResult, any, error) {
	if err := m.nobody(); err != nil {
		return nil, nil, err
	}
	addr, err := entity.PathSegs(in.At)
	if err != nil {
		return nil, nil, fmt.Errorf("at: %w", err)
	}
	reach, err := entity.PathSegs(in.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("path: %w", err)
	}
	if len(reach) == 0 && len(addr) == 0 {
		return nil, nil, fmt.Errorf("path names what to remove: a package's address, or one with " +
			"rules.<name>, strata.<name> or schedules.<name> after it")
	}
	split := -1
	if in.At != "" {
		split = len(addr)
	}
	said, err := charterRmVia(m.c.With(ctx), append(addr, reach...), split, false, asKPaths)
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
				"'.', '_' and '-' (verse-entity)", in.Name)
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
