package delivery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
)

// fakeInstaller answers installs without a cellar: the version asked or the candidate, the name
// asked or the package's own, and it records where each install landed.
type fakeInstaller struct {
	calls    []string
	provided bool
	err      error
}

func (f *fakeInstaller) Install(_ context.Context, inst, pkg, version, as string) (Installed, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s %s=%s as=%s", inst, pkg, version, as))
	if f.err != nil {
		return Installed{}, f.err
	}
	if f.provided {
		return Installed{Version: "1.0-image", Provided: true}, nil
	}
	if version == "" {
		version = "9.9-candidate"
	}
	if as == "" {
		return Installed{Version: version, Commands: []string{pkg}}, nil
	}
	return Installed{Version: version, As: as}, nil
}

// pkgLaunch is a launch whose environment gives one `pkg` block with the install shapes given,
// under policy.
func pkgLaunch(shapes []systems.Shape, policy task.Autonomy) *runner.Launch {
	return &runner.Launch{ID: "t1", Prompt: "p", Autonomy: policy, Environment: systems.Environment{Systems: systems.Systems{
		"pkg": {{Verbs: map[string][]systems.Shape{"install": shapes}}},
	}}}
}

// pkgRunner is a server that installs with a fake, under a ceiling for pkg that gives any install
// under any name, and a task under l whose install root is inst.
func pkgRunner(t *testing.T, l *runner.Launch, inst string) (*Server, Task, *fakeInstaller) {
	t.Helper()
	f := &fakeInstaller{}
	anyName := systems.Ceiling{{Verbs: map[string][]systems.Shape{"install": {{"as": pattern(t, "null")}}}}}
	s := &Server{Log: io.Discard, Ceilings: map[string]systems.Ceiling{"pkg": anyName}, Services: Services{Installer: f}}
	return s, Task{ID: "t1", Launch: l, Inst: inst}, f
}

// install is a `pkg.install` request, a field given nothing left out.
func installReq(pkg, version, as string) Request {
	fields := map[string]string{"pkg": pkg}
	if version != "" {
		fields["version"] = version
	}
	if as != "" {
		fields["as"] = as
	}
	return Request{Op: OpPkgInstall, Fields: fields}
}

var allowAll = task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}

