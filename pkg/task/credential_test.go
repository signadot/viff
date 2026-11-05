package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A task's credential is a file of its own, for its owner alone, under a variable that names the
// path and not the token; and it is never written inside the task's workspace, where it could be
// committed and pushed with the work.
func TestACredentialIsAFileOutsideTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	env, remove, err := CredentialFile("tok.en.sig", workspace)
	if err != nil {
		t.Fatal(err)
	}
	name, path, _ := strings.Cut(env, "=")
	if name != CredentialFileEnv || strings.Contains(env, "tok.en.sig") {
		t.Fatalf("the shell is told %q, want %s naming a path", env, CredentialFileEnv)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the file: %v, mode %v, want 0600", err, st.Mode().Perm())
	}
	if data, _ := os.ReadFile(path); string(data) != "tok.en.sig" {
		t.Errorf("the file holds %q", data)
	}
	if Inside(path, workspace) {
		t.Errorf("%s is inside the workspace %s", path, workspace)
	}
	remove()
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("the credential outlived its removal: %v", err)
	}

	// A temporary directory that lies inside the workspace is no place for one.
	t.Setenv("TMPDIR", workspace)
	if env, _, err := CredentialFile("tok.en.sig", workspace); err == nil {
		t.Errorf("a credential was written inside the workspace: %s", env)
	}
	if left, _ := os.ReadDir(workspace); len(left) != 0 {
		t.Errorf("the refusal left %d entries in the workspace", len(left))
	}
}
