# a system a runner's ceilings leave out is unbounded inside the run

On `verse/environment` (#33), `Supervisor.Ask` (`task/supervisor.go:260`) and `delivery.Server.refused` (`runner/runnerd/delivery/server.go:384`) hold a step to its system's ceiling only `if c, has := ceilings[system]; has`. A system the map leaves out is then unbounded after the autonomy, while the model is that a system with no ceiling is closed.

Not reachable through `verserunner` today: its `taskCeilings` holds every system whose steps reach the supervisor (a nil ceiling being closed), and `runnerd.CheckOffer` refuses an environment system the offer doesn't hold. It is reachable by any builder that passes a partial map (the parameter is open: `codex.Task`, `newScriptTask`, `agent.BuildConfig.Ceilings`).

Fix: where a runner states ceilings (a non-nil map), a system it leaves out is closed. A nil map, a handler that states none (one with no offers, in a test or an example), stays unbounded.