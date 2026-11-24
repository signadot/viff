package calltool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// callToolBin is main's viff-call-tool, built once for the package: the binary a script runs,
// speaking to this copy of the server. "" when there is no go to build it with.
var callToolBin string

func TestMain(m *testing.M) {
	os.Exit(func() int {
		if _, err := exec.LookPath("go"); err == nil {
			dir, err := os.MkdirTemp("", "ctbin")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			defer os.RemoveAll(dir)
			bin := filepath.Join(dir, "viff-call-tool")
			out, err := exec.Command("go", "build", "-o", bin, "go.viff.dev/viff/cmd/viff-call-tool").CombinedOutput()
			if err != nil {
				fmt.Fprintf(os.Stderr, "building viff-call-tool: %v\n%s", err, out)
				return 1
			}
			callToolBin = bin
		}
		return m.Run()
	}())
}

// callTool runs the binary against path with args, and is what it printed and its exit status.
func callTool(t *testing.T, path string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	if callToolBin == "" {
		t.Skip("no go on PATH to build viff-call-tool with")
	}
	cmd := exec.Command(callToolBin, args...)
	cmd.Env = append(os.Environ(), Env+"="+path)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return o.String(), e.String(), code
}

// MAIN'S BINARY SPEAKS TO THIS SERVER: the list, a call with its flags, a refusal, a failure,
// and a call the schema refuses before it crosses. It is what holds the two copies of the
// protocol together until the binary imports this one.
func TestMainsBinarySpeaksToThisServer(t *testing.T) {
	s, _ := start(t, func(_ context.Context, tool string, in map[string]any) (string, bool, error) {
		switch q, _ := in["question"].(string); {
		case tool == "push":
			return fmt.Sprintf("pushed %v of %v", in["branch"], in["repo"]), false, nil
		case q == "refuse":
			return "a gate said no", true, nil
		case q == "fail":
			return "", false, errors.New("the resolver is gone")
		}
		return "yes", false, nil
	})
	path := s.Path()

	if out, _, code := callTool(t, path); code != 0 || !strings.Contains(out, "push") || !strings.Contains(out, "ask") {
		t.Errorf("the list: exit %d\n%s", code, out)
	}
	if out, errs, code := callTool(t, path, "push", "-repo", "docs", "-branch", "viff/x"); code != 0 || !strings.Contains(out, "pushed viff/x of docs") {
		t.Errorf("a call: exit %d, %q %q", code, out, errs)
	}
	if out, _, code := callTool(t, path, "ask", "-question", "refuse"); code != 3 || !strings.Contains(out, "a gate said no") {
		t.Errorf("a refusal: exit %d, %q", code, out)
	}
	if _, errs, code := callTool(t, path, "ask", "-question", "fail"); code != 1 || !strings.Contains(errs, "the resolver is gone") {
		t.Errorf("a failure: exit %d, %q", code, errs)
	}
	if _, errs, code := callTool(t, path, "push", "-repo", "nope"); code == 0 || !strings.Contains(errs, "not one of") {
		t.Errorf("a value outside the enum: exit %d, %q", code, errs)
	}
}
