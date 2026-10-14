package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/task"
)

// A path may not leave the working directory, by traversal OR through a symlink.
//
// The symlink half is the one that was missing, and it is not a hypothetical shape: a working
// directory here is frequently a git checkout, git checks symlinks out as symlinks, and
// `link/pwned` contains no `..` — so the lexical check approved it and the write followed the
// link off the end of the world. No shell, no traversal, nothing clever from the model; just a
// repository that happens to carry a symlink.
func TestSafePathRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink whose target is a FILE outside, named directly.
	if err := os.Symlink(filepath.Join(outside, "target.txt"), filepath.Join(root, "leaf")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "target.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, path string
		ok         bool
	}{
		{"an ordinary file", "real.txt", true},
		{"a file that does not exist yet — a write names one", "new.txt", true},
		{"a nested file that does not exist yet", "sub/dir/new.txt", true},
		{"traversal", "../pwned", false},
		{"traversal, buried", "a/../../pwned", false},
		// Not refused, and worth pinning as behaviour rather than assumed either way:
		// filepath.Join strips the leading separator, so an absolute path is REINTERPRETED as
		// root-relative. `<root>/etc/passwd` is inside the root, so it is safe — the model
		// simply reads something that is almost certainly not there. The frame already tells
		// it every path is relative to the working directory.
		{"an absolute path, rooted rather than refused", "/etc/passwd", true},
		{"through a symlinked directory", "link/pwned", false},
		{"a symlink to a file outside", "leaf", false},
		{"deeper through a symlinked directory", "link/a/b/pwned", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := safePath(root, tc.path)
			if tc.ok && err != nil {
				t.Fatalf("safePath(%q) = %v, want it allowed", tc.path, err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("safePath(%q) = %q, want refused", tc.path, got)
				}
				return
			}
			// An allowed path must genuinely land inside the root once resolved, which is the
			// property the caller relies on rather than the absence of an error.
			real, rerr := resolveExisting(got)
			if rerr != nil {
				t.Fatalf("resolving %q: %v", got, rerr)
			}
			realRoot, _ := filepath.EvalSymlinks(root)
			if escapes(realRoot, real) {
				t.Fatalf("safePath allowed %q, which resolves to %q, outside %q", tc.path, real, realRoot)
			}
		})
	}
}

// The root itself is very often reached through a symlink — /var is /private/var on macOS, which
// is where every temp dir lives — so resolving the candidate without resolving the root would
// refuse every legitimate write. This is the regression that would make the fix worse than the
// bug.
func TestSafePathAllowsASymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	linkedRoot := filepath.Join(base, "via-link")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, p := range []string{"a.txt", "sub/b.txt"} {
		if _, err := safePath(linkedRoot, p); err != nil {
			t.Errorf("safePath(<symlinked root>, %q) = %v, want it allowed", p, err)
		}
	}
	// And it still refuses an escape from a symlinked root.
	if _, err := safePath(linkedRoot, "../pwned"); err == nil {
		t.Error("traversal out of a symlinked root was allowed")
	}
}

// The refusal says which kind it was, because "escapes the working directory" sent a reader
// looking for a `..` that is not in the path.
func TestSafePathNamesTheSymlinkCase(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := safePath(root, "link/x")
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want it to name the symlink", err)
	}
}

// A filesystem error tells the agent what to do next, in the paths it was given.
//
// Both halves come from one line a drive produced:
//
//	ERROR: read /Users/scott/.verse-coderunner/workspaces/docsdrift-e1f3.../verse/trigger: is a directory
//
// It names no remedy, so the model spends a turn deducing that list_dir exists; and it speaks in
// absolute paths after the frame told the agent every path is relative to its working directory.
func TestFilesystemErrorsAreActionableAndRelative(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "trigger"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &fsExec{root: root, log: io.Discard}
	fail := func(tool, path string) string {
		t.Helper()
		out, _, err := e.Execute(context.Background(), task.ToolUse{
			Name: tool, Input: map[string]any{"path": path}})
		if err != nil {
			t.Fatal(err)
		}
		return out.Output
	}

	// read_file on a directory names the tool that would have worked.
	if got := fail("read_file", "trigger"); !strings.Contains(got, "use list_dir") {
		t.Errorf("read_file on a directory does not name the remedy: %q", got)
	}
	// list_dir on a file, symmetrically.
	if got := fail("list_dir", "note.md"); !strings.Contains(got, "use read_file") {
		t.Errorf("list_dir on a file does not name the remedy: %q", got)
	}
	// A missing path is a missing path, said plainly.
	if got := fail("read_file", "nope.md"); !strings.Contains(got, "no such file") {
		t.Errorf("a missing file: %q", got)
	}
	// And no message leaks the host path the agent was never told about.
	for _, p := range []string{"trigger", "note.md", "nope.md"} {
		for _, tool := range []string{"read_file", "list_dir"} {
			if got := fail(tool, p); strings.Contains(got, root) {
				t.Errorf("%s %s leaks the working directory into the agent's view: %q", tool, p, got)
			}
		}
	}
}

// flakyCompleter fails a set number of times, then answers. (llm_test.go already has a
// fakeCompleter, which scripts turns rather than failures.)
type flakyCompleter struct {
	fails int
	err   error
	calls int
}

