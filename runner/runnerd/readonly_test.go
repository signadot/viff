package runnerd

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A READ-ONLY PATH IS LOOKED FOR AT EACH LAUNCH: one there is bound, one removed since the runner
// started is left out of the task and said, rather than failing the launch.
func TestAReadOnlyPathGoneIsLeftOutOfTheLaunch(t *testing.T) {
	keep, gone := t.TempDir(), filepath.Join(t.TempDir(), "gone")
	programs := make([]string, 1, 4)
	programs[0] = "/opt/tool"
	var log bytes.Buffer
	got := withReadOnly(programs, []string{keep, gone}, &log)
	if !slices.Equal(got, []string{"/opt/tool", keep}) {
		t.Errorf("bound %q", got)
	}
	if !strings.Contains(log.String(), gone) || strings.Contains(log.String(), keep) {
		t.Errorf("said %q", log.String())
	}
	if got2 := withReadOnly(programs, []string{gone}, &log); !slices.Equal(got2, programs) {
		t.Errorf("a second launch bound %q: the first's paths leaked into the runner's programs", got2)
	}
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := withReadOnly(programs, []string{gone}, &log); !slices.Contains(got, gone) {
		t.Errorf("a path back again is bound: %q", got)
	}
}
