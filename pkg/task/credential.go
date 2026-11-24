package task

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CredentialFileEnv names the variable a task's shell finds its credential's file under. The
// variable holds a path and never the token: a tool that takes a web identity token takes it as a
// file (`AWS_WEB_IDENTITY_TOKEN_FILE=$VIFF_CREDENTIAL_FILE`), and a path is all that every
// process the task starts inherits.
const CredentialFileEnv = "VIFF_CREDENTIAL_FILE"

// CredentialFile writes a launch's credential where its task can read it and its workspace does
// not hold it: a file of its own, in a fresh directory under the system's temporary directory,
// readable by its owner alone. A file in the workspace could be committed and pushed with the
// task's work, so a temporary directory that lies inside the workspace is refused.
//
// It answers the `NAME=value` a runner adds to the task's shell, and what removes the file when
// the task has ended.
func CredentialFile(token, workspace string) (env string, remove func(), err error) {
	dir, err := os.MkdirTemp("", "viff-credential-")
	if err != nil {
		return "", nil, err
	}
	remove = func() { _ = os.RemoveAll(dir) }
	if Inside(dir, workspace) {
		remove()
		return "", nil, fmt.Errorf("the temporary directory %s is inside the task's workspace %s, "+
			"and a credential is kept outside it", filepath.Dir(dir), workspace)
	}
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		remove()
		return "", nil, err
	}
	return CredentialFileEnv + "=" + path, remove, nil
}

// Inside reports whether dir is at or beneath root, as the filesystem resolves both.
func Inside(dir, root string) bool {
	if root == "" {
		return false
	}
	resolve := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		return p
	}
	rel, err := filepath.Rel(resolve(root), resolve(dir))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
