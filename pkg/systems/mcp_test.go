package systems

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// call is an mcp.call step's fields as the runner fills them: the server, the tool, and the
// call's arguments whole, an object.
func call(t *testing.T, server, tool, arguments string) *ir.Node {
	t.Helper()
	fields := FieldsDoc("mcp.call", map[string]string{"server": server, "tool": tool})
	var kvs []ir.KeyVal
	for i, k := range fields.Fields {
		kvs = append(kvs, ir.KeyVal{Key: k, Val: fields.Values[i]})
	}
	return ir.FromKeyVals(append(kvs, ir.KeyVal{Key: ir.FromString("arguments"), Val: pat(t, arguments)}))
}

// A SHAPE MATCHES A CALL'S ARGUMENTS OPEN, as tony matches an object: `{team: ENG}` admits a call
// whose team is ENG whatever else it gives, and refuses one whose team is another, or `null`, or
// absent. A shape that says nothing of the arguments admits any. A ceiling that pins an argument
// holds a shape that pins it within, and not one that leaves it open.
func TestACallsArgumentsAreMatchedOpen(t *testing.T) {
	if f, _ := FieldOf("mcp.call", "arguments"); f.Form != FormDoc || f.Default != "" {
		t.Fatalf("arguments: %+v", f)
	}
	e, err := readEnv(t, `systems: {mcp: [{server: linear, call: [{tool: !or [get_issue, list_issues]}, {tool: create_issue, arguments: {team: ENG}}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Check(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		server, tool, arguments string
		allowed                 bool
	}{
		{"linear", "get_issue", `{id: ENG-1}`, true},
		{"linear", "get_issue", `{}`, true},
		{"linear", "list_issues", `{filter: {state: [open, started]}, first: 50}`, true},
		{"linear", "create_issue", `{team: ENG, title: "the roof leaks", labels: [bug]}`, true},
		{"linear", "create_issue", `{team: OPS, title: x}`, false},
		{"linear", "create_issue", `{team: null, title: x}`, false},
		{"linear", "create_issue", `{title: x}`, false},
		{"linear", "delete_issue", `{id: ENG-1}`, false},
		{"search", "get_issue", `{id: ENG-1}`, false},
	} {
		if got := e.Allows("mcp.call", call(t, c.server, c.tool, c.arguments)); got != c.allowed {
			why, _ := e.Refuses("mcp.call", call(t, c.server, c.tool, c.arguments))
			t.Errorf("%s %s %s: allowed %v, want %v (%s)", c.server, c.tool, c.arguments, got, c.allowed, why)
		}
	}
	c := readCeiling(t, `{server: linear, call: [{tool: !glob "get_*"}, {tool: list_issues}, {tool: create_issue, arguments: {team: ENG}}]}`)
	if err := c.Check("mcp"); err != nil {
		t.Fatal(err)
	}
	for text, want := range map[string]string{
		`{server: linear, call: [{tool: get_issue}, {tool: create_issue, arguments: {team: ENG}}]}`: "",
		`{server: linear, call: {tool: create_issue, arguments: {team: ENG, priority: 1}}}`:         "",
		`{server: linear, call: {tool: create_issue}}`:                                              "within no shape",
		`{server: linear, call: {tool: create_issue, arguments: {team: OPS}}}`:                      "within no shape",
		`{server: linear, call: {tool: delete_issue}}`:                                              "within no shape",
		`{server: search, call: {tool: get_issue}}`:                                                 "server search is beyond linear",
	} {
		e, err := readEnv(t, `systems: {mcp: `+text+`}`)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Check(); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		err = c.Contains("mcp", e.Systems["mcp"][0])
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", text, err, want)
		}
	}
}
