package repocharters_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The scripts these tests run call the task's tools with verse-call-tool, which the embedded
// runner finds on PATH: built once, here, from the tree under test.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "repocharters-test")
	if err != nil {
		panic(err)
	}
	bin := filepath.Join(dir, "verse-call-tool")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/signadot/verse/cmd/verse-call-tool").CombinedOutput(); err != nil {
		panic(fmt.Sprintf("building verse-call-tool: %v\n%s", err, out))
	}
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
