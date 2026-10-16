package verserunner

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/signadot/verse/runner/runnerd/calltool"
	"github.com/signadot/verse/runner/runnerd/verserunner/agent"
)

// A program in a task's workspace reaches the task's tools through `verse-call-tool`, and this
// is how the two meet: the runner puts the binary in the workspace, the task process serves the
// socket there, and the program's environment names both.
//
//	<workspace>/.verse/bin/verse-call-tool   the binary, copied in by the runner (Prepare)
//	<workspace>/.verse/tools.sock            the socket the task process serves (buildCommandTask)
//
// Beside the checkouts rather than inside one, so no repository's tree is touched. The sandbox
// binds the workspace read-write at its own path, so the program sees both where this side put
// them.

// CallToolBinary is the binary's name, as it is on an image and in a workspace.
const CallToolBinary = "verse-call-tool"

// verseDir is the workspace's own directory, beside the checkouts.
const verseDir = ".verse"

func binDir(root string) string { return filepath.Join(root, verseDir, "bin") }

// sunPathMax is what a unix socket's path has to fit in, on the stricter platform (macOS: 104
// bytes, with the terminator).
const sunPathMax = 100

// socketFor is where the task process serves the socket for a workspace: in the workspace,
// unless the path is too long for a socket, in which case under the process's temp dir — which
// under bwrap is the sandbox's own /tmp, shared with the program and nothing else.
func socketFor(root string) (string, error) {
	p := filepath.Join(root, verseDir, "tools.sock")
	if len(p) < sunPathMax {
		return p, nil
	}
	f, err := os.CreateTemp("", "vt-*.sock")
	if err != nil {
		return "", fmt.Errorf("a path for the tools socket: %w", err)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return name, nil
}

// FindCallTool is where the runner finds the binary to give its tasks: beside its own
// executable, as an image installs the two together, else on its PATH. "" when it is nowhere,
// which is a runner whose programs have no tools and are told so in the log.
func FindCallTool(self string) string {
	if p := filepath.Join(filepath.Dir(self), CallToolBinary); isFile(p) {
		return p
	}
	if p, err := exec.LookPath(CallToolBinary); err == nil {
		return p
	}
	return ""
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// installCallTool copies the binary into a workspace. A COPY, and neither kind of link: a
// symlink to a path outside the system directories points at nothing under bwrap, and a hard
// link shares the inode, so a program that can write its workspace could rewrite the runner's
// own binary through it, for every task after — the sandbox's read-only /usr guards the other
// path, not the file.
func installCallTool(src, root string) error {
	dir := binDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, CallToolBinary)
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// toolsEnv is what a program's environment says about its tools: where the socket is, and the
// binary's directory, for PATH.
func toolsEnv(root, sock string) []string {
	return []string{calltool.Env + "=" + sock, "PATH=" + binDir(root)}
}

// defaultPath is what a shell searches when its environment names no PATH, spelled out so a
// program whose environment passes none through still finds its commands after the binary's
// directory is put in front. Setting it widens nothing: PATH is a convention and not a floor
// (buildEnv), since an absolute path walks past it, and a shell with none set searches these
// anyway.
const defaultPath = "/usr/local/bin:/usr/bin:/bin"

// withTools lays set over env: a variable set replaces one env passes through, except PATH,
// where set's directory is put in front of what env had — or of the default search path, when
// env had none — so the binary is found and so is everything else.
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

// callToolBrief tells a program how it reaches the task's tools. It rides the prompt beside the
// workspace brief, for the same reason: the arrangement is the runner's, and a program that was
// not told it has a shell and no idea the socket is there. "" when nothing is offered. Without
// the binary in the workspace — a runner that found none to install — the program is told the
// socket instead, and not a command it has not got.
func callToolBrief(tools []agent.Tool, installed bool) string {
	if len(tools) == 0 {
		return ""
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, "`"+t.Name+"`")
	}
	var b strings.Builder
	if installed {
		b.WriteString("This task's tools are called from your shell with `" + CallToolBinary + "`, which is on your " +
			"PATH: run it with no arguments to list them with their parameters, and `" + CallToolBinary +
			" <tool> -<param> <value>...` to call one. ")
	} else {
		b.WriteString("This task's tools are served on the unix socket $" + calltool.Env + " names, as HTTP: " +
			"GET /tools lists them with their parameters, POST /call with a body {tool, input} calls one; " +
			"the result is {out, refused}. ")
	}
	b.WriteString("The tools here: " + strings.Join(names, ", ") + ". ")
	for _, t := range tools {
		if t.Name == agent.AskToolName {
			b.WriteString("`ask` says something to the person this task runs for and waits for their " +
				"reply, which is what it prints; use it when you need a decision or information only " +
				"they have. ")
			break
		}
	}
	if installed {
		b.WriteString("A call prints its result and exits 0; exit 3 means it was refused, by a supervisor or by " +
			"the far side's own rules, and the output says why — an answer to plan around, not an error to " +
			"retry; exit 1 is a failure, on stderr.")
	} else {
		b.WriteString("A refusal, by a supervisor or by the far side's own rules, is an answer to plan around, " +
			"not an error to retry.")
	}
	b.WriteString("\n\n")
	return b.String()
}

// callToolInstalled is whether the binary is in a workspace, for the brief.
func callToolInstalled(root string) bool { return isFile(filepath.Join(binDir(root), CallToolBinary)) }
