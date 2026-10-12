package verserunner

import (
	"fmt"
	"io"
	"os"

	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/task"
)

// Runner builds the verse runner's tasks: each in its own scratch directory, removed when the
// task ends, with the driver its launch names.
type Runner struct {
	cfg     Config
	drivers Drivers
	log     io.Writer
	// work is where the tasks' directories are made.
	work string
}

// New is the verse runner under c, making its tasks' directories in work. A task's shell sees
// only the variables task.DefaultShellEnv names, never the runner's secrets.
func New(c Config, work string, log io.Writer) *Runner {
	r := &Runner{cfg: c, work: work, log: log}
	r.drivers = Drivers{
		Env:           task.DefaultShellEnv,
		Log:           log,
		Default:       "scripted",
		LLMBaseURL:    os.Getenv(EnvPortkeyBaseURL),
		LLMKey:        c.APIKey(),
		LLMModel:      c.Models.Model,
		LLMVirtualKey: c.Models.Portkey.VirtualKey,
		VirtualKeyOr:  r.virtualKey,
		NoKey:         "set " + EnvPortkeyAPIKey,
	}
	if c.APIKey() != "" {
		r.drivers.Default = "llm"
	}
	return r
}

// Daemon is the runner's host: its tasks, built by Build, and what it offers. Serve its Handler,
// or embed it.
func (r *Runner) Daemon() *runnerd.Daemon {
	d := runnerd.New(r.Build)
	d.Describe(r.cfg.Describe())
	return d
}

// Build is a runnerd builder.
func (r *Runner) Build(l task.Launch, reg *task.Registry) *task.Task {
	dir, err := os.MkdirTemp(r.work, "task-")
	if err != nil {
		return task.NewFailed(l.ID, reg, fmt.Sprintf("the task's directory could not be made: %v", err))
	}
	d := r.drivers
	d.Root = dir
	t := d.Build(l, reg)
	frames, stop := t.Subscribe()
	go func() {
		for range frames { // the stream ends when the task does
		}
		stop()
		os.RemoveAll(dir)
	}()
	return t
}

// virtualKey is the Portkey virtual key a run uses: the one its spawn picked, when the runner
// accepts the annotation, else the configured one.
//
// A key picked under a runner that doesn't accept the annotation is said in the log, since
// nothing refuses such a spawn yet. The key itself isn't logged.
func (r *Runner) virtualKey(picked string) string {
	if picked == "" {
		return r.cfg.Models.Portkey.VirtualKey
	}
	if r.cfg.accepts(AnnotationVirtualKey) {
		return picked
	}
	fmt.Fprintf(r.log, "a spawn picked a Portkey virtual key, but the runner doesn't accept %s: "+
		"the configured one is used\n", AnnotationVirtualKey)
	return r.cfg.Models.Portkey.VirtualKey
}
