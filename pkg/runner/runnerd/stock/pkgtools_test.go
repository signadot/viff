package stock

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd/delivery"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// echoInstaller answers installs without a cellar: the version asked or 1.0, the name asked or
// the package's own.
type echoInstaller struct{}

func (echoInstaller) Install(_ context.Context, _, pkg, version, as string) (delivery.Installed, error) {
	if version == "" {
		version = "1.0"
	}
	if as == "" {
		as = pkg
	}
	return delivery.Installed{Version: version, As: as}, nil
}

// pkgChannel is a task's channel to a runner that installs with a fake, under an open pkg
// ceiling, with the autonomy and the environment of l.
func pkgChannel(t *testing.T, l *runner.Launch) *delivery.Client {
	t.Helper()
	anyName := systems.Ceiling{{Verbs: map[string][]systems.Shape{"install": {{"as": ir.Null()}}}}}
	s := &delivery.Server{Log: io.Discard, Ceilings: map[string]systems.Ceiling{"pkg": anyName},
		Services: delivery.Services{Installer: echoInstaller{}}}
	near, far, err := delivery.SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		s.Serve(delivery.Task{ID: l.ID, Launch: l, Inst: "/state/cellar/.tasks/t1"}, near)
	}()
	conn, err := net.FileConn(far)
	far.Close()
	if err != nil {
		t.Fatal(err)
	}
	cl := delivery.NewClient(conn)
	t.Cleanup(func() { cl.Close(); <-served })
	return cl
}

// THE PKG TOOL IS ITS STEP, AND ITS USE IS WHAT THE RUNNER PERFORMS: the step it builds carries
// what the call asked and no more, a parameter the tool does not take refuses the use, a name
// that is a path is refused before the runner hears of it, and the runner's answer — what landed
// — is the tool's.
func TestThePkgToolAsksAndRelaysWhatLanded(t *testing.T) {
	env := systems.Environment{Systems: systems.Systems{"pkg": {{Verbs: map[string][]systems.Shape{"install": {{"as": ir.Null()}}}}}}}
	l := &runner.Launch{ID: "t1", Prompt: "p", Environment: env, Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}}
	cl := pkgChannel(t, l)
	tools := pkgTools(cl)
	if len(tools) != 1 || tools[0].Name != "pkg-install" || tools[0].Step != task.StepPkgInstall {
		t.Fatalf("%+v", tools)
	}
	tool := tools[0]

	st, ok := tool.Build(map[string]any{"pkg": "jq", "version": "1.6-2.1"})
	if !ok || st.Name != task.StepPkgInstall {
		t.Fatalf("%+v %v", st, ok)
	}
	if v := st.Field("version"); v == nil || v.String != "1.6-2.1" {
		t.Fatalf("the step's version: %v", v)
	}
	if st.Field("as") != nil {
		t.Fatal("an `as` the call did not give is on the step")
	}
	if _, ok := tool.Build(map[string]any{"pkg": "bin/evil"}); ok {
		t.Fatal("a path built a step")
	}

	out, isErr, err := tool.Do(t.Context(), task.ToolUse{Input: map[string]any{"pkg": "jq", "as": "jq-v2"}})
	if err != nil || isErr || out != "installed jq 1.0 as jq-v2" {
		t.Fatalf("%q %v %v", out, isErr, err)
	}
	out, isErr, err = tool.Do(t.Context(), task.ToolUse{Input: map[string]any{"pkg": "jq", "pin": "1.6"}})
	if err != nil || !isErr || !strings.Contains(out, "pin") {
		t.Fatalf("a stray parameter: %q %v %v", out, isErr, err)
	}
	out, isErr, err = tool.Do(t.Context(), task.ToolUse{Input: map[string]any{"pkg": "../evil"}})
	if err != nil || !isErr || !strings.Contains(out, "never a path") {
		t.Fatalf("a path: %q %v %v", out, isErr, err)
	}

	// A doer whose environment does not give the step never sees the tool.
	bare := systems.Environment{}
	if left := offered(tools, bare); len(left) != 0 {
		t.Fatalf("offered under an empty environment: %v", left)
	}
	if left := offered(tools, env); len(left) != 1 {
		t.Fatalf("not offered under its environment: %v", left)
	}
}
