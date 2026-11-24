package delivery

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/signadot/tony-format/go-tony/ir"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd/mcpclient"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
)

// mcpCalls is what a test server's tools were called with: the arguments as the server read
// them, by tool, in order.
type mcpCalls struct {
	mu  sync.Mutex
	got map[string][]string
}

func (c *mcpCalls) record(tool, args string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got[tool] = append(c.got[tool], args)
}

// of is what tool was called with, each time.
func (c *mcpCalls) of(tool string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.got[tool])
}

// count is how many calls the server performed, of any tool.
func (c *mcpCalls) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, calls := range c.got {
		n += len(calls)
	}
	return n
}

// mcpServer is the SDK's own server under httptest, with instructions and the tools named, each
// with a schema of one property. A tool answers its arguments as text, and records them; `slow`
// waits until the call is given up, `err` answers an error of the server's, `image` an image
// beside a line, and `structured` structured content and no text.
func mcpServer(t *testing.T, instructions string, tools ...string) (string, *mcpCalls) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ServerOptions{Instructions: instructions})
	c := &mcpCalls{got: map[string][]string{}}
	for _, name := range tools {
		schema := map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string", "description": "the issue"}}}
		srv.AddTool(&mcp.Tool{Name: name, Description: "the tool " + name, InputSchema: schema},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				c.record(name, string(req.Params.Arguments))
				switch name {
				case "slow":
					<-ctx.Done()
					return nil, ctx.Err()
				case "err":
					return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "no such issue"}}}, nil
				case "image":
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "a picture:"},
						&mcp.ImageContent{Data: []byte{1, 2, 3}, MIMEType: "image/png"}}}, nil
				case "structured":
					return &mcp.CallToolResult{Content: []mcp.Content{}, StructuredContent: map[string]any{"n": 1}}, nil
				case "input":
					return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{"q": &mcp.ElicitParams{Mode: "form", Message: "which team?"}}}, nil
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}}}, nil
			})
	}
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	return ts.URL, c
}

// mcpLaunch is a launch whose environment gives one `mcp` block, the server linear, with the call
// shapes given, under policy.
func mcpLaunch(shapes []systems.Shape, policy task.Autonomy) *runner.Launch {
	return &runner.Launch{ID: "t1", Prompt: "p", Autonomy: policy, Environment: systems.Environment{Systems: systems.Systems{
		"mcp": {{Attributes: map[string]string{"server": "linear"}, Verbs: map[string][]systems.Shape{"call": shapes}}},
	}}}
}

// mcpRunner is a server that calls linear at url, with a call's timeout, under an open ceiling
// for mcp, and a task under l with its sessions, as Serve gives one.
func mcpRunner(t *testing.T, l *runner.Launch, url string, timeout time.Duration) (*Server, Task) {
	t.Helper()
	servers := mcpclient.Servers{"linear": {URL: url, Timeout: timeout}}
	sessions := mcpclient.New(servers, nil, io.Discard)
	t.Cleanup(func() { sessions.Close() })
	s := &Server{Log: io.Discard, Ceilings: performs("mcp"), Services: Services{MCP: servers}}
	return s, Task{ID: "t1", Launch: l, mcp: sessions}
}

// call is an `mcp.call` request of tool at linear with the arguments given.
func call(tool, arguments string) Request {
	return Request{Op: OpMCPCall, Body: arguments, Fields: map[string]string{"server": "linear", "tool": tool}}
}

