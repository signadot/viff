package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// installSync installs onlyRule's package at sync.one, through the CLI.
func installSync(t *testing.T) {
	t.Helper()
	dir := writeTree(t, map[string]string{"one.tony": onlyRule("sync.one")})
	if out, err := runCLI(t, "charter", "-a", "-d", dir); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
}

// THE CHARTER TOOL READS WHAT `viff c` READS, AND ANSWERS AS IT PRINTS: the charter as its file,
// a rule as it stands, one kinded path per rule and level, and what is under an address as a note
// naming kinded paths the tool takes back.
func TestTheCharterToolReadsWhatTheCLIInstalled(t *testing.T) {
	serve(t)
	installSync(t)
	cs := mcpSession(t)

	cli, err := runCLI(t, "charter", "sync", "one")
	if err != nil {
		t.Fatal(err)
	}
	out, isErr := callTool(t, cs, "viff_charter", map[string]any{"path": "sync.one"})
	if isErr || out != strings.TrimRight(cli, "\n") || !strings.Contains(out, "name: mark") {
		t.Fatalf("the charter at sync.one: error=%v\n%s\nthe CLI printed:\n%s", isErr, out, cli)
	}

	if out, isErr := callTool(t, cs, "viff_charter", map[string]any{"path": "sync.one.rules.mark"}); isErr ||
		!strings.Contains(out, "name: mark") || strings.Contains(out, "strata") {
		t.Fatalf("one rule: error=%v\n%s", isErr, out)
	}

	out, isErr = callTool(t, cs, "viff_charter", map[string]any{"path": "sync", "paths": true})
	if isErr || !strings.Contains(out, "sync.one.rules.mark") || !strings.Contains(out, "sync.one.strata.src") {
		t.Fatalf("the paths under sync: error=%v\n%s", isErr, out)
	}
	if out, isErr := callTool(t, cs, "viff_charter", map[string]any{"path": "sync", "paths": true, "depth": 0}); isErr ||
		strings.Contains(out, "sync.one") {
		t.Fatalf("depth 0 reached under sync: error=%v\n%s", isErr, out)
	}

	if out, isErr := callTool(t, cs, "viff_charter", map[string]any{}); isErr || !strings.Contains(out, "under here: sync.one — each is read at its own path") {
		t.Fatalf("baseline's charter does not name what is under it: error=%v\n%s", isErr, out)
	}
	// The command names the same charters as the steps it takes.
	if _, errOut, err := runCLIStreams(t, "charter", "sync"); err != nil ||
		!strings.Contains(errOut, "note: under sync: sync one — `viff c sync -d <dir>`") {
		t.Fatalf("the command's note on what is under sync: %v\n%s", err, errOut)
	}
	if out, isErr := callTool(t, cs, "viff_charter", map[string]any{"path": "sync.one", "depth": -1}); !isErr || !strings.Contains(out, "never negative") {
		t.Fatalf("a negative depth: error=%v\n%s", isErr, out)
	}
	if out, isErr := callTool(t, cs, "viff_charter", map[string]any{"path": "nowhere.at.all"}); !isErr || !strings.Contains(out, "charter:") {
		t.Fatalf("a path nothing stands at: error=%v\n%s", isErr, out)
	}
}

// AT SAYS WHERE THE ADDRESS ENDS, as `--` does on the command line: with packages at x and at
// x.rules, the path x.rules is the package, and at x the path rules is x's rules.
func TestTheCharterToolsAtSaysWhereTheAddressEnds(t *testing.T) {
	serve(t)
	dir := writeTree(t, map[string]string{"x.tony": onlyRule("x"), "xr.tony": onlyRule("x.rules")})
	if out, err := runCLI(t, "charter", "-a", "-d", dir); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	cs := mcpSession(t)
	if out, isErr := callTool(t, cs, "viff_charter", map[string]any{"path": "x.rules"}); isErr || !strings.Contains(out, "at: x.rules") {
		t.Fatalf("the package at x.rules: error=%v\n%s", isErr, out)
	}
	cli, err := runCLI(t, "charter", "x", "--", "rules")
	if err != nil {
		t.Fatal(err)
	}
	out, isErr := callTool(t, cs, "viff_charter", map[string]any{"at": "x", "path": "rules"})
	if isErr || out != strings.TrimRight(cli, "\n") || strings.Contains(out, "at: x.rules") || !strings.Contains(out, "path: x.row") {
		t.Fatalf("x's rules: error=%v\n%s\nthe CLI printed:\n%s", isErr, out, cli)
	}
}

