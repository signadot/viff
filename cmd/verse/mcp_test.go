package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/verbs"
)

// mcpSession is a client connected to `verse mcp` toward the verse the test started, acting as
// the package's tester.
func mcpSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	return mcpSessionAs(t, tester)
}

// mcpSessionAs is mcpSession acting as who, which the server fixes when it starts, as `verse mcp`
// does. It proves nothing: the test's verse takes who a request names.
func mcpSessionAs(t *testing.T, who string) *mcp.ClientSession {
	t.Helper()
	return mcpConnect(t, who, false)
}

// mcpConnect is a client connected to a server acting as who, serving only the reads with
// readOnly, as `verse mcp -read-only` does.
func mcpConnect(t *testing.T, who string, readOnly bool) *mcp.ClientSession {
	t.Helper()
	g.Addr = testAddr
	srv := newMCPServer(t.Context(), mcpClient(who, nil), codec.Tony, who, readOnly)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// callTool calls one tool and answers its text, and whether it answered as an error.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func TestTheDataToolReadsWhatTheCLIWrote(t *testing.T) {
	serveAsking(t)
	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", "{stage: open}"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "data", "demo", "pr", "2", "--put", "{stage: closed}"); err != nil {
		t.Fatal(err)
	}
	cs := mcpSession(t)

	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var data *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "verse_data" {
			data = tool
		}
	}
	if data == nil || data.Annotations == nil || !data.Annotations.ReadOnlyHint {
		t.Fatalf("verse_data is not listed as a read: %+v", data)
	}

	if out, isErr := callTool(t, cs, "verse_data", map[string]any{"at": "demo.pr.1"}); isErr || !strings.Contains(out, "stage: open") {
		t.Fatalf("demo.pr.1: error=%v\n%s", isErr, out)
	}
	out, isErr := callTool(t, cs, "verse_data", map[string]any{"at": "demo.pr.*", "if": "{stage: open}"})
	if isErr || !strings.Contains(out, "open") || strings.Contains(out, "closed") {
		t.Fatalf("the if is not the server's selector: error=%v\n%s", isErr, out)
	}
	out, isErr = callTool(t, cs, "verse_data", map[string]any{"at": "demo", "paths": true})
	// Kinded paths, the spelling `at` takes back: a segment that reads as a number is quoted.
	if isErr || !strings.Contains(out, `demo.pr."1"`) || !strings.Contains(out, `demo.pr."2"`) {
		t.Fatalf("the paths: error=%v\n%s", isErr, out)
	}
}

// -READ-ONLY IS A FLAG OF THE COMMAND: the command line knows it, so a host that starts
// `verse mcp -read-only` gets the server, not a usage error. An argument after it is refused as
// one, which says the flag before it was read.
func TestTheCommandLineTakesReadOnly(t *testing.T) {
	_, err := runCLI(t, "mcp", "-read-only", "extra")
	if err == nil || !strings.Contains(err.Error(), "mcp takes no arguments") {
		t.Fatalf("verse mcp -read-only extra: %v, want the flag read and the argument refused", err)
	}
}

// -READ-ONLY SERVES THE READS AND NOTHING ELSE: every tool annotated as a read, and no other, so a
// write is no tool at all rather than one refused.
func TestAReadOnlyServerServesOnlyTheReads(t *testing.T) {
	serveAsking(t)
	list := func(readOnly bool) map[string]bool {
		tools, err := mcpConnect(t, tester, readOnly).ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, tool := range tools.Tools {
			names[tool.Name] = tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		}
		return names
	}
	all, reads := list(false), list(true)
	for name, read := range all {
		if _, served := reads[name]; served != read {
			t.Errorf("%s: a read=%v, served read-only=%v", name, read, served)
		}
	}
	if len(reads) == 0 || len(reads) == len(all) || !reads["verse_data"] {
		t.Fatalf("read-only serves %d of %d tools: %v", len(reads), len(all), reads)
	}
	cs := mcpConnect(t, tester, true)
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "verse_data_put", Arguments: map[string]any{"at": "demo.pr.1", "value": "{stage: open}"}})
	if err == nil && !res.IsError {
		t.Fatal("a read-only server wrote")
	}
	if out, _ := runCLI(t, "data", "--at", "demo.pr.1"); strings.Contains(out, "stage") {
		t.Fatalf("demo.pr.1 was written:\n%s", out)
	}
}