// AN `mcp.call` IS READ INTO ONE STEP, AND REFUSED WHERE IT DOES NOT READ, before any row decides
// it and before the server hears of it: the server and the tool named, the arguments an object
// within the bound that JSON can carry. The step is the server, the tool and the arguments whole,
// so a server the environment gives no block for, a tool its shapes do not give, and arguments a
// shape pins otherwise are outside it; a shape matches the arguments open, so one that pins
// `team` admits a call with another argument beside it; a row decides on the tool as on any
// field. None of them reaches the server.
func TestAnMCPCallIsReadIntoOneStep(t *testing.T) {
	url, calls := mcpServer(t, "", "get_issue", "list_issues", "create_issue", "delete_issue")
	gets := []systems.Shape{{"tool": pattern(t, `!glob "get_*"`)}, {"tool": pattern(t, `create_issue`), "arguments": pattern(t, `{team: ENG}`)}}
	deny := task.Autonomy{
		{Name: "no-create", Match: &task.Match{System: "mcp", Verb: "call", Params: map[string]*ir.Node{"tool": pattern(t, `create_issue`)}}, Then: task.Then{Mode: task.ThenDeny}},
		{Then: task.Then{Mode: task.ThenAllow}},
	}
	ask := task.Autonomy{{Name: "ask-all", Match: &task.Match{System: "mcp"}, Then: task.Then{Mode: task.ThenAsk}}}
	big := `{"text": "` + strings.Repeat("x", ArgumentsMost) + `"}`
	for _, c := range []struct {
		name string
		l    *runner.Launch
		req  Request
		says string
	}{
		{"no body", mcpLaunch(gets, allow), call("get_issue", ""), "the arguments are an object"},
		{"a list", mcpLaunch(gets, allow), call("get_issue", `[1]`), "the arguments are an object"},
		{"a body that does not read", mcpLaunch(gets, allow), call("get_issue", `{"id": `), "the arguments do not read"},
		{"a tag", mcpLaunch(gets, allow), call("get_issue", `{id: !glob "ENG-*"}`), "the arguments hold a tag (!glob)"},
		{"a comment", mcpLaunch(gets, allow), call("get_issue", "# the issue\n{id: ENG-7}"), "the arguments hold a comment"},
		{"a key that is no string", mcpLaunch(gets, allow), call("get_issue", `{1: "x"}`), "the arguments hold a tag (!sparsearray)"}, // tony's integer keys
		{"over the bound", mcpLaunch(gets, allow), call("get_issue", big), "over the 65536 a call carries"},
		{"no tool", mcpLaunch(gets, allow), Request{Op: OpMCPCall, Body: `{}`, Fields: map[string]string{"server": "linear"}}, "a call names the tool"},
		{"a tool with a space", mcpLaunch(gets, allow), call("get issue", `{}`), `"get issue" is no tool's name`},
		{"no server", mcpLaunch(gets, allow), Request{Op: OpMCPCall, Body: `{}`, Fields: map[string]string{"tool": "get_issue"}}, "a call names the server"},
		{"a server the environment gives no block for", mcpLaunch(gets, allow), Request{Op: OpMCPCall, Body: `{}`, Fields: map[string]string{"server": "github", "tool": "get_issue"}}, "outside the environment"},
		{"a tool the shapes do not give", mcpLaunch(gets, allow), call("delete_issue", `{"id": "ENG-7"}`), "outside the environment"},
		{"an argument pinned otherwise", mcpLaunch(gets, allow), call("create_issue", `{"team": "OPS", "title": "x"}`), "outside the environment"},
		{"an argument pinned and left out", mcpLaunch(gets, allow), call("create_issue", `{"title": "x"}`), "outside the environment"},
		{"a row that denies the tool", mcpLaunch(gets, deny), call("create_issue", `{"team": "ENG", "title": "x"}`), "refused by the autonomy's row no-create"},
		{"a row that asks, with no answer relayed", mcpLaunch(gets, ask), call("get_issue", `{"id": "ENG-7"}`), "relayed no answer"},
	} {
		s, tk := mcpRunner(t, c.l, url, 0)
		rep := s.answer(tk, frame(t, c.req))
		if rep.OK || !strings.Contains(rep.Refused, c.says) {
			t.Errorf("%s: %+v, want refused: %s", c.name, rep, c.says)
		}
	}
	if n := calls.count(); n != 0 {
		t.Errorf("the server performed %d call(s) of requests that were refused", n)
	}
	// Within the environment, the call is performed: a pinned argument with another beside it.
	s, tk := mcpRunner(t, mcpLaunch(gets, deny), url, 0)
	if rep := s.answer(tk, frame(t, call("get_issue", `{"id": "ENG-7", "team": "OPS"}`))); !rep.OK || rep.Out != `{"id":"ENG-7","team":"OPS"}` {
		t.Errorf("a call within: %+v", rep)
	}
	if got := calls.of("get_issue"); !slices.Equal(got, []string{`{"id":"ENG-7","team":"OPS"}`}) {
		t.Errorf("the server was called with %q", got)
	}
	// The table: both apart, the call's deadline past the longest timeout a server may name;
	// and the task an op apart is handed keeps its sessions, with no checkout.
	if !ops[OpMCPCall].apart || !ops[OpMCPList].apart || ops[OpMCPCall].deadline <= mcpclient.TimeoutMost || ops[OpMCPList].deadline <= mcpclient.ListTimeout {
		t.Errorf("the table: call %+v, list %+v", ops[OpMCPCall], ops[OpMCPList])
	}
	if apart := tk.apart(); apart.mcp != tk.mcp || apart.Workspace != nil {
		t.Errorf("the task apart: %+v", apart)
	}
}