// THE CHECK TOOL ASKS WHAT AN INSTALL WOULD DO, AS `viff c --check` DOES, and installs nothing:
// a charter that would go says so, one outside the address is refused as an answer, and the text
// it is given is read as a directory's files are.
func TestTheCharterCheckToolAnswersAsTheCLIDoes(t *testing.T) {
	serve(t)
	cs := mcpSession(t)

	two := onlyRule("sync.one") + "---\n" + onlyRule("sync.two")
	out, isErr := callTool(t, cs, "viff_charter_check", map[string]any{"at": "sync", "charters": two})
	if isErr || !strings.Contains(out, "verdict: installs") || !strings.Contains(out, "sync.two") {
		t.Fatalf("two charters that would install: error=%v\n%s", isErr, out)
	}
	if out, isErr := callTool(t, cs, "viff_charter", map[string]any{"path": "sync.one"}); !isErr {
		t.Fatalf("the check installed sync.one:\n%s", out)
	}
	if out, isErr := callTool(t, cs, "viff_charter_check", map[string]any{"at": "elsewhere", "charters": onlyRule("sync.one")}); isErr ||
		!strings.Contains(out, "verdict: refused") {
		t.Fatalf("a charter outside the address: error=%v\n%s", isErr, out)
	}

	for _, c := range []struct {
		at, charters, want string
	}{
		{"sync", "{name: mark}", "document 1 is no charter"},
		{"sync", onlyRule("sync.one") + "---\nnull\n---\n{name: mark}", "document 3 is no charter"},
		{"sync", onlyRule("sync.one") + "---\n" + onlyRule("sync.one"), "document 1 and document 2 are both the charter at sync.one"},
		{"sync", "", "a check carries the charters"},
		{"", onlyRule("sync.one"), "names the address"},
	} {
		// A refusal speaks of the tool's parameters, never the command line's flags.
		if out, isErr := callTool(t, cs, "viff_charter_check", map[string]any{"at": c.at, "charters": c.charters}); !isErr ||
			!strings.Contains(out, c.want) || strings.Contains(out, "--") {
			t.Errorf("at %q, %q: error=%v\n%s", c.at, c.charters, isErr, out)
		}
	}
}

// STATUS SAYS WHAT THE VIFF HAS DONE: after a rule fires, the count of firings has moved.
func TestTheStatusToolSaysWhatFired(t *testing.T) {
	serveAsking(t)
	cs := mcpSession(t)
	out, isErr := callTool(t, cs, "viff_status", nil)
	if isErr || !strings.Contains(out, "fired: 0") {
		t.Fatalf("before a firing: error=%v\n%s", isErr, out)
	}
	installSync(t)
	if _, err := runCLI(t, "data", "demo", "item", "x", "--put", "{id: x}"); err != nil {
		t.Fatal(err)
	}
	pollTool(t, cs, "viff_data", map[string]any{"at": "sync.one.row.x"}, "seen: true")
	if out, _ := callTool(t, cs, "viff_status", nil); strings.Contains(out, "fired: 0") || !strings.Contains(out, "fired:") {
		t.Fatalf("after a firing:\n%s", out)
	}
	if out, isErr := callTool(t, cs, "viff_status", map[string]any{"trim": "{fired}"}); isErr ||
		!strings.Contains(out, "fired:") || strings.Contains(out, "storeRev") {
		t.Fatalf("status trimmed to fired: error=%v\n%s", isErr, out)
	}

	// A rule's marks are notes naming the charter as a kinded path, as the tool takes it back.
	if out, err := runCLI(t, "flow", "suspend", "--charter", "sync.one:mark", "--why", "a test"); err != nil {
		t.Fatalf("suspend: %v\n%s", err, out)
	}
	// They are a block of their own, so the first block is the charter's file alone.
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "viff_charter", Arguments: map[string]any{"path": "sync.one"}})
	if err != nil || res.IsError || len(res.Content) != 2 {
		t.Fatalf("the charter with a suspended rule: %v %+v", err, res)
	}
	file, notes := res.Content[0].(*mcp.TextContent).Text, res.Content[1].(*mcp.TextContent).Text
	if strings.Contains(file, "note:") || !strings.HasPrefix(notes, "note: sync.one: rule mark is suspended by") {
		t.Fatalf("the file:\n%s\nthe notes:\n%s", file, notes)
	}
}

