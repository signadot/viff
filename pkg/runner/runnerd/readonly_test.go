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
	got := withReadOnly(programs, []string{keep, gone}, nil, &log)
	if !slices.Equal(got, []string{"/opt/tool", keep}) {
		t.Errorf("bound %q", got)
	}
	if !strings.Contains(log.String(), gone) || strings.Contains(log.String(), keep) {
		t.Errorf("said %q", log.String())
	}
	if got2 := withReadOnly(programs, []string{gone}, nil, &log); !slices.Equal(got2, programs) {
		t.Errorf("a second launch bound %q: the first's paths leaked into the runner's programs", got2)
	}
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := withReadOnly(programs, []string{gone}, nil, &log); !slices.Contains(got, gone) {
		t.Errorf("a path back again is bound: %q", got)
	}
}

// A READ-ONLY PATH MAY NOT REACH WHAT THE RUNNER KEEPS FROM ITS TASKS: its work, its mirrors, and
// its control directory, whose sockets a read-only mount would still let a task connect to. A
// path that holds one, is one, or lies in one is refused, links followed, whether or not the path
// exists yet; one beside them is not.
func TestAReadOnlyPathMayNotReachTheRunnersOwn(t *testing.T) {
	real := t.TempDir()
	// base reaches real through a link, as t.TempDir's /var reaches /private/var on macOS: the
	// runner's directories are spelled through it, and the paths below through either.
	base := filepath.Join(t.TempDir(), "base")
	if err := os.Symlink(real, base); err != nil {
		t.Fatal(err)
	}
	run := filepath.Join(base, "var", "verse")
	work, control, mirrors := filepath.Join(run, "work"), filepath.Join(run, "control"), filepath.Join(base, "mirrors")
	for _, d := range []string{work, control, mirrors, filepath.Join(base, "cache", "go")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "sneaky")
	if err := os.Symlink(run, link); err != nil {
		t.Fatal(err)
	}
	kept := KeptDirs(work, control, mirrors)
	for p, want := range map[string]string{
		run:                                     "control", // the parent of control and work
		control:                                 "control", // the directory itself
		filepath.Join(work, "task-1"):           "work",    // a task's own, not made yet
		filepath.Join(real, "var/verse/work/x"): "work",    // the same, spelled the other way
		filepath.Join(mirrors, "o/r"):           "mirrors", // a repository, not made yet
		link:                                    "control", // the parent again, by a link
		filepath.Join(link, "control", "new"):   "control", // through the link, not made yet
		filepath.Join(base, "var"):              "control", // further up
	} {
		err := CheckReadOnly([]string{filepath.Join(base, "cache", "go"), p}, kept)
		if err == nil || !strings.Contains(err.Error(), "the runner's "+want) {
			t.Errorf("readOnly %s: %v, want refused for its %s", p, err, want)
		}
	}
	if err := CheckReadOnly([]string{filepath.Join(base, "cache", "go"), filepath.Join(base, "var", "other")}, kept); err != nil {
		t.Errorf("paths beside the runner's own: %v", err)
	}
}

// THE LAUNCH CHECKS AGAIN: a path that was fine at start and has since become a link to what the
// runner keeps (or appeared there) is left out of the task, and said.
func TestALaunchLeavesOutAPathThatNowReachesTheRunnersOwn(t *testing.T) {
	base := t.TempDir()
	control := filepath.Join(base, "verse-runner-control-1")
	cache := filepath.Join(base, "cache")
	for _, d := range []string{control, cache} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	kept := KeptDirs("", control, "")
	var log bytes.Buffer
	if got := withReadOnly(nil, []string{cache}, kept, &log); !slices.Equal(got, []string{cache}) {
		t.Fatalf("a plain cache: bound %q (%s)", got, log.String())
	}
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(base, cache); err != nil { // now $TMPDIR, as it were
		t.Fatal(err)
	}
	log.Reset()
	if got := withReadOnly(nil, []string{cache}, kept, &log); len(got) != 0 {
		t.Errorf("a cache turned link to the control directory's parent was bound: %q", got)
	}
	if !strings.Contains(log.String(), "the runner's control") {
		t.Errorf("said %q", log.String())
	}
}