// WHAT IS SENT IS THE STEP'S ARGUMENTS, as JSON of that structure: nested objects and lists,
// numbers as spelled (`1.0` is not `1`), booleans, text that is not ASCII, and a `null` that
// stays a `null`, never an absent argument. The task's tool reads the same step from the request
// it sends (StepOf) as the runner does (ReadCall), so a row decides one step on both sides.
func TestTheCallSendsTheStepsArguments(t *testing.T) {
	url, calls := mcpServer(t, "", "save_issue")
	s, tk := mcpRunner(t, mcpLaunch([]systems.Shape{{}}, allow), url, 0)
	body := `{"n": 1.0, "m": 1, "assignee": null, "ok": true, "s": "007", "u": "héllo ☃", "e": {}, "l": [], "nest": {"a": [1, {"b": "x\ny"}]}, "x": 1e5, "q\"k": 2}`
	req := call("save_issue", body)
	if rep := s.answer(tk, frame(t, req)); !rep.OK {
		t.Fatalf("%+v", rep)
	}
	got := calls.of("save_issue")
	if len(got) != 1 {
		t.Fatalf("the server was called %d time(s)", len(got))
	}
	want, err := codec.ParseNode([]byte(body), codec.JSON)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := codec.ParseNode([]byte(got[0]), codec.JSON)
	if err != nil {
		t.Fatalf("%s: %v", got[0], err)
	}
	if !systems.Plain(sent).DeepEqual(systems.Plain(want)) {
		t.Errorf("the server was sent %s, want the structure of %s", got[0], body)
	}
	for _, piece := range []string{`"n":1.0`, `"m":1`, `"assignee":null`, `"ok":true`, `"s":"007"`, `"u":"héllo ☃"`, `"e":{}`, `"l":[]`, `"b":"x\ny"`, `"x":100000.0`, `"q\"k":2`} {
		if !strings.Contains(got[0], piece) {
			t.Errorf("the server was sent %s, want %s in it", got[0], piece)
		}
	}
	// The step on both sides, and its arguments as given.
	st, why := StepOf(req)
	c, _ := ReadCall("linear", "save_issue", body)
	if why != "" || !st.Same(c.Step()) {
		t.Errorf("the tool's step %s (%s) and the runner's %s", st, why, c.Step())
	}
	if args := st.Field("arguments"); args == nil || !args.DeepEqual(systems.Plain(want)) {
		t.Errorf("the step's arguments are %v", args)
	}
}

// A `null` IS NOT AN ABSENT ARGUMENT. A call with `assignee: null` reaches the server with
// `"assignee": null` and one with no `assignee` reaches it with none; the two are not the same
// step, differing in `arguments.assignee`, so an answer about one does not perform the other; a
// shape that pins `assignee` to a value refuses the `null`.
func TestANullArgumentStaysANull(t *testing.T) {
	url, calls := mcpServer(t, "", "save_issue")
	s, tk := mcpRunner(t, mcpLaunch([]systems.Shape{{}}, allow), url, 0)
	for _, body := range []string{`{"id": "ENG-7", "assignee": null}`, `{"id": "ENG-7"}`} {
		if rep := s.answer(tk, frame(t, call("save_issue", body))); !rep.OK {
			t.Fatalf("%s: %+v", body, rep)
		}
	}
	if got := calls.of("save_issue"); !slices.Equal(got, []string{`{"id":"ENG-7","assignee":null}`, `{"id":"ENG-7"}`}) {
		t.Errorf("the server was called with %q", got)
	}
	unassign, _ := ReadCall("linear", "save_issue", `{"id": "ENG-7", "assignee": null}`)
	keep, _ := ReadCall("linear", "save_issue", `{"id": "ENG-7"}`)
	if unassign.Step().Same(keep.Step()) || !slices.Equal(unassign.Step().Differs(keep.Step()), []string{"arguments.assignee"}) {
		t.Errorf("the two steps: %s and %s differ in %v", unassign.Step(), keep.Step(), unassign.Step().Differs(keep.Step()))
	}
	pinned := mcpLaunch([]systems.Shape{{"arguments": pattern(t, `{assignee: alice}`)}}, allow)
	s, tk = mcpRunner(t, pinned, url, 0)
	if rep := s.answer(tk, frame(t, call("save_issue", `{"id": "ENG-7", "assignee": null}`))); rep.OK || !strings.Contains(rep.Refused, "outside the environment") {
		t.Errorf("a null where a shape pins a value: %+v", rep)
	}
	if rep := s.answer(tk, frame(t, call("save_issue", `{"id": "ENG-7", "assignee": "alice"}`))); !rep.OK {
		t.Errorf("the value the shape pins: %+v", rep)
	}
}