// THE VERBS A RUNNER OFFERS TOO ARE SERVED AS DEFINED (verbs): each tool with its description and
// its parameters as verse mcp's face reads them, required and typed as the definition says, and
// no other parameter taken.
func TestTheSharedVerbsAreServedAsDefined(t *testing.T) {
	serveAsking(t)
	tools, err := mcpSession(t).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		served[tool.Name] = tool
	}
	for _, v := range verbs.All {
		tool := served[v.Tool]
		if tool == nil {
			t.Errorf("%s is not served", v.Tool)
			continue
		}
		var schema map[string]any
		b, _ := json.Marshal(tool.InputSchema)
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(v.Schema(verbs.MCP))
		var wantSchema map[string]any
		_ = json.Unmarshal(want, &wantSchema)
		if tool.Description != v.Description(verbs.MCP) || !reflect.DeepEqual(schema, wantSchema) {
			t.Errorf("%s is served as %q %v, defined as %q %v", v.Tool, tool.Description, schema, v.Description(verbs.MCP), wantSchema)
		}
	}
}

// AN ADDRESS NOTHING STANDS AT is answered in the words `verse data` says it in.
func TestTheDataToolSaysWhatTheCLISays(t *testing.T) {
	serveAsking(t)
	_, cliErr := runCLI(t, "data", "--at", "demo.pr.9")
	if cliErr == nil {
		t.Fatal("verse data on an address nothing stands at answered")
	}
	out, isErr := callTool(t, mcpSession(t), "verse_data", map[string]any{"at": "demo.pr.9"})
	if !isErr || !strings.Contains(out, cliErr.Error()) {
		t.Fatalf("the tool said %q (error=%v), the CLI %q", out, isErr, cliErr)
	}
}

// TRIM IS THE CLI'S: what `verse data --trim` writes of each entity the path reached, the tool
// answers.
func TestTheDataToolTrimsAsTheCLIDoes(t *testing.T) {
	serveAsking(t)
	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", "{stage: open, title: one}"); err != nil {
		t.Fatal(err)
	}
	want, err := runCLI(t, "data", "--at", "demo.pr.*", "--trim", "{stage}")
	if err != nil {
		t.Fatal(err)
	}
	out, isErr := callTool(t, mcpSession(t), "verse_data", map[string]any{"at": "demo.pr.*", "trim": "{stage}"})
	if isErr || out != strings.TrimRight(want, "\n") || strings.Contains(out, "title") {
		t.Fatalf("the tool answered %q (error=%v), the CLI %q", out, isErr, want)
	}
}

// AN ANSWER IS NOT CUT: past the most the server answers, it says so and how to narrow it.
func TestADataAnswerIsNotCut(t *testing.T) {
	serveAsking(t)
	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", "{stage: open}"); err != nil {
		t.Fatal(err)
	}
	was := mcpAnswerMax
	mcpAnswerMax = 4
	t.Cleanup(func() { mcpAnswerMax = was })
	out, isErr := callTool(t, mcpSession(t), "verse_data", map[string]any{"at": "demo.pr.1"})
	if !isErr || !strings.Contains(out, "is not cut") || strings.Contains(out, "stage") {
		t.Fatalf("error=%v\n%s", isErr, out)
	}
}