// A `pkg.install` IS READ INTO ONE STEP, DECIDED, AND PERFORMED INTO THIS TASK'S ROOT ALONE: the
// step carries what the call asked and no more, so a shape that pins the version refuses a call
// that names none, a pattern on the package admits what it matches, a rename is admitted only
// where a shape names `as`, and a name that is a path is refused before any row hears of it. What the act landed — the resolved version, the command's
// name — is the answer's to say.
func TestAPkgInstallIsReadIntoOneStepAndLandsInTheTasksRoot(t *testing.T) {
	open := []systems.Shape{{}}
	renames := []systems.Shape{{"as": pattern(t, "null")}}
	for _, c := range []struct {
		name   string
		shapes []systems.Shape
		req    Request
		says   string // "" is performed OK
		out    string
	}{
		{"an open shape installs the candidate", open, installReq("jq", "", ""), "", "installed jq 9.9-candidate: jq, on PATH"},
		{"a pinned version and a chosen name ride through", renames, installReq("jq", "1.6-2.1", "jq-v1"), "", "installed jq 1.6-2.1 as jq-v1"},
		{"a shape that says nothing of `as` refuses a rename", open, installReq("make", "", "npm"), "outside", ""},
		{"a shape that pins the version refuses a call that names none",
			[]systems.Shape{{"pkg": pattern(t, "jq"), "version": pattern(t, `"1.6-2.1"`)}}, installReq("jq", "", ""), "outside", ""},
		{"and admits the call that names it",
			[]systems.Shape{{"pkg": pattern(t, "jq"), "version": pattern(t, `"1.6-2.1"`)}}, installReq("jq", "1.6-2.1", ""), "", "installed jq 1.6-2.1: jq, on PATH"},
		{"a pattern on the package admits what it matches", []systems.Shape{{"pkg": pattern(t, `!glob "lib*"`)}}, installReq("libonig5", "", ""), "", "installed libonig5 9.9-candidate: libonig5, on PATH"},
		{"and refuses the rest", []systems.Shape{{"pkg": pattern(t, `!glob "lib*"`)}}, installReq("jq", "", ""), "outside", ""},
		{"a name that is a path is refused unread", renames, installReq("jq", "", "bin/evil"), "a name, never a path", ""},
		{"a package must be named", open, installReq("", "", ""), "pkg names the package", ""},
	} {
		s, tk, f := pkgRunner(t, pkgLaunch(c.shapes, allowAll), "/state/cellar/.tasks/t1")
		rep := s.answer(tk, frame(t, c.req))
		if c.says == "" {
			if !rep.OK || rep.Out != c.out {
				t.Errorf("%s: %+v, want OK %q", c.name, rep, c.out)
			}
			continue
		}
		if rep.OK || !strings.Contains(rep.Refused+rep.Error, c.says) {
			t.Errorf("%s: %+v, want a refusal saying %q", c.name, rep, c.says)
		}
		if len(f.calls) != 0 {
			t.Errorf("%s: the installer was reached: %v", c.name, f.calls)
		}
	}

	// An environment without pkg refuses every install, whatever the autonomy allows.
	s, tk, f := pkgRunner(t, &runner.Launch{ID: "t1", Prompt: "p", Autonomy: allowAll}, "/state/cellar/.tasks/t1")
	if rep := s.answer(tk, frame(t, installReq("jq", "", ""))); rep.OK || rep.Refused == "" {
		t.Errorf("an empty environment: %+v", rep)
	}
	if len(f.calls) != 0 {
		t.Errorf("the installer was reached under an empty environment: %v", f.calls)
	}
}

// THE ACT LANDS IN THE TASK'S OWN ROOT, AND SAYS WHAT IT COULD NOT: a task with no install root
// and a runner with no installer answer why, a failed install carries the wrapper's words, and a
// package the image provides is said to be on PATH already, nothing laid.
func TestAnInstallSaysWhereItLandedOrWhyItCouldNot(t *testing.T) {
	l := pkgLaunch([]systems.Shape{{"as": pattern(t, "null")}}, allowAll)
	s, tk, f := pkgRunner(t, l, "/state/cellar/.tasks/t1")
	if rep := s.answer(tk, frame(t, installReq("jq", "", "jq-v2"))); !rep.OK || rep.Out != "installed jq 9.9-candidate as jq-v2" {
		t.Fatalf("%+v", rep)
	}
	if len(f.calls) != 1 || f.calls[0] != "/state/cellar/.tasks/t1 jq= as=jq-v2" {
		t.Fatalf("the installer was asked: %v", f.calls)
	}

	f.provided = true
	if rep := s.answer(tk, frame(t, installReq("git", "", ""))); !rep.OK || rep.Out != "the image provides git 1.0-image, on PATH already" {
		t.Errorf("provided: %+v", rep)
	}

	f.provided, f.err = false, errors.New("the index carries no SHA256 for jq 9.9")
	if rep := s.answer(tk, frame(t, installReq("jq", "", ""))); rep.OK || !strings.Contains(rep.Error, "not installed: the index carries no SHA256") {
		t.Errorf("a failed install: %+v", rep)
	}

	rootless, tk2, _ := pkgRunner(t, l, "")
	if rep := rootless.answer(tk2, frame(t, installReq("jq", "", ""))); rep.OK || !strings.Contains(rep.Error, "no install root") {
		t.Errorf("no install root: %+v", rep)
	}

	none := &Server{Log: io.Discard, Ceilings: performs("pkg")}
	if rep := none.answer(Task{ID: "t1", Launch: l, Inst: "/i"}, frame(t, installReq("jq", "", ""))); rep.OK || !strings.Contains(rep.Error, "installs no packages") {
		t.Errorf("no installer: %+v", rep)
	}
}
