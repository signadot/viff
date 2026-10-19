package calltool

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// socketDir is a short directory for a socket: a unix socket's path is bounded (104 bytes on
// macOS), and t.TempDir can be longer than that.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ct")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// fakeTools is a tool set like the runner's: a push with an enum and no required parameter, an
// ask that requires its question.
func fakeTools() []Tool {
	return []Tool{
		{
			Name:        "push",
			Description: "Push this task's committed work.",
			Params: map[string]any{
				// As the runner's own tools declare it: a Go string slice, not the decoded shape.
				"repo":   map[string]any{"type": "string", "enum": []string{"verse", "docs"}},
				"branch": map[string]any{"type": "string"},
				"delete": map[string]any{"type": "boolean"},
			},
		},
		{Name: "fetch", Description: "Bring the mirror up to date."},
		{
			Name:        "ask",
			Description: "Ask the person.",
			Params: map[string]any{
				"question": map[string]any{"type": "string"},
				"options":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			Required: []string{"question"},
		},
	}
}

type seen struct {
	tool  string
	input map[string]any
}

func start(t *testing.T, call func(ctx context.Context, tool string, in map[string]any) (string, bool, error)) (*Server, *Client) {
	t.Helper()
	s := &Server{Tools: fakeTools(), Call: call}
	path := filepath.Join(socketDir(t), "tools.sock")
	if err := s.Start(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, Dial(path)
}

func TestToolsAreListedAsTheModelSeesThem(t *testing.T) {
	_, c := start(t, nil)
	tools, err := c.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 3 || tools[0].Name != "push" || tools[2].Name != "ask" {
		t.Fatalf("tools: %+v", tools)
	}
	if tools[2].Required[0] != "question" || tools[0].Description != "Push this task's committed work." {
		t.Fatalf("what the list carries: %+v", tools)
	}
	repo, _ := tools[0].Params["repo"].(map[string]any)
	if enum, _ := repo["enum"].([]any); len(enum) != 2 || enum[0] != "verse" {
		t.Fatalf("the enum crossed as %#v", repo)
	}
}

func TestACallReachesTheHostAndItsResultTheCaller(t *testing.T) {
	var got seen
	_, c := start(t, func(_ context.Context, tool string, in map[string]any) (string, bool, error) {
		got = seen{tool, in}
		return "pushed verse to verse/x", false, nil
	})
	res, err := c.Call(context.Background(), "push", map[string]any{"branch": "verse/x", "delete": false})
	if err != nil {
		t.Fatal(err)
	}
	if res.Refused || res.Out != "pushed verse to verse/x" {
		t.Fatalf("result: %+v", res)
	}
	if got.tool != "push" || got.input["branch"] != "verse/x" || got.input["delete"] != false {
		t.Fatalf("the host saw %+v", got)
	}
}

func TestARefusalIsAnAnswer(t *testing.T) {
	_, c := start(t, func(context.Context, string, map[string]any) (string, bool, error) {
		return "refused: not on this branch", true, nil
	})
	res, err := c.Call(context.Background(), "push", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refused || res.Out != "refused: not on this branch" {
		t.Fatalf("result: %+v", res)
	}
}

func TestAFailureIsAnErrorInTheHostsWords(t *testing.T) {
	_, c := start(t, func(context.Context, string, map[string]any) (string, bool, error) {
		return "", false, errors.New("the remote is unreachable")
	})
	_, err := c.Call(context.Background(), "push", nil)
	if err == nil || !strings.Contains(err.Error(), "push: the remote is unreachable") {
		t.Fatalf("err = %v", err)
	}
}

func TestAnUnknownToolIsRefusedWithTheList(t *testing.T) {
	called := false
	_, c := start(t, func(context.Context, string, map[string]any) (string, bool, error) {
		called = true
		return "", false, nil
	})
	_, err := c.Call(context.Background(), "deploy", nil)
	if err == nil || !strings.Contains(err.Error(), `no tool "deploy"`) || !strings.Contains(err.Error(), "ask, fetch, push") {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Fatal("the host was asked to perform a tool it has not got")
	}
}

func TestOutCrossesAsItIs(t *testing.T) {
	// Free text with the shapes a pretty rendering trips on: trailing spaces, blank lines, a
	// colon, a leading dash.
	out := "pushed verse to verse/x:  \n\n- 3 commits \n  a: b\n"
	_, c := start(t, func(context.Context, string, map[string]any) (string, bool, error) {
		return out, false, nil
	})
	res, err := c.Call(context.Background(), "push", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Out != out {
		t.Fatalf("out crossed as %q, want %q", res.Out, out)
	}
}

func TestTheInputIsCheckedAgainstTheSchema(t *testing.T) {
	called := false
	_, c := start(t, func(context.Context, string, map[string]any) (string, bool, error) {
		called = true
		return "", false, nil
	})
	for _, tc := range []struct {
		tool  string
		input map[string]any
		want  string
	}{
		{"push", map[string]any{"repo": "other"}, `repo: "other" is not one of verse, docs`},
		{"push", map[string]any{"force": true}, `push has no parameter "force"`},
		{"push", map[string]any{"delete": "yes"}, "delete: want a boolean, not a string"},
		{"ask", map[string]any{"question": ""}, "ask requires question"},
		{"ask", map[string]any{"question": "x", "options": "yes"}, "options: want a list, not a string"},
		{"fetch", map[string]any{"now": true}, "fetch takes no parameters"},
	} {
		_, err := c.Call(context.Background(), tc.tool, tc.input)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %v: err = %v, want %q", tc.tool, tc.input, err, tc.want)
		}
	}
	if called {
		t.Fatal("the host was asked to perform a call the schema refuses")
	}
}

func TestANullIsAnAbsentParameter(t *testing.T) {
	var got map[string]any
	_, c := start(t, func(_ context.Context, _ string, in map[string]any) (string, bool, error) {
		got = in
		return "ok", false, nil
	})
	if _, err := c.Call(context.Background(), "push", map[string]any{"branch": nil, "delete": nil}); err != nil {
		t.Fatal(err)
	}
	if got["branch"] != nil || got["delete"] != nil {
		t.Fatalf("the host saw %v", got)
	}
}

func TestCheckReadsTheSchemaAsTheToolDeclaresIt(t *testing.T) {
	// Direct, with no wire in between: the enum is a []string, the number an int.
	tl, err := Describe(Tool{Name: "x", Params: map[string]any{
		"repo": map[string]any{"type": "string", "enum": []string{"a", "b"}},
		"n":    map[string]any{"type": "integer", "enum": []int{1, 2}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(tl, map[string]any{"repo": "c"}); err == nil || !strings.Contains(err.Error(), `repo: "c" is not one of a, b`) {
		t.Errorf("enum from a []string: %v", err)
	}
	if err := Check(tl, map[string]any{"repo": "a", "n": int64(2)}); err != nil {
		t.Errorf("a member: %v", err)
	}
	if err := Check(tl, map[string]any{"n": float64(2)}); err != nil {
		t.Errorf("the same number in another spelling: %v", err)
	}
	if err := Check(tl, map[string]any{"n": 3}); err == nil {
		t.Error("3 is not in [1, 2]")
	}
	// An enum of documents: compared as values, not crashed on.
	docs, _ := Describe(Tool{Name: "z", Params: map[string]any{"k": map[string]any{"type": "object", "enum": []any{map[string]any{"mode": "fast", "n": 1}}}}})
	if err := Check(docs, map[string]any{"k": map[string]any{"mode": "fast", "n": 1.0}}); err != nil {
		t.Errorf("a document in its enum, its number in another spelling: %v", err)
	}
	if err := Check(docs, map[string]any{"k": map[string]any{"mode": "slow", "n": 1}}); err == nil {
		t.Error("a document outside its enum")
	}
	// No type and an enum of numbers: the string "1" is not the number 1.
	untyped, _ := Describe(Tool{Name: "y", Params: map[string]any{"k": map[string]any{"enum": []int{1, 2}}}})
	if err := Check(untyped, map[string]any{"k": "1"}); err == nil {
		t.Error(`"1" matched the number 1`)
	}
	if err := Check(untyped, map[string]any{"k": 1}); err != nil {
		t.Errorf("1 in [1, 2]: %v", err)
	}
}

func TestAHostsErrorNamesTheToolOnce(t *testing.T) {
	_, c := start(t, func(context.Context, string, map[string]any) (string, bool, error) {
		return "", false, errors.New("ask: a question is required")
	})
	_, err := c.Call(context.Background(), "ask", map[string]any{"question": "x"})
	if err == nil || err.Error() != "ask: a question is required" {
		t.Fatalf("err = %v", err)
	}
}

func TestAMissingRequiredParamIsRefusedBeforeTheCall(t *testing.T) {
	called := false
	_, c := start(t, func(context.Context, string, map[string]any) (string, bool, error) {
		called = true
		return "", false, nil
	})
	_, err := c.Call(context.Background(), "ask", map[string]any{"options": []any{"yes"}})
	if err == nil || !strings.Contains(err.Error(), "ask requires question") {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Fatal("the host was asked to perform a call missing what the tool requires")
	}
}

func TestTheCallersContextEndsTheCall(t *testing.T) {
	arrived, ended := make(chan struct{}), make(chan struct{})
	_, c := start(t, func(ctx context.Context, _ string, _ map[string]any) (string, bool, error) {
		close(arrived)
		<-ctx.Done()
		close(ended)
		return "", false, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { c.Call(ctx, "ask", map[string]any{"question": "well?"}) }()
	<-arrived
	cancel()
	<-ended // the host's context ended with the caller's
}

func TestCloseRemovesTheSocket(t *testing.T) {
	s, _ := start(t, nil)
	path := s.Path()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("after Close, stat = %v", err)
	}
}

func TestStartRefusesASocketSomethingAnswers(t *testing.T) {
	s, _ := start(t, nil)
	second := &Server{}
	err := second.Start(s.Path())
	if err == nil || !strings.Contains(err.Error(), "already answered") {
		second.Close()
		t.Fatalf("err = %v", err)
	}
	// The first is still reachable.
	if _, err := Dial(s.Path()).Tools(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAToolWhoseSchemaCannotBeCarriedIsRefusedAtStart(t *testing.T) {
	s := &Server{Tools: []Tool{{Name: "odd", Params: map[string]any{"f": func() {}}}}}
	err := s.Start(filepath.Join(socketDir(t), "tools.sock"))
	if err == nil || !strings.Contains(err.Error(), "the schema of odd cannot be carried") {
		s.Close()
		t.Fatalf("err = %v", err)
	}
}

func TestTwoToolsOfOneNameAreRefusedAtStart(t *testing.T) {
	s := &Server{Tools: []Tool{{Name: "push"}, {Name: "push"}}}
	err := s.Start(filepath.Join(socketDir(t), "tools.sock"))
	if err == nil || !strings.Contains(err.Error(), `two tools named "push"`) {
		s.Close()
		t.Fatalf("err = %v", err)
	}
}

func TestStartLeavesAFileThatIsNotASocket(t *testing.T) {
	path := filepath.Join(socketDir(t), "tools.sock")
	if err := os.WriteFile(path, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	err := s.Start(path)
	if err == nil || !strings.Contains(err.Error(), "is not a socket") {
		s.Close()
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "mine" {
		t.Fatal("the file was touched")
	}
}

func TestStartClearsAStaleSocket(t *testing.T) {
	path := filepath.Join(socketDir(t), "tools.sock")
	// A socket whose server died without removing it.
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	s := &Server{}
	if err := s.Start(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := Dial(path).Tools(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAStatusWithNoWordsIsTheError(t *testing.T) {
	path := filepath.Join(socketDir(t), "tools.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})}
	go srv.Serve(ln)
	defer srv.Close()
	_, err = Dial(path).Tools(context.Background())
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v", err)
	}
}

func TestDescribeIsTheToolWithItsSchemaNormalised(t *testing.T) {
	tl := Tool{Name: "x", Description: "d", Params: map[string]any{"a": 1}, Required: []string{"a"}}
	d, err := Describe(tl)
	// The integer is the codec's int64, as it would be after a trip over the wire.
	if err != nil || d.Name != "x" || d.Description != "d" || d.Params["a"] != int64(1) || d.Required[0] != "a" {
		t.Fatalf("%+v, %v", d, err)
	}
	if _, err := Describe(Tool{Name: "odd", Params: map[string]any{"f": func() {}}}); err == nil {
		t.Fatal("a schema the codec cannot carry was described")
	}
}

// A tool with a parameter named `input` cannot be offered: verse-call-tool takes the whole
// input as `-input`, and the list grows on the server with no change to the binary.
func TestAToolWithAParameterNamedInputIsRefusedAtStart(t *testing.T) {
	s := &Server{Tools: []Tool{{Name: "odd", Params: map[string]any{Reserved: map[string]any{"type": "string"}}}}}
	err := s.Start(filepath.Join(socketDir(t), "tools.sock"))
	if err == nil || !strings.Contains(err.Error(), `odd has a parameter named "input"`) {
		s.Close()
		t.Fatalf("err = %v", err)
	}
}