func TestTheSchemaToolPrintsWhatTheCLIPrints(t *testing.T) {
	serve(t)
	cs := mcpSession(t)
	cli, err := runCLI(t, "schema", "viff-status")
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := callTool(t, cs, "viff_schema", map[string]any{"name": "viff-status"}); isErr || out != strings.TrimRight(cli, "\n") {
		t.Fatalf("viff-status: error=%v\n%s\nthe CLI printed:\n%s", isErr, out, cli)
	}
	for _, name := range []string{"a/b", "viff-entity?x=1", "a#b", "%zz", "..", "."} {
		if out, isErr := callTool(t, cs, "viff_schema", map[string]any{"name": name}); !isErr || !strings.Contains(out, "is no definition's name") {
			t.Errorf("%q: error=%v\n%s", name, isErr, out)
		}
	}
}

// THE FLOW READS ANSWER WHAT `viff flow` AND `viff flow show` PRINT.
func TestTheFlowReadsPrintWhatTheCLIPrints(t *testing.T) {
	serveAsking(t)
	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", "{stage: open}"); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "flow", "suspend", "--charter", "sync.one:mark", "--subject", "demo.pr.1", "--why", "a test"); err != nil {
		t.Fatalf("suspend: %v\n%s", err, out)
	}
	cs := mcpSession(t)
	for _, c := range []struct {
		tool string
		args map[string]any
		cli  []string
	}{
		{"viff_flow", nil, []string{"flow"}},
		{"viff_flow", map[string]any{"charter": "sync.one:mark", "subject": "demo.pr.*"}, []string{"flow", "--charter", "sync.one:mark", "--subject", "demo.pr.*"}},
		{"viff_flow", map[string]any{"charter": "elsewhere"}, []string{"flow", "--charter", "elsewhere"}},
		{"viff_flow_show", map[string]any{"subject": "demo.pr.1"}, []string{"flow", "show", "demo.pr.1"}},
		{"viff_flow_show", map[string]any{"subject": "demo.pr.*", "active": false}, []string{"flow", "show", "demo.pr.*", "--no-active"}},
	} {
		cli, err := runCLI(t, c.cli...)
		if err != nil {
			t.Fatalf("%v: %v", c.cli, err)
		}
		if out, isErr := callTool(t, cs, c.tool, c.args); isErr || out != strings.TrimRight(cli, "\n") {
			t.Errorf("%s %v: error=%v\n%s\nthe CLI printed:\n%s", c.tool, c.args, isErr, out, cli)
		}
	}
	if out, _ := callTool(t, cs, "viff_flow", nil); !strings.Contains(out, "a test") {
		t.Fatalf("the listing does not hold the suspension:\n%s", out)
	}
	if out, _ := callTool(t, cs, "viff_flow", map[string]any{"charter": "elsewhere"}); strings.Contains(out, "a test") {
		t.Fatalf("a selection of elsewhere holds the suspension:\n%s", out)
	}
	// A selection the server would refuse is refused in the tool's words, which name no flag.
	for _, args := range []map[string]any{
		{"task": "t1", "charter": "sync.one"},
		{"task": "t1", "subject": "demo.pr.1"},
		{"task": "t1", "recursive": true},
		{"recursive": true},
	} {
		if out, isErr := callTool(t, cs, "viff_flow", args); !isErr || strings.Contains(out, "--") {
			t.Errorf("%v: error=%v\n%s", args, isErr, out)
		}
	}
	if out, isErr := callTool(t, cs, "viff_flow_show", map[string]any{"subject": ""}); !isErr || !strings.Contains(out, "name it") {
		t.Fatalf("flow show of no subject: error=%v\n%s", isErr, out)
	}
}

