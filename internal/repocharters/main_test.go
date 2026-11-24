package repocharters_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The scripts these tests run call the task's tools with viff-call-tool, which the embedded
// runner finds on PATH: built once, here, from the tree under test.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "repocharters-test")
	if err != nil {
		panic(err)
	}
	bin := filepath.Join(dir, "viff-call-tool")
	if out, err := exec.Command("go", "build", "-o", bin, "go.viff.dev/viff/cmd/viff-call-tool").CombinedOutput(); err != nil {
		panic(fmt.Sprintf("building viff-call-tool: %v\n%s", err, out))
	}
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
