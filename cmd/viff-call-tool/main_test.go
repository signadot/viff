package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/runner/runnerd/calltool"
)

// A tool set shaped like the runner's: push with an enum and a boolean, ask with a required
// string and a list, and one tool with a number and a document, so every kind of flag is
// exercised.
func tools() []calltool.Tool {
	return []calltool.Tool{
		{
			Name:        "push",
			Description: "Push this task's committed work to a branch of a repository.",
			Params: map[string]any{
				"repo":   map[string]any{"type": "string", "enum": []string{"viff", "docs"}},
				"branch": map[string]any{"type": "string", "description": "where it goes"},
				"delete": map[string]any{"type": "boolean"},
			},
		},
		{
			Name:        "ask",
			Description: "Say something to the person this task runs for and wait for their reply.",
			Params: map[string]any{
				"question": map[string]any{"type": "string"},
				"options":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"within":   map[string]any{"type": "string"},
			},
			Required: []string{"question"},
		},
		{Name: "fetch", Description: "Bring the mirror up to date."},
		{
			Name: "shape",
			Params: map[string]any{
				"count":  map[string]any{"type": "integer"},
				"ratio":  map[string]any{"type": "number"},
				"fields": map[string]any{"type": "object"},
			},
		},
	}
}

type host struct {
	tool   string
	input  map[string]any
	out    string
	refuse bool
	err    error
}

func (h *host) call(_ context.Context, tool string, in map[string]any) (string, bool, error) {
	h.tool, h.input = tool, in
	return h.out, h.refuse, h.err
}

// serve stands up a server on a short socket path and points $VIFF_TOOLS at it.
func serve(t *testing.T, h *host) {
	t.Helper()
	dir, err := os.MkdirTemp("", "vct")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s := &calltool.Server{Tools: tools(), Call: h.call}
	path := filepath.Join(dir, "tools.sock")
	if err := s.Start(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	t.Setenv(calltool.Env, path)
}

func exec(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(context.Background(), args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestNoToolListsWhatIsOnOffer(t *testing.T) {
	serve(t, &host{})
	code, out, _ := exec(t, "")
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{
		"push\n", "Push this task's committed work",
		"-repo <string>  one of: viff, docs", "-delete (or -delete=false)\n", "-branch <string>\n        where it goes",
		"-question <string>  (required)", "-options <string>  (repeat for each)",
		"-count <integer>", "-fields <document>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing lacks %q:\n%s", want, out)
		}
	}
	// required first, then by name
	if strings.Index(out, "-question") > strings.Index(out, "-options") {
		t.Errorf("a required parameter is listed after an optional one:\n%s", out)
	}
}

func TestFlagsBecomeTheInputAndTheResultIsStdout(t *testing.T) {
	h := &host{out: "pushed viff to viff/x (3 commits)"}
	serve(t, h)
	code, out, errb := exec(t, "", "push", "-repo", "viff", "-branch", "viff/x", "-delete")
	if code != exitOK || errb != "" {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if out != "pushed viff to viff/x (3 commits)\n" {
		t.Fatalf("stdout %q", out)
	}
	if h.tool != "push" || h.input["repo"] != "viff" || h.input["branch"] != "viff/x" || h.input["delete"] != true {
		t.Fatalf("the host saw %s %v", h.tool, h.input)
	}
}

func TestOnlyWhatWasGivenIsSent(t *testing.T) {
	h := &host{}
	serve(t, h)
	if code, _, _ := exec(t, "", "push"); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if len(h.input) != 0 {
		t.Fatalf("a bare call sent %v", h.input)
	}
}

func TestAListIsTheFlagRepeated(t *testing.T) {
	h := &host{out: "walter chose yes"}
	serve(t, h)
	code, out, _ := exec(t, "", "ask", "-question", "ship it?", "-options", "yes", "-options", "no", "-within", "30m")
	if code != exitOK || out != "walter chose yes\n" {
		t.Fatalf("exit %d, out %q", code, out)
	}
	opts, _ := h.input["options"].([]any)
	if len(opts) != 2 || opts[0] != "yes" || opts[1] != "no" || h.input["within"] != "30m" {
		t.Fatalf("the host saw %v", h.input)
	}
}

func TestNumbersAndDocumentsAreTyped(t *testing.T) {
	h := &host{}
	serve(t, h)
	code, _, errb := exec(t, "", "shape", "-count", "3", "-ratio", "0.5", "-fields", `{a: 1, b: [x, y]}`)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errb)
	}
	if h.input["count"] != int64(3) || h.input["ratio"] != 0.5 {
		t.Fatalf("numbers: %#v", h.input)
	}
	fields, _ := h.input["fields"].(map[string]any)
	if fields == nil || fields["a"] != int64(1) {
		t.Fatalf("the document: %#v", h.input["fields"])
	}
}

func TestAnInputDocumentWithFlagsOverIt(t *testing.T) {
	h := &host{}
	serve(t, h)
	code, _, errb := exec(t, "", "push", "-input", `{repo: docs, branch: old}`, "-branch", "new")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errb)
	}
	if h.input["repo"] != "docs" || h.input["branch"] != "new" {
		t.Fatalf("the host saw %v", h.input)
	}
}

