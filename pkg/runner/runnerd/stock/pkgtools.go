package stock

import (
	"context"

	"github.com/signadot/verse/pkg/runner/runnerd/delivery"
	"github.com/signadot/verse/pkg/runner/runnerd/verse"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// pkgInstallTool is the pkg system's tool, named for its step.
var pkgInstallTool = systems.Tool("pkg", "install")

// pkgParams are `pkg-install`'s arguments: the package, the version where the call pins one, and
// the command's name where it overrides the package's own.
var pkgParams = map[string]any{
	"pkg":     map[string]any{"type": "string"},
	"version": map[string]any{"type": "string"},
	"as":      map[string]any{"type": "string"},
}

// pkgTools is the pkg system's tool: an install the runner performs into the task's read-only
// prefix, asked for over the channel. Offered where the environment gives the step (offered).
func pkgTools(cl *delivery.Client) []verse.Tool {
	read := func(in map[string]any) (delivery.Install, string) {
		return delivery.ReadInstall(str(in, "pkg"), str(in, "version"), str(in, "as"))
	}
	return []verse.Tool{{
		Name: pkgInstallTool,
		Description: "Install one package into your read-only prefix, onto your PATH. The runner resolves `pkg` against " +
			"the signed index of the repositories its operator trusts, fetches by the index's hash, extracts it with none " +
			"of the package's own scripts run, and lays the tool where you read it: you never fetch or run install logic " +
			"yourself, and nothing outside your own prefix changes. `version` pins one as the index spells it; absent is " +
			"the index's candidate, and the answer says exactly what landed. `as` renames the package's one command " +
			"(`jq-v1`), so two versions sit side by side; absent, every command the package ships lands under its own " +
			"name. A rename is refused unless your environment's shape names `as`. A package the image already " +
			"provides is answered as on PATH already.",
		Params: pkgParams, Required: []string{"pkg"},
		Step: task.StepPkgInstall,
		Build: func(in map[string]any) (task.Step, bool) {
			ins, why := read(in)
			if why != "" {
				return task.Step{}, false
			}
			return ins.Step(), true
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			if why, stray := untaken(pkgInstallTool, tu.Input, pkgParams); stray {
				return why, true, nil
			}
			ins, why := read(tu.Input)
			if why != "" {
				return "not installed: " + why, true, nil
			}
			fields := map[string]string{"pkg": ins.Pkg}
			if ins.Version != "" {
				fields["version"] = ins.Version
			}
			if ins.As != "" {
				fields["as"] = ins.As
			}
			return relay(cl.Ask(delivery.Request{Op: delivery.OpPkgInstall, Fields: fields, Decision: tu.Decision}))
		},
	}}
}
