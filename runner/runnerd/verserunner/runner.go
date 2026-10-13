package verserunner

import (
	"fmt"
	"io"
	"os"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/task"
)

// Secrets is what the runner reads from its environment. A task's process doesn't have the
// runner's environment, so they are sent to it on its stdin.
type Secrets struct {
	PortkeyAPIKey  string `tony:"field=portkeyApiKey,omitzero"`
	PortkeyBaseURL string `tony:"field=portkeyBaseUrl,omitzero"`
}

// SecretsFromEnv reads the runner's secrets from its environment.
func SecretsFromEnv() Secrets {
	return Secrets{
		PortkeyAPIKey:  os.Getenv(EnvPortkeyAPIKey),
		PortkeyBaseURL: os.Getenv(EnvPortkeyBaseURL),
	}
}

// Standalone is the verse runner as a standalone runner, with a runnerd.SubprocessHandler: each
// task in a process of its own,
// started with command (which calls ServeTask), confined by sb. The process gets the runner's
// configuration and secrets on its stdin, and an environment with only what
// task.DefaultShellEnv names.
func Standalone(c Config, s Secrets, sb runnerd.Sandbox, command []string, work, control string,
	log io.Writer) (*runnerd.SubprocessHandler, error) {
	blob, err := codec.Marshal(taskConfig{Config: c, Secrets: s}, codec.Tony)
	if err != nil {
		return nil, err
	}
	h := runnerd.NewSubprocessHandler(runnerd.SubprocessConfig{
		Command: command, Env: task.ShellEnv(task.DefaultShellEnv), Config: blob,
		Sandbox: sb, Work: work, Control: control, Log: log,
	})
	h.Describe(c.Describe(s))
	return h, nil
}

// taskConfig is what a task's process reads on its stdin.
type taskConfig struct {
	Config  Config  `tony:"field=config"`
	Secrets Secrets `tony:"field=secrets"`
}

// ServeTask is a task's process: it reads the runner's configuration and secrets from stdin,
// and serves the one task a SubprocessHandler hands it, in the working directory.
func ServeTask(stdin io.Reader, log io.Writer) error {
	blob, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("reading the task's configuration: %w", err)
	}
	var tc taskConfig
	if err := codec.Unmarshal(blob, &tc, codec.Tony); err != nil {
		return fmt.Errorf("the task's configuration does not parse: %w", err)
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	return runnerd.ServeTask(New(tc.Config, tc.Secrets, dir, log).Build)
}

// Runner builds the verse runner's tasks, with the driver their launch names.
type Runner struct {
	cfg     Config
	drivers Drivers
	log     io.Writer
}

// New is the verse runner under c and s, running its tasks in dir. A task's shell sees only the
// variables task.DefaultShellEnv names, never the runner's secrets.
func New(c Config, s Secrets, dir string, log io.Writer) *Runner {
	r := &Runner{cfg: c, log: log}
	r.drivers = Drivers{
		Root:          dir,
		Env:           task.DefaultShellEnv,
		Log:           log,
		Default:       "scripted",
		LLMBaseURL:    s.PortkeyBaseURL,
		LLMKey:        s.PortkeyAPIKey,
		LLMModel:      c.Models.Model,
		LLMVirtualKey: c.Models.Portkey.VirtualKey,
		VirtualKeyOr:  r.virtualKey,
		NoKey:         "set " + EnvPortkeyAPIKey,
	}
	if s.PortkeyAPIKey != "" {
		r.drivers.Default = "llm"
	}
	return r
}

// Build is a runnerd builder.
func (r *Runner) Build(l task.Launch, reg *task.Registry) *task.Task {
	return r.drivers.Build(l, reg)
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