// THE PROMPTS ARE LISTED, AND EACH SAYS WHICH READS TO MAKE with what it was given.
func TestThePromptsSayWhichReadsToMake(t *testing.T) {
	serve(t)
	cs := mcpSession(t)
	list, err := cs.ListPrompts(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range list.Prompts {
		names = append(names, p.Name)
	}
	if strings.Join(names, " ") != "explain-charter why-not" {
		t.Fatalf("the prompts: %v", names)
	}

	text := func(name string, args map[string]string) (string, error) {
		res, err := cs.GetPrompt(t.Context(), &mcp.GetPromptParams{Name: name, Arguments: args})
		if err != nil {
			return "", err
		}
		return res.Messages[0].Content.(*mcp.TextContent).Text, nil
	}
	for _, c := range []struct {
		name  string
		args  map[string]string
		wants []string
	}{
		{"explain-charter", map[string]string{"address": "sync.one"}, []string{`path "sync.one"`, `charter "sync.one"`}},
		{"explain-charter", nil, []string{"baseline's charter", `charter ":"`}},
		{"why-not", map[string]string{"rule": "sync.one:mark", "subject": "demo.item.x"},
			[]string{`viff_charter with path "sync.one" and depth 0`, "mark is in its", `viff_data with at "demo.item.x"`,
				`if {subject: "demo.item.x"}`, "if {kind: suspension}"}},
		{"why-not", map[string]string{"rule": ":review", "subject": "demo.pr.1"}, []string{"viff_charter with path left out", "review is in its"}},
		{"why-not", map[string]string{"rule": `demo."a:b":review`, "subject": "demo.pr.1"}, []string{`viff_charter with path "demo.\"a:b\""`, "review is in its"}},
	} {
		out, err := text(c.name, c.args)
		if err != nil {
			t.Fatalf("%s %v: %v", c.name, c.args, err)
		}
		for _, w := range c.wants {
			if !strings.Contains(out, w) {
				t.Errorf("%s %v does not say %q:\n%s", c.name, c.args, w, out)
			}
		}
	}
	if _, err := text("why-not", map[string]string{"rule": "sync.one", "subject": "demo.pr.1"}); err == nil ||
		!strings.Contains(err.Error(), "names no rule") {
		t.Fatalf("a rule with no name: %v", err)
	}
	for _, rule := range []string{"*:review", "sync.*:mark", "sync.one:*"} {
		if _, err := text("why-not", map[string]string{"rule": rule, "subject": "demo.pr.1"}); err == nil ||
			!strings.Contains(err.Error(), "selects more than one") {
			t.Errorf("%s: %v", rule, err)
		}
	}
}

// viff_flow_show sends active only when it is said, as `flow show --active` and `--no-active` do.
func TestTheFlowShowToolSendsActiveOnlyWhenSaid(t *testing.T) {
	var q url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		w.Write([]byte(`[]`))
	}))
	defer ts.Close()
	testAddr = strings.TrimPrefix(ts.URL, "http://")
	cs := mcpSession(t)
	for _, c := range []struct {
		args map[string]any
		want string
		has  bool
	}{
		{map[string]any{"subject": "demo.pr.*"}, "", false},
		{map[string]any{"subject": "demo.pr.*", "active": true}, "true", true},
		{map[string]any{"subject": "demo.pr.*", "active": false}, "false", true},
	} {
		if out, isErr := callTool(t, cs, "viff_flow_show", c.args); isErr {
			t.Fatalf("%v: %s", c.args, out)
		}
		if q.Has("active") != c.has || q.Get("active") != c.want || q.Get("subject") != "demo.pr.*" {
			t.Fatalf("%v asked %v, want active=%q (sent: %v)", c.args, q, c.want, c.has)
		}
	}
}