// AN ANSWER ABOUT A CALL PERFORMS THAT CALL AND NO OTHER. Under an `ask` row, the runner performs
// a call on the answer it relayed for the decision the request names, where the step is the one
// the question was about; a call sent with other arguments under the same decision is refused,
// the refusal naming the argument that differs and never its value, and the server hears nothing
// of it.
func TestAnAnswerAboutOtherArgumentsDoesNotPerformTheCall(t *testing.T) {
	url, calls := mcpServer(t, "", "save_issue")
	ask := task.Autonomy{{Name: "ask-all", Match: &task.Match{System: "mcp"}, Then: task.Then{Mode: task.ThenAsk}}}
	s, tk := mcpRunner(t, mcpLaunch([]systems.Shape{{}}, ask), url, 0)
	// What alice answered proceed to: an unassign of ENG-7.
	about, _ := ReadCall("linear", "save_issue", `{"id": "ENG-7", "assignee": null}`)
	tk.Answered = func(st task.Step, decision string) (string, []string, bool) {
		if decision != "t1~d1" {
			return "", nil, false
		}
		if differ := about.Step().Differs(st); differ != nil {
			return "", differ, false
		}
		return "alice", nil, true
	}
	other := call("save_issue", `{"id": "ENG-7", "assignee": "mallory"}`)
	other.Decision = "t1~d1"
	rep := s.answer(tk, frame(t, other))
	if rep.OK || !strings.Contains(rep.Refused, "this one differs in arguments.assignee") || strings.Contains(rep.Refused, "mallory") {
		t.Errorf("a call under another call's answer: %+v", rep)
	}
	if n := calls.count(); n != 0 {
		t.Fatalf("the server performed %d call(s)", n)
	}
	same := call("save_issue", `{"id": "ENG-7", "assignee": null}`)
	same.Decision = "t1~d1"
	if rep := s.answer(tk, frame(t, same)); !rep.OK {
		t.Errorf("the call that was answered: %+v", rep)
	}
	if got := calls.of("save_issue"); !slices.Equal(got, []string{`{"id":"ENG-7","assignee":null}`}) {
		t.Errorf("the server was called with %q", got)
	}
}

// listed is an `mcp.list` answer as the task reads it.
type listed struct {
	Instructions string `tony:"field=instructions"`
	Tools        []struct {
		Name        string   `tony:"field=name"`
		Description string   `tony:"field=description"`
		Input       *ir.Node `tony:"field=input"`
	} `tony:"field=tools"`
	Cut string `tony:"field=cut,omitzero"`
}

// list asks s for server's list for tk, and reads the answer.
func list(t *testing.T, s *Server, tk Task, server string) listed {
	t.Helper()
	rep := s.answer(tk, frame(t, Request{Op: OpMCPList, Fields: map[string]string{"server": server}}))
	if !rep.OK {
		t.Fatalf("%+v", rep)
	}
	var out listed
	if err := codec.UnmarshalStrict([]byte(rep.Out), &out, codec.Tony); err != nil {
		t.Fatalf("%v in %s", err, rep.Out)
	}
	return out
}

