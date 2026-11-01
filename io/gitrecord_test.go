package io_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
)

// gitOut is what a git command prints in dir, trimmed.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// reflectedRecords is the records a reflection carries, by name.
func reflectedRecords(t *testing.T, g *verseio.Git) map[string]entity.Payload {
	t.Helper()
	rn, err := g.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	recs := map[string]entity.Payload{}
	for _, e := range rn.Entities {
		if segs := e.Ref.Segs(); len(segs) == 4 && segs[2] == "record" {
			recs[segs[3]] = e.Payload
		}
	}
	return recs
}

// A record holds a tree as its entry's whole tree, says the commit it came from, and is appended
// to only for another tree: the same tree from another commit is the same record. Its entries
// chain, and it is reflected as a record, not a ref.
func TestARecordHoldsATreeAndChainsItsEntries(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".verse", "charters"), 0o755); err != nil {
		t.Fatal(err)
	}
	one := commit(t, dir, ".verse/charters/hello.tony", helloCharter, "hello")
	treeA := treeSha(t, dir, "main", ".verse/charters")
	two := commit(t, dir, "README", "elsewhere", "elsewhere")
	three := commit(t, dir, ".verse/charters/other.tony", otherCharter, "other")
	treeB := treeSha(t, dir, "main", ".verse/charters")
	g := verseio.NewGit()
	g.AddRepo("r", dir)
	r := newGitOutRig(t, g, "r")

	r.ask(t, "live", entity.Obj{"to": "record.live", "tree": treeA, "from": one[:10]})
	r.pass(t)
	first := refSha(t, dir, "refs/verse/live")
	if first == "" || refSha(t, dir, first+"^{tree}") != treeA {
		t.Fatalf("refs/verse/live = %q, holding %q; want an entry holding %s", first, refSha(t, dir, first+"^{tree}"), treeA)
	}
	if got := gitOut(t, dir, "log", "-1", "--format=%(trailers:key=From,valueonly)", first); got != one {
		t.Errorf("the entry's From: = %q, want %s", got, one)
	}
	if refSha(t, dir, first+"^") != "" {
		t.Error("the first entry has a parent")
	}
	st := r.status(t, "live")
	if st["ref"] != "refs/verse/live" || st["sha"] != first || st["tree"] != treeA || st["was"] != nil || st["error"] != nil {
		t.Fatalf("status = %v", st)
	}
	// The same tree from another commit: in step, nothing appended.
	r.ask(t, "live", entity.Obj{"to": "record.live", "tree": treeA, "from": two})
	r.pass(t)
	if got := refSha(t, dir, "refs/verse/live"); got != first {
		t.Fatalf("the same tree from another commit appended %s", got)
	}
	// Another tree: an entry on top of the first.
	r.ask(t, "live", entity.Obj{"to": "record.live", "tree": treeB, "from": three})
	r.pass(t)
	second := refSha(t, dir, "refs/verse/live")
	if second == first || refSha(t, dir, second+"^") != first || refSha(t, dir, second+"^{tree}") != treeB {
		t.Fatalf("after another tree, refs/verse/live = %s, parent %s", second, refSha(t, dir, second+"^"))
	}
	if st := r.status(t, "live"); st["was"] != first || st["sha"] != second {
		t.Errorf("status after an append = %v", st)
	}
	// Create only: a record that stands is kept.
	r.ask(t, "made", entity.Obj{"to": "record.live", "tree": treeA, "create": true})
	r.pass(t)
	if got := refSha(t, dir, "refs/verse/live"); got != second {
		t.Errorf("create: true appended to a record that stood: %s", got)
	}
	if st := r.status(t, "made"); st["kept"] != true || st["sha"] != second {
		t.Errorf("status of a kept record = %v", st)
	}

	recs := reflectedRecords(t, g)
	if p := recs["live"]; entity.Str(p, "sha") != second || entity.Str(p, "tree") != treeB || entity.Str(p, "from") != three {
		t.Errorf("the reflected record = %v", entity.ToObj(recs["live"]))
	}

	// Refused for good: a tree the repository lacks, a from it lacks, no tree, a bad name.
	for name, in := range map[string]entity.Obj{
		"nosuch":   {"to": "record.x", "tree": "0123456789abcdef0123456789abcdef01234567"},
		"nofrom":   {"to": "record.x", "tree": treeA, "from": "0123456789abcdef0123456789abcdef01234567"},
		"notree":   {"to": "record.x"},
		"badname":  {"to": "record.a..b", "tree": treeA},
		"refsname": {"to": "record.refs/heads/main", "tree": treeA},
	} {
		r.ask(t, name, in)
	}
	r.pass(t)
	for _, name := range []string{"nosuch", "nofrom", "notree", "badname", "refsname"} {
		if st := r.status(t, name); st["failed"] != verseio.PermanentFailure {
			t.Errorf("%s: status %v, want a permanent refusal", name, st)
		}
	}
	if refSha(t, dir, "refs/verse/x") != "" {
		t.Error("a refused input wrote a record")
	}
}

