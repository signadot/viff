package runner

// SpawnResponse is the answer to `POST /tasks`: the id the task runs under. `201` started it,
// `200` found it already running, and `202` accepted it and is starting it: a runner that
// readies a task's workspace first answers before that work is done, and the task runs to
// every reader meanwhile.
//
//tony:schemagen=verse-runner-spawned,notag
type SpawnResponse struct {
	ID string `tony:"field=id"`
}