// THE LIST IS THE TOOLS THE ENVIRONMENT AND THE CEILING ADMIT SOME CALL OF, each with its
// description and its schema, and the server's instructions, cut at a bound; a tool no shape of
// either could admit is left out, whatever the server says of it, and under no ceiling for mcp
// nothing is listed. A server the environment gives no block for is refused, as a call of it
// would be; one the runner does not call is an error.
func TestTheListIsTheToolsTheEnvironmentAndTheCeilingAdmit(t *testing.T) {
	instructions := "call get_issue first. " + strings.Repeat("y", InstructionsMost)
	url, _ := mcpServer(t, instructions, "get_issue", "list_issues", "create_issue", "delete_issue", "archive_project")
	shapes := []systems.Shape{{"tool": pattern(t, `!glob "*_issue"`)}, {"tool": pattern(t, `list_issues`), "arguments": pattern(t, `{team: ENG}`)}}
	s, tk := mcpRunner(t, mcpLaunch(shapes, allow), url, 0)
	s.Ceilings["mcp"] = systems.Ceiling{{Attributes: map[string]*ir.Node{"server": pattern(t, `linear`)},
		Verbs: map[string][]systems.Shape{"call": {{"tool": pattern(t, `!not delete_issue`)}}}}}
	out := list(t, s, tk, "linear")
	var names []string
	for _, tl := range out.Tools {
		names = append(names, tl.Name)
		if tl.Description != "the tool "+tl.Name || ir.Get(tl.Input, "properties") == nil {
			t.Errorf("the tool %s: %q, %v", tl.Name, tl.Description, tl.Input)
		}
	}
	// delete_issue is the ceiling's to leave out and archive_project the environment's, and
	// list_issues's shape pins an argument, which leaves the tool in: some call of it is within.
	// The server lists by name.
	if !slices.Equal(names, []string{"create_issue", "get_issue", "list_issues"}) || out.Cut != "" {
		t.Errorf("listed %v, cut %q", names, out.Cut)
	}
	if !strings.HasPrefix(out.Instructions, "call get_issue first.") || len(out.Instructions) > InstructionsMost+200 ||
		!strings.Contains(out.Instructions, "[cut: the instructions are") {
		t.Errorf("the instructions: %d bytes, ending %q", len(out.Instructions), out.Instructions[max(0, len(out.Instructions)-80):])
	}
	delete(s.Ceilings, "mcp")
	if out := list(t, s, tk, "linear"); len(out.Tools) != 0 {
		t.Errorf("under no ceiling for mcp: %v", out.Tools)
	}
	s.Ceilings["mcp"] = systems.Open()
	rep := s.answer(tk, frame(t, Request{Op: OpMCPList, Fields: map[string]string{"server": "github"}}))
	if rep.OK || !strings.Contains(rep.Refused, "outside the environment") || !strings.Contains(rep.Refused, "no block for server github") {
		t.Errorf("a server the environment gives no block for: %+v", rep)
	}
	uncalled := mcpLaunch([]systems.Shape{{}}, allow)
	uncalled.Environment.Systems["mcp"][0].Attributes["server"] = "github"
	_, tk = mcpRunner(t, uncalled, url, 0)
	rep = s.answer(tk, frame(t, Request{Op: OpMCPList, Fields: map[string]string{"server": "github"}}))
	if rep.OK || !strings.Contains(rep.Error, "calls no server github") {
		t.Errorf("a server the runner does not call: %+v", rep)
	}
	// A call to it was not made, and says so, rather than that it is not known.
	rep = s.answer(tk, frame(t, Request{Op: OpMCPCall, Body: `{}`, Fields: map[string]string{"server": "github", "tool": "get_issue"}}))
	if rep.OK || rep.Error != "this runner calls no server github, so the call of get_issue was not made" {
		t.Errorf("a call to a server the runner does not call: %+v", rep)
	}
}

// THE LIST FITS A FRAME: a tool's schema past its bound is replaced by the least one, said in the
// description, and the tools past the list's bound are left out, said under `cut`; a tool whose
// schema does not read is left out alone, and the rest are listed.
func TestTheListIsBounded(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	nothing := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	}
	// 60 tools of 14 KiB each come to over ListMost; `huge` is over SchemaMost on its own.
	wide := map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string", "description": strings.Repeat("d", 14<<10)}}}
	for i := range 60 {
		srv.AddTool(&mcp.Tool{Name: fmt.Sprintf("t%02d", i), InputSchema: wide}, nothing)
	}
	srv.AddTool(&mcp.Tool{Name: "a_huge", Description: "big", InputSchema: map[string]any{"type": "object",
		"properties": map[string]any{"text": map[string]any{"type": "string", "description": strings.Repeat("d", SchemaMost+1)}}}}, nothing)
	srv.AddTool(&mcp.Tool{Name: "a_small", InputSchema: map[string]any{"type": "object"}}, nothing)
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	s, tk := mcpRunner(t, mcpLaunch([]systems.Shape{{}}, allow), ts.URL, 0)
	out := list(t, s, tk, "linear")
	if len(out.Tools) == 0 || out.Tools[0].Name != "a_huge" || ir.Get(out.Tools[0].Input, "properties") != nil ||
		!strings.Contains(out.Tools[0].Description, "big [cut: the schema is") {
		t.Errorf("the huge schema: %+v", out.Tools[:min(2, len(out.Tools))])
	}
	if len(out.Tools) < 3 || len(out.Tools) >= 62 || !strings.Contains(out.Cut, "left out: the list is over the") {
		t.Errorf("%d tools listed, cut %q", len(out.Tools), out.Cut)
	}
	raw, _ := codec.Marshal(out, codec.Tony)
	if len(raw) > ListMost+SchemaMost+DescriptionMost {
		t.Errorf("the answer is %d bytes", len(raw))
	}
}