// A TOOL READS TONY, as its schema says, whatever format the config reads the command line's in:
// in YAML `{stage owner}` is one key, and would trim the entity to nothing.
func TestTheToolsReadTonyWhateverTheConfigReads(t *testing.T) {
	serveAsking(t)
	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", "{stage: open, owner: ana, size: 3}"); err != nil {
		t.Fatal(err)
	}
	g.IFmt = "yaml"
	t.Cleanup(func() { g.IFmt = "" })
	out, isErr := callTool(t, mcpSession(t), "verse_data", map[string]any{"at": "demo.pr.1", "trim": "{stage owner}"})
	if isErr || !strings.Contains(out, "stage: open") || !strings.Contains(out, "owner: ana") || strings.Contains(out, "size") {
		t.Fatalf("error=%v\n%s", isErr, out)
	}
}

// WRITING THROUGH THE TOOLS, read back by the CLI: a put merges and answers what stands, its
// conditions hold it back, and a remove says what `verse data --rm` says.
func TestTheDataToolsPutAndRemove(t *testing.T) {
	serveAsking(t)
	cs := mcpSession(t)
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if a := tool.Annotations; (tool.Name == "verse_data_put" || tool.Name == "verse_data_rm") &&
			(a == nil || a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint != (tool.Name == "verse_data_rm")) {
			t.Errorf("%s is annotated %+v", tool.Name, a)
		}
	}
	out, isErr := callTool(t, cs, "verse_data_put", map[string]any{"at": "demo.pr.1", "value": "{stage: open}", "create": true})
	if isErr || !strings.Contains(out, "stage: open") {
		t.Fatalf("the put: error=%v\n%s", isErr, out)
	}
	if out, isErr := callTool(t, cs, "verse_data_put", map[string]any{"at": "demo.pr.1", "value": "{stage: x}", "create": true}); !isErr {
		t.Fatalf("a create where something stands was taken:\n%s", out)
	}
	if out, isErr := callTool(t, cs, "verse_data_put", map[string]any{"at": "demo.pr.1", "value": "{stage: x}", "expect": "{stage: closed}"}); !isErr {
		t.Fatalf("a put whose expect does not match was taken:\n%s", out)
	}
	if out, isErr := callTool(t, cs, "verse_data_put", map[string]any{"at": "demo.pr.1", "value": "{owner: ana}", "expect": "{stage: open}"}); isErr {
		t.Fatalf("a put whose expect matches: %s", out)
	}
	if got, err := runCLI(t, "data", "demo", "pr", "1"); err != nil || !strings.Contains(got, "stage: open") || !strings.Contains(got, "owner: ana") {
		t.Fatalf("the CLI reads %q, %v", got, err)
	}
	if out, isErr := callTool(t, cs, "verse_data_rm", map[string]any{"at": "demo.pr.1"}); isErr || out != "removed demo pr 1" {
		t.Fatalf("the remove said %q (error=%v)", out, isErr)
	}
	if got, _ := runCLI(t, "data", "demo", "pr", "1"); strings.Contains(got, "stage") {
		t.Fatalf("removed, the CLI still reads:\n%s", got)
	}
}

// A WRITE IS REFUSED before it is sent where it cannot be one: nobody to make it, no address, a
// wildcard, grants on a node that stands.
func TestTheDataToolsRefuseWhatIsNoWrite(t *testing.T) {
	serveAsking(t)
	for _, c := range []struct {
		who, tool string
		args      map[string]any
		want      string
	}{
		{"", "verse_data_put", map[string]any{"at": "demo.pr.1", "value": "{a: b}"}, "nobody is configured"},
		{tester, "verse_data_put", map[string]any{"at": "", "value": "{a: b}"}, "at says where to write"},
		{tester, "verse_data_rm", map[string]any{"at": "demo.pr.*"}, ""},
		{tester, "verse_data_put", map[string]any{"at": "demo.pr.1", "value": "{a: b}", "update": true, "grants": "[]"}, "creates the node"},
	} {
		out, isErr := callTool(t, mcpSessionAs(t, c.who), c.tool, c.args)
		if !isErr || !strings.Contains(out, c.want) {
			t.Errorf("%s %v: error=%v %q", c.tool, c.args, isErr, out)
		}
	}
}
