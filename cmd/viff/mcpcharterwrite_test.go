package main

import (
	"strings"
	"testing"
	"time"
)

// THE INSTALL TOOL INSTALLS AS `viff c -a` DOES, AND SAYS WHAT WENT WHERE: one charter from
// text, the shorthand at an address, and a tree with a prune; the remove tool takes one rule out,
// then a package, as `viff c -r` does. The command reads back each step.
func TestTheCharterToolsInstallAndRemove(t *testing.T) {
	serve(t)
	cs := mcpSession(t)

	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "viff_charter_install" || tool.Name == "viff_charter_remove" {
			if a := tool.Annotations; a == nil || a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint {
				t.Errorf("%s is not annotated a destructive write: %+v", tool.Name, a)
			}
		}
	}

	out, isErr := callTool(t, cs, "viff_charter_install", map[string]any{"charters": onlyRule("sync.one")})
	if isErr || !strings.Contains(out, "package sync.one: installed 2 stratum(a) and 1 trigger(s)") ||
		!strings.Contains(out, "viff_charter shows every charter") {
		t.Fatalf("one charter: error=%v\n%s", isErr, out)
	}
	if out, err := runCLI(t, "charter", "sync", "one", "rules", "mark"); err != nil || !strings.Contains(out, "name: mark") {
		t.Fatalf("the command does not read the rule the tool installed: %v\n%s", err, out)
	}

	rule := func(name string) string {
		return `{name: ` + name + `, stratum: src, condition: {subject: {path: "demo.item.*"}}, ` +
			`action: {derive: [{path: "sync.one.row", id: .[subject.value.id], value: {` + name + `: true}}]}}`
	}
	if out, isErr := callTool(t, cs, "viff_charter_install", map[string]any{"at": "sync.one",
		"charters": "[" + rule("again") + ", " + rule("more") + "]"}); isErr ||
		!strings.Contains(out, "package sync.one: installed 2 trigger(s)") {
		t.Fatalf("a list of rules at an address: error=%v\n%s", isErr, out)
	}
	for _, name := range []string{"again", "more"} {
		if out, err := runCLI(t, "charter", "sync", "one", "rules", name); err != nil {
			t.Fatalf("the command does not read rule %s installed at sync.one: %v\n%s", name, err, out)
		}
	}

	out, isErr = callTool(t, cs, "viff_charter_install", map[string]any{"at": "sync", "prune": true, "charters": onlyRule("sync.two")})
	if isErr || !strings.Contains(out, "package sync.two: installed") ||
		!strings.Contains(out, "package sync.one: uninstalled, not in the charters given") {
		t.Fatalf("a tree with a prune: error=%v\n%s", isErr, out)
	}
	if _, err := runCLI(t, "charter", "sync", "one"); err == nil {
		t.Fatal("the prune left sync.one installed")
	}

	if out, isErr := callTool(t, cs, "viff_charter_remove", map[string]any{"path": "sync.two.rules.mark"}); isErr || out != "removed the rule mark" {
		t.Fatalf("one rule: error=%v\n%s", isErr, out)
	}
	if out, err := runCLI(t, "charter", "sync", "two", "rules", "mark"); err == nil {
		t.Fatalf("the rule is still there:\n%s", out)
	}
	if out, isErr := callTool(t, cs, "viff_charter_remove", map[string]any{"path": "sync.two"}); isErr ||
		out != "uninstalled the charter at sync.two; what it wrote is still there" {
		t.Fatalf("the package: error=%v\n%s", isErr, out)
	}
	if out, isErr := callTool(t, cs, "viff_charter_remove", map[string]any{"path": "sync.two"}); !isErr ||
		!strings.Contains(out, "no charter is installed at sync.two") {
		t.Fatalf("a package that is gone, in the server's words: error=%v\n%s", isErr, out)
	}
}

