# a system a runner's ceilings leave out is unbounded inside the run

On `verse/environment` (#33), `Supervisor.Ask` (`task/supervisor.go:260`) and `delivery.Server.refused` (`runner/runnerd/delivery/server.go:384`) hold a step to its system's ceiling only `if c, has := ceilings[system]; has`. A system the map leaves out, and every system when the map is nil, is then unbounded after the autonomy. The agreed model (Scott and Daniel) is that a system with no ceiling is inaccessible.

Not reachable through `verserunner` today: its `taskCeilings` holds every system whose steps reach the supervisor (a nil ceiling being closed), and `runnerd.CheckOffer` refuses an environment system the offer doesn't hold. It is reachable by any builder that passes a partial or nil map (the parameter is open: `codex.Task`, `newScriptTask`, `agent.BuildConfig.Ceilings`), and the docs' example runner (`docs/reference/examples/runner/main.go`) sets none.

Fix: a system the ceilings name no ceiling for is closed, a nil map closing every system (`systems.Beyond`, read by the supervisor and the delivery server). A runner, test or example that means a system to be open says so with an open ceiling.
