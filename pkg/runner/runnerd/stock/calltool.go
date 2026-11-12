package stock

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/signadot/verse/pkg/runner/runnerd/calltool"
	"github.com/signadot/verse/pkg/runner/runnerd/verse"
)

// A script reaches the task's tools through `verse-call-tool`, and this is how the two meet: the
// binary is where the runner is installed, the task process serves the socket, and the script's
// environment names both.
//
// The binary is never copied into a task: under bwrap it is bound read-only at its own path, as
// the runner's own program is (SubprocessConfig.Programs), and the runner names it in the task's
// configuration, so the task process uses the one bound and nothing writable shares its inode.
// `verse-secret-exec` is bound with it where it is beside it (FindSecretExec), and found by the
// same PATH.
// The socket is in a directory of its own under the task process's temporary directory, as the
// credential file is: under bwrap that is the sandbox's own /tmp, shared with the script and
// nothing else, and no repository's tree is touched.

// CallToolBinary is the binary's name, as it is on an image.
const CallToolBinary = "verse-call-tool"

// FindCallTool is the binary a runner whose executable is self gives its scripts: beside self,
// as an image installs the two together, else on its PATH. "" when it is nowhere, which leaves a
// script the socket alone.
func FindCallTool(self string) string {
	if p := filepath.Join(filepath.Dir(self), CallToolBinary); isFile(p) {
		return p
	}
	if p, err := exec.LookPath(CallToolBinary); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	return ""
}

// SecretExecBinary is the name of the command a script runs one command with a secret by, as if
// in place (cmd/verse-secret-exec): `secret-exec` and what a caller does around each call of it.
const SecretExecBinary = "verse-secret-exec"

// FindSecretExec is that command where a script finds it: beside callTool, whose directory is
// the one put on a script's PATH. "" when it is not there, which leaves a script the tools.
func FindSecretExec(callTool string) string {
	if callTool == "" {
		return ""
	}
	if p := filepath.Join(filepath.Dir(callTool), SecretExecBinary); isFile(p) {
		return p
	}
	return ""
}

// findCallTool is FindCallTool for this process.
func findCallTool() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	return FindCallTool(self)
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// described is tools as the socket offers them.
func described(tools []verse.Tool) []calltool.Tool {
	out := make([]calltool.Tool, len(tools))
	for i, t := range tools {
		out[i] = calltool.Tool{Name: t.Name, Description: t.Description, Params: t.Params, Required: t.Required}
	}
	return out
}

// toolsEnv is what a script's environment says about its tools: where the socket is, and the
// binary's directory, for PATH, when there is a binary.
func toolsEnv(sock, bin string) []string {
	set := []string{calltool.Env + "=" + sock}
	if bin != "" {
		set = append(set, "PATH="+filepath.Dir(bin))
	}
	return set
}

// defaultPath is what a shell searches when its environment names no PATH, spelled out so a
// script whose environment passes none through still finds its commands after the binary's
// directory is put in front. Setting it widens nothing: an absolute path walks past PATH, and a
// shell with none set searches these anyway.
const defaultPath = "/usr/local/bin:/usr/bin:/bin"

// withTools lays set over env: a variable set replaces one env has, except PATH, where set's
// directory is put in front of what env had — or of the default search path, when env had none —
// so the binary is found and so is everything else.
func withTools(env, set []string) []string {
	if len(set) == 0 {
		return env
	}
	out := make([]string, 0, len(env)+len(set))
	had := map[string]bool{}
	for _, kv := range set {
		name, value, _ := strings.Cut(kv, "=")
		if name == "PATH" {
			old, ok := lookup(env, "PATH")
			if !ok || old == "" {
				old = defaultPath
			}
			kv = "PATH=" + value + string(os.PathListSeparator) + old
		}
		had[name] = true
		out = append(out, kv)
	}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !had[name] {
			out = append(out, kv)
		}
	}
	return out
}

func lookup(env []string, name string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}