// AT REACHES WHAT A PATH CANNOT, as `--` does on the command line: with packages at x and at
// x.rules, the package at x.rules is removed by at alone, and x stays. A schedule removed is said
// to be one, and its package stays.
func TestTheRemoveToolTakesAtAndSchedules(t *testing.T) {
	serve(t)
	dir := writeTree(t, map[string]string{"x.tony": onlyRule("x"), "xr.tony": onlyRule("x.rules")})
	if out, err := runCLI(t, "charter", "-a", "-d", dir); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	cs := mcpSession(t)
	if out, isErr := callTool(t, cs, "viff_charter_remove", map[string]any{"at": "x.rules"}); isErr ||
		out != "uninstalled the charter at x.rules; what it wrote is still there" {
		t.Fatalf("the package at x.rules: error=%v\n%s", isErr, out)
	}
	if out, err := runCLI(t, "charter", "x"); err != nil || !strings.Contains(out, "at: x\n") {
		t.Fatalf("x went with x.rules: %v\n%s", err, out)
	}

	sweep := "at: sync.three\nschedules:\n- name: sweep\n  every: 3h\n"
	if out, isErr := callTool(t, cs, "viff_charter_install", map[string]any{"charters": sweep}); isErr {
		t.Fatalf("a schedule: %s", out)
	}
	if out, isErr := callTool(t, cs, "viff_charter_remove", map[string]any{"path": "sync.three.schedules.sweep"}); isErr ||
		out != "removed the schedule sweep" {
		t.Fatalf("the schedule: error=%v\n%s", isErr, out)
	}
	if out, err := runCLI(t, "charter", "sync", "three"); err != nil {
		t.Fatalf("the package went with its schedule: %v\n%s", err, out)
	}
}

// WHAT THE COMMAND REFUSES BEFORE SENDING, the tools refuse, in their own words: an install of
// nothing, several charters at one address without a prune, a prune of no address, a tree holding
// what is no charter, and a removal of nothing. Nobody configured installs nothing.
func TestTheCharterWriteToolsRefuse(t *testing.T) {
	serve(t)
	cs := mcpSession(t)
	two := onlyRule("sync.one") + "---\n" + onlyRule("sync.two")
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"viff_charter_install", map[string]any{"charters": ""}, "an install carries the charters"},
		{"viff_charter_install", map[string]any{"at": "sync", "charters": two}, "each installed at their own at:"},
		{"viff_charter_install", map[string]any{"prune": true, "charters": two}, "name it in at"},
		{"viff_charter_install", map[string]any{"charters": two + "---\n{name: x}"}, "document 3 is no charter"},
		{"viff_charter_install", map[string]any{"at": "sync", "prune": true, "charters": "{name: x}"}, "document 1 is no charter"},
		{"viff_charter_remove", map[string]any{"path": ""}, "path names what to remove"},
		{"viff_charter_remove", map[string]any{}, "path names what to remove"},
	} {
		if out, isErr := callTool(t, cs, c.tool, c.args); !isErr || !strings.Contains(out, c.want) || strings.Contains(out, "--") {
			t.Errorf("%s %v: error=%v\n%s", c.tool, c.args, isErr, out)
		}
	}
	if out, err := runCLI(t, "charter", "sync", "one"); err == nil {
		t.Fatalf("a refused install installed:\n%s", out)
	}

	nobody := mcpSessionAs(t, "")
	for tool, args := range map[string]map[string]any{
		"viff_charter_install": {"charters": onlyRule("sync.one")},
		"viff_charter_remove":  {"path": "sync.one"},
	} {
		if out, isErr := callTool(t, nobody, tool, args); !isErr || !strings.Contains(out, "nobody is configured") {
			t.Errorf("%s as nobody: error=%v\n%s", tool, isErr, out)
		}
	}
}