// A CALL THAT DOES NOT COMPLETE SAYS SO, AND THAT WHETHER THE SERVER PERFORMED IT IS NOT KNOWN:
// one given up at the server's timeout, one to a server that is gone, and one on a runner that
// calls no server at all. A result is cut at its bound at a rune's boundary, saying so, and an
// error of the server's with no text is said to have none.
func TestAnMCPCallThatDoesNotCompleteSaysSo(t *testing.T) {
	url, _ := mcpServer(t, "", "slow")
	s, tk := mcpRunner(t, mcpLaunch([]systems.Shape{{}}, allow), url, 200*time.Millisecond)
	rep := s.answer(tk, frame(t, call("slow", `{}`)))
	if rep.OK || !strings.Contains(rep.Error, "given up at the server's timeout (200ms)") || !strings.Contains(rep.Error, "not known") {
		t.Errorf("a call past the timeout: %+v", rep)
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	s, tk = mcpRunner(t, mcpLaunch([]systems.Shape{{}}, allow), gone.URL, 0)
	rep = s.answer(tk, frame(t, call("slow", `{}`)))
	if rep.OK || !strings.Contains(rep.Error, "did not complete") || !strings.Contains(rep.Error, "not known") {
		t.Errorf("a call to a server that is gone: %+v", rep)
	}
	long := strings.Repeat("é", ResultMost)
	rep = mcpResult(mcpclient.Result{Text: long})
	if !rep.OK || !utf8.ValidString(rep.Out) || len(rep.Out) > ResultMost+100 ||
		!strings.HasSuffix(rep.Out, fmt.Sprintf("[cut: the result is %d bytes, and the runner passes on %d]", len(long), ResultMost)) {
		t.Errorf("a long result: %d bytes, valid %t, ending %q", len(rep.Out), utf8.ValidString(rep.Out), rep.Out[max(0, len(rep.Out)-80):])
	}
	if rep := mcpResult(mcpclient.Result{IsError: true}); !rep.OK || rep.Out != "the server answered an error with no text" {
		t.Errorf("an error with no text: %+v", rep)
	}
	none := &Server{Log: io.Discard, Ceilings: performs("mcp")}
	for _, req := range []Request{call("slow", `{}`), {Op: OpMCPList, Fields: map[string]string{"server": "linear"}}} {
		if rep := none.answer(Task{ID: "t1", Launch: mcpLaunch([]systems.Shape{{}}, allow)}, frame(t, req)); rep.OK || !strings.Contains(rep.Error, "calls no MCP server") {
			t.Errorf("%s on a runner with no servers: %+v", req.Op, rep)
		}
	}
}

// A RESULT IS THE SERVER'S WORD: its text, an error of the server's marked as such and no error
// of the runner's, content that is not text named in its place, and structured content as JSON
// text where there is no text. A server's answer that the call is refused is a refusal in the
// server's words, which the task re-plans on: a tool the server has not, and a tool that asked
// for input the runner does not give.
func TestAnMCPResultIsTheServersWord(t *testing.T) {
	url, _ := mcpServer(t, "", "err", "image", "structured", "input")
	s, tk := mcpRunner(t, mcpLaunch([]systems.Shape{{}}, allow), url, 0)
	for tool, want := range map[string]string{
		"err":        "the server answered an error:\nno such issue",
		"image":      "a picture:\n[an image, image/png, left out]",
		"structured": `{"n":1}`,
	} {
		if rep := s.answer(tk, frame(t, call(tool, `{}`))); !rep.OK || rep.Out != want {
			t.Errorf("%s: %+v, want %q", tool, rep, want)
		}
	}
	for tool, says := range map[string]string{"nothing": "nothing", "input": "elicitation"} {
		rep := s.answer(tk, frame(t, call(tool, `{}`)))
		if rep.OK || !strings.HasPrefix(rep.Refused, "linear refused the call of "+tool+": ") || !strings.Contains(rep.Refused, says) {
			t.Errorf("%s: %+v, want the server's refusal saying %q", tool, rep, says)
		}
	}
}
