package verserunner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/verserunner/agent/modeltest"
	"github.com/signadot/verse/task"
)

const token = "eyJ.token.sig"

// A SCRIPT GETS ITS LAUNCH'S CREDENTIAL AS A FILE: the variable names a file outside the task's
// workspace that holds the token, the token itself is in no variable, and the file is gone once
// the task has ended.
func TestAScriptGetsItsCredentialAsAFile(t *testing.T) {
	root := t.TempDir()
	d := New(Config{}, Secrets{}, root, io.Discard)
	p := d.Build(runner.Launch{ID: "t1", Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, Credential: token,
		Doer: runner.Doer{Script: "#!/bin/sh\necho \"$" + task.CredentialFileEnv + "\"\ncat \"$" +
			task.CredentialFileEnv + "\"; echo\nenv | grep -c " + token + " || true", With: runner.With{Network: netOpen}}}, task.NewRegistry())
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := strings.Fields(p.Result())
	if len(out) != 3 {
		t.Fatalf("output: %q", p.Result())
	}
	path, read, inEnv := out[0], out[1], out[2]
	if read != token {
		t.Errorf("the file holds %q, want the launch's token", read)
	}
	if inEnv != "0" {
		t.Errorf("the token is in the script's environment")
	}
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("the credential file %s is inside the workspace %s", path, root)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the credential file %s outlived its task", path)
		}
	}
}

// A launch with no credential gets no file, and its shell no variable.
func TestNoCredentialNoFile(t *testing.T) {
	d := New(Config{}, Secrets{}, t.TempDir(), io.Discard)
	p := d.Build(runner.Launch{ID: "t1", Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}},
		Doer: runner.Doer{Script: "echo \"[$" + task.CredentialFileEnv + "]\"", With: runner.With{Network: netOpen}}}, task.NewRegistry())
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := p.Result(); got != "[]" {
		t.Errorf("a launch with no credential set %s", got)
	}
}

// The verse agent's shell gets the credential the same way: the file its variable names.
func TestTheAgentGetsItsCredentialAsAFile(t *testing.T) {
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		if res := r.Results(); len(res) > 0 {
			out := strings.Split(strings.TrimSpace(res[0]), "\n")
			return modeltest.Say(out[len(out)-1])
		}
		return modeltest.Use("run_bash", map[string]any{"command": "cat \"$" + task.CredentialFileEnv + "\"; echo"})
	})
	d := New(Config{}, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL}, t.TempDir(), io.Discard)
	p := d.Build(runner.Launch{ID: "t1", Prompt: "read your credential", Policy: allowing("commands"),
		Credential: token, Doer: runner.Doer{Agent: AgentName, With: runner.With{Network: netOpen}}}, task.NewRegistry())
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := p.Result(); got != token {
		t.Errorf("the agent's shell read %q, want the launch's token", got)
	}
}

// A task in a process of its own gets its credential too: the launch crosses to the process, and
// the file is made there.
func TestATaskInItsOwnProcessGetsItsCredential(t *testing.T) {
	rt, _ := serve(t, Secrets{}, runnerd.Unconfined{})
	rt.Launch(runner.Launch{ID: "t1", Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, Credential: token,
		Doer: runner.Doer{Script: "#!/bin/sh\ncat \"$" + task.CredentialFileEnv + "\"", With: runner.With{Network: netOpen}}})
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		in, err := rt.(task.Lister).TaskSnapshot("t1")
		if err != nil || !in.Status.Terminal() {
			continue
		}
		if in.Status != task.Done || in.Result != token {
			t.Fatalf("task: %+v, want done with the launch's token", in)
		}
		return
	}
	t.Fatal("the task didn't end")
}

// A sub-agent's shell gets the launch's with.env, as the task's does, and not its credential,
// which is the task's alone.
func TestASubAgentGetsTheEnvAndNotTheCredential(t *testing.T) {
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		res := r.Results()
		if r.Prompt() == "look" { // the sub-agent
			if len(res) == 0 {
				return modeltest.Use("run_bash", map[string]any{"command": "echo branch=$BRANCH cred=${" + task.CredentialFileEnv + ":-none}"})
			}
			out := strings.Split(strings.TrimSpace(res[0]), "\n")
			return modeltest.Say(out[len(out)-1])
		}
		for _, m := range r.Messages { // the parent, once the sub-agent's result has folded in
			if m.Role == "user" && strings.Contains(m.Content, "branch=") {
				return modeltest.Say(m.Content)
			}
		}
		if len(res) == 0 {
			return modeltest.Use("call_agent", map[string]any{"agent": "helper", "prompt": "look"})
		}
		return modeltest.Use("yield", map[string]any{"reason": "waiting"})
	})
	r := New(Config{}, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL}, t.TempDir(), io.Discard)
	p := r.Build(runner.Launch{ID: "t1", Prompt: "delegate", Policy: allowing("commands", "agent"), Credential: token,
		Doer: runner.Doer{Agent: AgentName, With: runner.With{Network: netOpen, Env: map[string]string{"BRANCH": "main"}}}}, task.NewRegistry())
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := p.Result(); !strings.Contains(got, "branch=main cred=none") {
		t.Errorf("the sub-agent's shell saw %q, want BRANCH and no credential", got)
	}
}

// The runner never says a credential: not in its log, nor in its task's process's output, as the
// launch crosses from the runner to the process and the task runs.
func TestTheLogNeverHoldsTheCredential(t *testing.T) {
	var log lockedLog
	rt, _ := serveLogging(t, Secrets{}, runnerd.Unconfined{}, &log)
	rt.Launch(runner.Launch{ID: "t1", Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, Credential: token,
		Doer: runner.Doer{Script: "#!/bin/sh\ntest -s \"$" + task.CredentialFileEnv + "\" && echo has-it", With: runner.With{Network: netOpen}}})
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if in, err := rt.(task.Lister).TaskSnapshot("t1"); err == nil && in.Status.Terminal() {
			if in.Result != "has-it" {
				t.Fatalf("task: %+v", in)
			}
			if strings.Contains(log.String(), token) {
				t.Fatalf("the log holds the credential:\n%s", log.String())
			}
			return
		}
	}
	t.Fatal("the task didn't end")
}

// lockedLog is a log a test reads while a runner writes it.
type lockedLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