func (f *flakyCompleter) complete(context.Context, []chatMessage, []toolDef) (chatMessage, string, error) {
	f.calls++
	if f.calls <= f.fails {
		return chatMessage{}, "", f.err
	}
	return chatMessage{Role: "assistant", Content: "done"}, "stop", nil
}

// A transient model failure is retried; a permanent one is not.
//
// One slow response used to end the whole task: 43 boundaries of work — reads, reasoning, a
// finding half-written — died on `Client.Timeout exceeded while awaiting headers`, and a task's
// conversation lives in its driver, so there was nothing to resume. Found by driving it.
func TestTheModelCallIsRetriedWhenTheFailureSaysNothingAboutTheRequest(t *testing.T) {
	saved := modelBackoff
	modelBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { modelBackoff = saved }()

	// Two transient failures, then an answer: the task survives.
	f := &flakyCompleter{fails: 2, err: transient{errors.New("context deadline exceeded")}}
	d := &llmDriver{client: f, log: io.Discard}
	msg, _, err := d.completeWithRetry(context.Background())
	if err != nil {
		t.Fatalf("a hiccup ended the task: %v", err)
	}
	if msg.Content != "done" || f.calls != 3 {
		t.Errorf("calls = %d, msg = %q", f.calls, msg.Content)
	}

	// A permanent failure is not retried: asking again cannot change a bad key or a malformed
	// request, and doing so spends the task's budget on a fixed answer.
	f = &flakyCompleter{fails: 99, err: errors.New("api: Invalid API Key")}
	d = &llmDriver{client: f, log: io.Discard}
	if _, _, err = d.completeWithRetry(context.Background()); err == nil {
		t.Fatal("a permanent failure was swallowed")
	}
	if f.calls != 1 {
		t.Errorf("a permanent failure was retried %d times", f.calls-1)
	}

	// Exhausted retries say how many attempts, and carry the last failure.
	f = &flakyCompleter{fails: 99, err: transient{errors.New("boom")}}
	d = &llmDriver{client: f, log: io.Discard}
	_, _, err = d.completeWithRetry(context.Background())
	if f.calls != 3 {
		t.Errorf("attempts = %d, want 1 + %d retries", f.calls, len(modelBackoff))
	}
	if !strings.Contains(err.Error(), "3 time(s)") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("the failure does not account for itself: %v", err)
	}

	// A cancelled task stops asking, and reports the cancel rather than the failure it was
	// retrying — the cancel is the better account of why the run ended.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f = &flakyCompleter{fails: 99, err: transient{errors.New("boom")}}
	d = &llmDriver{client: f, log: io.Discard}
	if _, _, err = d.completeWithRetry(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled task kept retrying: %v", err)
	}
}

// slowCompleter blocks until released, so a call in flight can be observed.
type slowCompleter struct{ release chan struct{} }

func (s *slowCompleter) complete(context.Context, []chatMessage, []toolDef) (chatMessage, string, error) {
	<-s.release
	return chatMessage{Role: "assistant", Content: "eventually"}, "stop", nil
}

// A model call in flight says so, and a retried sequence stops asking once its budget is spent.
//
// The retry alone made a stall invisible for LONGER: three attempts at five minutes is a quarter
// of an hour in which a task emits nothing and cannot be told from one that has died. A drive
// watched exactly that and reported "it appears hung", which was the correct reading of the
// evidence available.
func TestALongModelCallSaysSoAndARetriedSequenceIsBounded(t *testing.T) {
	savedNotice, savedBudget, savedBackoff := modelWaitNotice, modelTotalBudget, modelBackoff
	defer func() {
		modelWaitNotice, modelTotalBudget, modelBackoff = savedNotice, savedBudget, savedBackoff
	}()
	modelWaitNotice = 5 * time.Millisecond

	// In flight: the read plane hears about it before the call returns.
	var mu sync.Mutex
	var said []string
	slow := &slowCompleter{release: make(chan struct{})}
	d := &llmDriver{client: slow, log: io.Discard, emit: func(f task.Event) {
		mu.Lock()
		said = append(said, f.Text)
		mu.Unlock()
	}}
	go func() { time.Sleep(60 * time.Millisecond); close(slow.release) }()
	if _, _, err := d.completeWithRetry(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	heard := len(said)
	mu.Unlock()
	if heard == 0 {
		t.Error("a call that took twelve notice periods emitted nothing — a task mid-call is " +
			"indistinguishable from one that has died")
	}

	// Budget: a sequence that keeps failing stops asking rather than spending every attempt.
	modelBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	modelTotalBudget = 0 // anything already spent is over budget
	f := &flakyCompleter{fails: 99, err: transient{errors.New("boom")}}
	d = &llmDriver{client: f, log: io.Discard}
	_, _, err := d.completeWithRetry(context.Background())
	if err == nil {
		t.Fatal("expected failure")
	}
	// The property is that the sequence stops EARLY, not that it stops at a particular count:
	// a budget already spent has nothing left to give, so it does not even reach the first
	// retry. Asserting a count here would be asserting the arithmetic rather than the rule.
	if f.calls >= 1+len(modelBackoff) {
		t.Errorf("attempts = %d — the budget did not stop the sequence", f.calls)
	}
	if !strings.Contains(err.Error(), "not a hiccup") {
		t.Errorf("giving up does not say why: %v", err)
	}
}
