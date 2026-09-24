package task

import "os"

// DefaultShellEnv is the passthrough a task's shell gets when whoever runs it declares none:
// enough for a shell and the tools in it to find themselves, and no credential.
//
// A credential is never on it, and adding one here would be the wrong fix for a task that needs
// it — that is a declaration for the deployment to make (`shellEnv:` for `verse up`, `env:` for a
// coderunner environment), where it can be seen.
var DefaultShellEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "TERM", "TMPDIR"}

// ShellEnv is the environment a task's shell runs with: the variables named in passthrough, with
// the values this process has, and nothing else.
//
// It never returns nil. `exec.Cmd.Env == nil` means "inherit os.Environ()", so a nil here would
// hand an agent's shell the whole of the host process's environment — the model credentials the
// process was started with included — which is the opposite of an empty passthrough.
func ShellEnv(passthrough []string) []string {
	env := []string{}
	for _, name := range passthrough {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}
