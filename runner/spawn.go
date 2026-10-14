package runner

// SpawnResponse is the answer to `POST /tasks`: the id the task runs under. `201` started it,
// `200` found it already running.
//
//tony:schemagen=verse-runner-spawned,notag
type SpawnResponse struct {
	ID string `tony:"field=id"`
}
