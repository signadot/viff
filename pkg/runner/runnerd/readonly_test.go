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
	run := filepath.Join(base, "var", "viff")
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
	kept := KeptDirs(work, control, mirrors, "", "")
	for p, want := range map[string]string{
		run:                                    "control", // the parent of control and work
		control:                                "control", // the directory itself
		filepath.Join(work, "task-1"):          "work",    // a task's own, not made yet
		filepath.Join(real, "var/viff/work/x"): "work",    // the same, spelled the other way
		filepath.Join(mirrors, "o/r"):          "mirrors", // a repository, not made yet
		link:                                   "control", // the parent again, by a link
		filepath.Join(link, "control", "new"):  "control", // through the link, not made yet
		filepath.Join(base, "var"):             "control", // further up
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
	control := filepath.Join(base, "viff-runner-control-1")
	cache := filepath.Join(base, "cache")
	for _, d := range []string{control, cache} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	kept := KeptDirs("", control, "", "", "")
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

// THE SECRET STORE IS ONE OF THE RUNNER'S OWN: no readOnly path may hold it, be it, or lie in it,
// at the start, and one that comes to reach it after is left out of the launch, and said.
func TestAReadOnlyPathMayNotReachTheSecretStore(t *testing.T) {
	base := t.TempDir()
	store, cache := filepath.Join(base, "secrets"), filepath.Join(base, "cache")
	for _, d := range []string{store, cache} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	kept := KeptDirs("", "", "", store, "")
	for _, p := range []string{store, base, filepath.Join(store, "slack-bot")} {
		if err := CheckReadOnly([]string{p}, kept); err == nil || !strings.Contains(err.Error(), "the runner's secrets directory") {
			t.Errorf("readOnly %s: %v, want refused for the store", p, err)
		}
	}
	var log bytes.Buffer
	if got := withReadOnly(nil, []string{cache}, kept, &log); !slices.Equal(got, []string{cache}) {
		t.Fatalf("a plain cache: bound %q (%s)", got, log.String())
	}
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(store, cache); err != nil {
		t.Fatal(err)
	}
	log.Reset()
	if got := withReadOnly(nil, []string{cache}, kept, &log); len(got) != 0 {
		t.Errorf("a cache turned link to the store was bound: %q", got)
	}
	if !strings.Contains(log.String(), "the runner's secrets directory") {
		t.Errorf("said %q", log.String())
	}
}

// THE STORE IS WHERE NO SANDBOX BINDS IT. One under a system directory would be every task's to
// read, since every sandbox has those; one that holds or lies in the runner's work, mirrors or
// control directory is refused as a readOnly path there is. A link is where it leads.
func TestTheSecretStoreIsWhereNoSandboxBindsIt(t *testing.T) {
	base := t.TempDir()
	work, control, mirrors := filepath.Join(base, "work"), filepath.Join(base, "control"), filepath.Join(base, "mirrors")
	store := filepath.Join(base, "secrets")
	for _, d := range []string{work, control, mirrors, store} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	intoEtc := filepath.Join(base, "into-etc")
	if err := os.Symlink("/etc", intoEtc); err != nil {
		t.Fatal(err)
	}
	kept := KeptDirs(work, control, mirrors, "", "")
	for dir, want := range map[string]string{
		"/etc/viff/secrets":              "every task's sandbox has",
		"/opt/secrets":                   "every task's sandbox has",
		"/usr/local/share/secrets":       "every task's sandbox has",
		"/etc":                           "every task's sandbox has",
		filepath.Join(intoEtc, "viff"):   "every task's sandbox has",
		"/":                              "the whole machine",
		base:                             "overlaps the runner's control directory",
		filepath.Join(work, "t1", "s"):   "overlaps the runner's work directory",
		filepath.Join(mirrors, "secret"): "overlaps the runner's mirrors directory",
		control:                          "overlaps the runner's control directory",
	} {
		if err := CheckSecretStore(dir, kept); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("secrets.dir %s: %v, want refused: %s", dir, err, want)
		}
	}
	for _, dir := range []string{store, filepath.Join(base, "not-made-yet"), "/secrets", "/etcetera/secrets"} {
		if err := CheckSecretStore(dir, kept); err != nil {
			t.Errorf("secrets.dir %s: %v", dir, err)
		}
	}
}