// A record is checked against what runs as a ref is: its entry's tree, in step or differing,
// and `missing` where there is no such record.
func TestARecordIsCheckedAgainstWhatRuns(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".verse", "charters"), 0o755); err != nil {
		t.Fatal(err)
	}
	one := commit(t, dir, ".verse/charters/hello.tony", helloCharter, "hello")
	treeA := treeSha(t, dir, "main", ".verse/charters")
	two := commit(t, dir, ".verse/charters/other.tony", otherCharter, "other")
	treeB := treeSha(t, dir, "main", ".verse/charters")
	g := verseio.NewGit()
	g.AddRepo("r", dir)
	r := newGitOutRig(t, g, "r")
	r.ask(t, "install", entity.Obj{"to": "install.repos.r", "tree": treeA, "sha": one})
	r.ask(t, "rec", entity.Obj{"to": "record.live", "tree": treeA, "from": one})
	r.pass(t)
	entry := refSha(t, dir, "refs/verse/live")

	check := func(round string) entity.Obj {
		t.Helper()
		r.ask(t, "live", entity.Obj{"to": "check.repos.r", "record": "live", "round": round})
		r.pass(t)
		return r.status(t, "live")
	}
	if st := check("1"); st["verdict"] != "installs" || fmt.Sprint(st["changes"]) != "0" ||
		st["record"] != "live" || st["sha"] != entry || st["tree"] != treeA || st["ref"] != nil {
		t.Fatalf("the record in step with what runs: status %v", st)
	}
	r.ask(t, "rec", entity.Obj{"to": "record.live", "tree": treeB, "from": two})
	r.pass(t)
	if st := check("2"); fmt.Sprint(st["changes"]) != "1" || fmt.Sprint(st["differingFor"]) != "1" || st["tree"] != treeB {
		t.Fatalf("the record moved to charters that do not run: status %v", st)
	}
	r.ask(t, "gone", entity.Obj{"to": "check.repos.r", "record": "nosuch", "round": "1"})
	r.ask(t, "both", entity.Obj{"to": "check.repos.r", "record": "live", "ref": "main", "round": "1"})
	r.pass(t)
	if st := r.status(t, "gone"); st["verdict"] != "missing" || st["error"] != nil {
		t.Fatalf("a record the repository does not have: status %v", st)
	}
	if st := r.status(t, "both"); st["failed"] != verseio.PermanentFailure {
		t.Fatalf("a check naming a record and a ref: status %v", st)
	}
}

// A mirror pushes a record's entry to the remote it fetches from, with a lease on the entry
// before, and the next reflection carries it.
func TestARecordIsPushedFromAMirror(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	work := t.TempDir()
	initRepo(t, work)
	if err := os.MkdirAll(filepath.Join(work, ".verse", "charters"), 0o755); err != nil {
		t.Fatal(err)
	}
	one := commit(t, work, ".verse/charters/hello.tony", helloCharter, "hello")
	treeA := treeSha(t, work, "main", ".verse/charters")
	commit(t, work, ".verse/charters/other.tony", otherCharter, "other")
	treeB := treeSha(t, work, "main", ".verse/charters")
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitRun(t, work, "clone", "--bare", work, remote)

	g := verseio.NewGit()
	if _, err := g.AddMirror(context.Background(), "m", remote, filepath.Join(t.TempDir(), "m")); err != nil {
		t.Fatal(err)
	}
	r := newGitOutRig(t, g, "m")
	r.ask(t, "live", entity.Obj{"to": "record.live", "tree": treeA, "from": one})
	r.pass(t)
	entry := refSha(t, remote, "refs/verse/live")
	if entry == "" || refSha(t, remote, entry+"^{tree}") != treeA {
		t.Fatalf("the remote's refs/verse/live = %q", entry)
	}
	if p := reflectedRecords(t, g)["live"]; entity.Str(p, "sha") != entry {
		t.Errorf("the reflected record after the push = %v", entity.ToObj(p))
	}

	// Somebody wrote the record on the remote meanwhile: the lease refuses the overwrite.
	gitRun(t, work, "fetch", remote, "refs/verse/live:refs/verse/live")
	other := gitOut(t, work, "commit-tree", treeB, "-p", entry, "-m", "by hand")
	gitRun(t, work, "push", remote, other+":refs/verse/live")
	r.ask(t, "live", entity.Obj{"to": "record.live", "tree": treeB, "from": one})
	r.pass(t)
	if got := refSha(t, remote, "refs/verse/live"); got != other {
		t.Errorf("a stale lease overwrote the remote: %q, want %s", got, other)
	}
	if st := r.status(t, "live"); st["error"] == nil || st["failed"] == verseio.PermanentFailure {
		t.Errorf("status after a refused lease = %v, want a retryable error", st)
	}
}
