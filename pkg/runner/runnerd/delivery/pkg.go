package delivery

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.viff.dev/viff/pkg/runner/runnerd/cellar"
	"go.viff.dev/viff/pkg/task"
)

// THE PKG SYSTEM IS ONE PACKAGE INSTALLED BY THE RUNNER INTO THE TASK'S READ-ONLY PREFIX, which
// the task reads through the one bind its sandbox made at its start (InstPath): the runner
// resolves the name against the signed index of the repositories its operator trusts, fetches by
// the index's hash, extracts with none of the package's own scripts run, and lays the tool into
// the task's install root. The task only asks; the environment says which packages, the autonomy
// decides each install, and the ceiling bounds both.
//
// The request: `pkg` is the package's name as the index spells it, `version` pins one (absent is
// the index's candidate, and the answer says what landed), and `as` names the command on the
// task's PATH (absent is the package's own), so two versions sit side by side. The step a row
// decides carries what the task asked and no more: a version the task did not name is the act's
// to resolve, bound by the ceiling's shapes as every field is. A rename is admitted only where a
// shape names `as`: one that says nothing of it means none.
const OpPkgInstall = "pkg.install"

// How long an install has: the resolve, the fetches and the layout; and what the op has past it.
const (
	InstallTimeout = 10 * time.Minute
	installGrace   = time.Minute
)

// Installed is what an install landed: the version; the one renamed command where the call said
// `as`, or the commands landed under their own names where it did not; and whether the image
// provides the package already — then nothing was laid, and the command is the image's own.
type Installed struct {
	Version string
	// As is the one command the call renamed, "" where nothing was: never a list, since a
	// consumer reads it as a name on the task's PATH.
	As string
	// Commands are the commands landed under their own names, where no `as` renamed one.
	Commands []string
	Provided bool
}

// Installer installs one package into a task's install root: the resolve, the cellar's fill, and
// the materialising are its; version and as may be empty, the index's candidate and the
// package's own command. The runner's stock wires one over its cellar and its wrapper, which
// this package cannot name.
type Installer interface {
	Install(ctx context.Context, inst, pkg, version, as string) (Installed, error)
}

// Install is a `pkg.install` as read from a call (ReadInstall): the package, the version where
// the call pins one, and the command's name where it overrides the package's own.
type Install struct {
	Pkg, Version, As string
}

// ReadInstall reads a `pkg.install` from what a call gives, and says why it is refused before
// any row decides it, "" where it is not: a package named, and each of the three a name, never a
// path. The runner reads it as the request arrives (pkgInstall), and the task's tool before it
// sends the call, so a row decides one step (Install.Step) on both sides.
func ReadInstall(pkg, version, as string) (Install, string) {
	if pkg == "" {
		return Install{}, "pkg names the package to install"
	}
	// The one name rule is the cellar's (CheckName), whose path segments the names become: a
	// second copy here would drift from the one the layout's safety rests on.
	for what, v := range map[string]string{"pkg": pkg, "version": version, "as": as} {
		if v == "" {
			continue
		}
		if err := cellar.CheckName(what, v); err != nil {
			return Install{}, err.Error()
		}
	}
	return Install{Pkg: pkg, Version: version, As: as}, ""
}

// Step is the step the install is, as the autonomy and the ceiling see it: what the call asked,
// a field it did not give absent. A shape that pins the version then refuses it; an absent `as`
// is no rename, which a shape that says nothing of `as` means too (the system's document's
// default).
func (i Install) Step() task.Step {
	fields := map[string]string{"pkg": i.Pkg}
	if i.Version != "" {
		fields["version"] = i.Version
	}
	if i.As != "" {
		fields["as"] = i.As
	}
	return task.NewStep(task.StepPkgInstall, fields)
}

// pkgInstall answers a `pkg.install`: the request read into the one step, decided by the
// environment, the autonomy and the ceiling, and then the install, into this task's root alone.
func (s *Server) pkgInstall(ctx context.Context, tk Task, req Request) (asked, Reply) {
	ins, why := ReadInstall(req.Field("pkg"), req.Field("version"), req.Field("as"))
	if why != "" {
		return before(Reply{Refused: why})
	}
	return asking(ins.Step(), func() Reply {
		if s.Services.Installer == nil {
			return Reply{Error: "this runner installs no packages"}
		}
		if tk.Inst == "" {
			return Reply{Error: "this task has no install root, so nothing installed could reach it"}
		}
		got, err := s.Services.Installer.Install(ctx, tk.Inst, ins.Pkg, ins.Version, ins.As)
		if err != nil {
			return Reply{Error: "not installed: " + err.Error()}
		}
		var out string
		switch {
		case got.Provided:
			out = fmt.Sprintf("the image provides %s %s, on PATH already", ins.Pkg, got.Version)
		case got.As != "":
			out = fmt.Sprintf("installed %s %s as %s", ins.Pkg, got.Version, got.As)
		default:
			out = fmt.Sprintf("installed %s %s: %s, on PATH", ins.Pkg, got.Version, strings.Join(got.Commands, ", "))
		}
		fmt.Fprintf(s.Log, "delivery: %s: %s\n", tk.ID, out)
		return Reply{OK: true, Out: out}
	})
}