// A SUSPENSION OF A CHARTER'S RULE, MADE THROUGH THE TOOL, HOLDS ITS NEXT FIRING, and both the
// tool's listing and the command's show it; a resume through the tool releases what it held. A say
// reaches the live tasks a charter names, of which there are none here.
func TestTheFlowToolsSuspendAndResumeACharter(t *testing.T) {
	serveAsking(t)
	installSync(t)
	cs := mcpSession(t)

	if out, isErr := callTool(t, cs, "viff_flow_suspend", map[string]any{"charter": "sync.one:mark", "why": "reading it"}); isErr {
		t.Fatalf("suspend: %s", out)
	}
	if out, _ := callTool(t, cs, "viff_flow", nil); !strings.Contains(out, "reading it") || !strings.Contains(out, "sync.one:mark") {
		t.Fatalf("the tool's listing does not hold the suspension:\n%s", out)
	}
	if out, err := runCLI(t, "flow"); err != nil || !strings.Contains(out, "reading it") {
		t.Fatalf("the command's listing does not hold the suspension: %v\n%s", err, out)
	}

	if _, err := runCLI(t, "data", "demo", "item", "y", "--put", "{id: y}"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if out, isErr := callTool(t, cs, "viff_data", map[string]any{"at": "sync.one.row.y"}); !isErr && strings.Contains(out, "seen: true") {
		t.Fatalf("the suspended rule fired:\n%s", out)
	}
	if out, isErr := callTool(t, cs, "viff_flow_say", map[string]any{"charter": "sync.one:mark", "text": "hello"}); isErr ||
		!strings.Contains(out, "affected: 0") {
		t.Fatalf("say to a charter: error=%v\n%s", isErr, out)
	}

	if out, isErr := callTool(t, cs, "viff_flow_resume", map[string]any{"charter": "sync.one:mark"}); isErr {
		t.Fatalf("resume: %s", out)
	}
	pollTool(t, cs, "viff_data", map[string]any{"at": "sync.one.row.y"}, "seen: true")
	if out, _ := callTool(t, cs, "viff_flow", nil); strings.Contains(out, "reading it") {
		t.Fatalf("the suspension outlived its resume:\n%s", out)
	}
}

// A FLOW VERB THAT DOES NOT SAY WHAT IT ACTS ON IS REFUSED BEFORE ANYTHING IS SENT, in the tool's
// words.
func TestTheFlowToolsRefuseWhatNamesNothing(t *testing.T) {
	serve(t)
	cs := mcpSession(t)
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"viff_flow_suspend", map[string]any{"why": "x"}, "task <id>, or charter"},
		{"viff_flow_suspend", map[string]any{}, "task <id>, or charter"},
		{"viff_flow_suspend", map[string]any{"recursive": true}, "task <id>, or charter"},
		{"viff_flow_suspend", map[string]any{"subject": "demo.pr.1", "recursive": true, "why": "x"}, "so it wants charter"},
		{"viff_flow_suspend", map[string]any{"task": "t1", "recursive": true}, "nor recursive"},
		{"viff_flow", map[string]any{"task": "t1", "recursive": true}, "nor recursive"},
		{"viff_flow_suspend", map[string]any{"charter": "sync.one"}, "give why"},
		{"viff_flow_suspend", map[string]any{"task": "t1", "charter": "sync.one", "why": "x"}, "combines with neither"},
		{"viff_flow_resume", map[string]any{"recursive": true}, "task <id>, or charter"},
		{"viff_flow_resume", map[string]any{"subject": "demo.pr.1", "recursive": true}, "so it wants charter"},
		{"viff_flow_say", map[string]any{"charter": "sync.one", "text": ""}, "wants the text to say"},
		{"viff_flow_cancel", map[string]any{"task": ""}, "cancel names one run; suspend a charter or a subject"},
		{"viff_flow_say", map[string]any{"text": "hi"}, "what to say it to"},
	} {
		if out, isErr := callTool(t, cs, c.tool, c.args); !isErr || !strings.Contains(out, c.want) || strings.Contains(out, "--") {
			t.Errorf("%s %v: error=%v\n%s", c.tool, c.args, isErr, out)
		}
	}
}

// A FLOW VERB AS NOBODY IS REFUSED FOR THAT FIRST, as every write is, whatever else it lacks.
func TestTheFlowToolsRefuseNobodyFirst(t *testing.T) {
	serve(t)
	nobody := mcpSessionAs(t, "")
	for _, tool := range []string{"viff_flow_cancel", "viff_flow_suspend", "viff_flow_resume", "viff_flow_say"} {
		args := map[string]any{"task": ""}
		if tool == "viff_flow_say" {
			args["text"] = ""
		}
		if out, isErr := callTool(t, nobody, tool, args); !isErr || !strings.Contains(out, "nobody is configured") {
			t.Errorf("%s as nobody: error=%v\n%s", tool, isErr, out)
		}
	}
}