func TestTheInputDocumentMayComeFromStdin(t *testing.T) {
	h := &host{}
	serve(t, h)
	code, _, errb := exec(t, `{"question": "well?", "options": ["a", "b"]}`, "ask", "-input", "-")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errb)
	}
	if h.input["question"] != "well?" {
		t.Fatalf("the host saw %v", h.input)
	}
}

func TestABooleanMayBeSetFalse(t *testing.T) {
	h := &host{}
	serve(t, h)
	if code, _, errb := exec(t, "", "push", "-delete=false"); code != exitOK {
		t.Fatalf("exit %d: %s", code, errb)
	}
	if h.input["delete"] != false {
		t.Fatalf("the host saw %v", h.input)
	}
}

func TestARefusalIsStdoutAndExit3(t *testing.T) {
	serve(t, &host{out: "refused: viff/main is not in the allowance", refuse: true})
	code, out, errb := exec(t, "", "push", "-branch", "main")
	if code != exitRefused || errb != "" {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if !strings.HasPrefix(out, "refused:") {
		t.Fatalf("stdout %q", out)
	}
}

func TestAFailureIsStderrAndExit1(t *testing.T) {
	serve(t, &host{err: errors.New("the remote is unreachable")})
	code, out, errb := exec(t, "", "push")
	if code != exitFailed || out != "" {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
	if !strings.Contains(errb, "push: the remote is unreachable") {
		t.Fatalf("stderr %q", errb)
	}
}

func TestTheCommandLineIsCheckedBeforeTheCall(t *testing.T) {
	h := &host{}
	serve(t, h)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown tool", []string{"deploy"}, `no tool "deploy" here; the tools are: ask, fetch, push, shape`},
		{"missing required", []string{"ask", "-options", "yes"}, "ask requires question"},
		{"empty required", []string{"ask", "-question", " "}, "ask requires question"},
		{"outside the enum", []string{"push", "-repo", "other"}, `repo: "other" is not one of viff, docs`},
		{"outside the enum, by document", []string{"push", "-input", "{repo: other}"}, `repo: "other" is not one of viff, docs`},
		{"unknown key, by document", []string{"push", "-input", "{force: true}"}, `push has no parameter "force" (it has: branch, delete, repo)`},
		{"wrong type, by document", []string{"push", "-input", "{branch: 7}"}, "branch: want a string, not a number"},
		{"a boolean's value as an argument", []string{"push", "-delete", "false"}, `unexpected argument "false": a tool's parameters are flags, -<name> <value>; a boolean is -<name> or -<name>=false`},
		{"not an integer", []string{"shape", "-count", "many"}, `"many" is not an integer`},
		{"unknown flag", []string{"push", "-force"}, "flag provided but not defined: -force"},
		{"bare argument", []string{"push", "main"}, `unexpected argument "main": a tool's parameters are flags`},
		{"bare argument, no parameters", []string{"fetch", "now"}, `unexpected argument "now": fetch takes no parameters`},
		{"bad document", []string{"push", "-input", "{"}, "-input:"},
	}
	for _, c := range cases {
		h.tool = ""
		code, _, errb := exec(t, "", c.args...)
		if code != exitUsage {
			t.Errorf("%s: exit %d, stderr %q", c.name, code, errb)
		}
		if !strings.Contains(errb, c.want) {
			t.Errorf("%s: stderr %q lacks %q", c.name, errb, c.want)
		}
		if h.tool != "" {
			t.Errorf("%s: the call crossed", c.name)
		}
	}
}

func TestHelpOnAToolIsItsParameters(t *testing.T) {
	serve(t, &host{})
	code, _, errb := exec(t, "", "ask", "-help")
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"usage: viff-call-tool ask", "wait for their reply", "-question <string>  (required)", "-input <document>"} {
		if !strings.Contains(errb, want) {
			t.Errorf("help lacks %q:\n%s", want, errb)
		}
	}
}

func TestHelpAloneIsNotAnError(t *testing.T) {
	t.Setenv(calltool.Env, "")
	code, _, errb := exec(t, "", "-help")
	if code != exitOK || !strings.Contains(errb, "usage: viff-call-tool") {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
}

func TestNoSocketSaysWhereOneComesFrom(t *testing.T) {
	t.Setenv(calltool.Env, "")
	code, _, errb := exec(t, "", "push")
	if code != exitUsage || !strings.Contains(errb, "$VIFF_TOOLS is unset") {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
}

func TestASocketNobodyAnswersFails(t *testing.T) {
	t.Setenv(calltool.Env, filepath.Join(t.TempDir(), "nobody.sock"))
	code, _, errb := exec(t, "", "push")
	if code != exitFailed || !strings.Contains(errb, "reaching the task's tools") {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
}

// A server that offers a tool with a parameter named `input` anyway is told no, in words: the
// flag would be defined twice, and that ends a program with a stack trace.
func TestAParameterNamedInputIsRefusedNotAPanic(t *testing.T) {
	odd := calltool.Tool{Name: "odd", Params: map[string]any{"input": map[string]any{"type": "string"}}}
	var errb bytes.Buffer
	_, err := parseInput(odd, []string{"-help"}, strings.NewReader(""), &errb)
	if err == nil || !strings.Contains(err.Error(), `odd has a parameter named "input"`) {
		t.Fatalf("err = %v", err)
	}
}
