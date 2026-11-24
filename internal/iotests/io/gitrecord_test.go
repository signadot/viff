package io_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/entity"
	viffio "go.viff.dev/viff/pkg/io"
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
func reflectedRecords(t *testing.T, g *viffio.Git) map[string]entity.Payload {
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
	if err := os.MkdirAll(filepath.Join(dir, ".viff", "charters"), 0o755); err != nil {
		t.Fatal(err)
	}
	one := commit(t, dir, ".viff/charters/hello.tony", helloCharter, "hello")
	treeA := treeSha(t, dir, "main", ".viff/charters")
	two := commit(t, dir, "README", "elsewhere", "elsewhere")
	three := commit(t, dir, ".viff/charters/other.tony", otherCharter, "other")
	treeB := treeSha(t, dir, "main", ".viff/charters")
	g := viffio.NewGit()
	g.AddRepo("r", dir)
	r := newGitOutRig(t, g, "r")

	r.ask(t, "live", entity.Obj{"to": "record.live", "tree": treeA, "from": one[:10]})
	r.pass(t)
	first := refSha(t, dir, "refs/viff/live")
	if first == "" || refSha(t, dir, first+"^{tree}") != treeA {
		t.Fatalf("refs/viff/live = %q, holding %q; want an entry holding %s", first, refSha(t, dir, first+"^{tree}"), treeA)
	}
	if got := gitOut(t, dir, "log", "-1", "--format=%(trailers:key=From,valueonly)", first); got != one {
		t.Errorf("the entry's From: = %q, want %s", got, one)
	}
	if refSha(t, dir, first+"^") != "" {
		t.Error("the first entry has a parent")
	}
	st := r.status(t, "live")
	if st["ref"] != "refs/viff/live" || st["sha"] != first || st["tree"] != treeA || st["was"] != nil || st["error"] != nil {
		t.Fatalf("status = %v", st)
	}
	// The same tree from another commit: in step, nothing appended.
	r.ask(t, "live", entity.Obj{"to": "record.live", "tree": treeA, "from": two})
	r.pass(t)
	if got := refSha(t, dir, "refs/viff/live"); got != first {
		t.Fatalf("the same tree from another commit appended %s", got)
	}
	// Another tree: an entry on top of the first.
	r.ask(t, "live", entity.Obj{"to": "record.live", "tree": treeB, "from": three})
	r.pass(t)
	second := refSha(t, dir, "refs/viff/live")
	if second == first || refSha(t, dir, second+"^") != first || refSha(t, dir, second+"^{tree}") != treeB {
		t.Fatalf("after another tree, refs/viff/live = %s, parent %s", second, refSha(t, dir, second+"^"))
	}
	if st := r.status(t, "live"); st["was"] != first || st["sha"] != second {
		t.Errorf("status after an append = %v", st)
	}
	// Create only: a record that stands is kept.
	r.ask(t, "made", entity.Obj{"to": "record.live", "tree": treeA, "create": true})
	r.pass(t)
	if got := refSha(t, dir, "refs/viff/live"); got != second {
		t.Errorf("create: true appended to a record that stood: %s", got)
	}
	if st := r.status(t, "made"); st["kept"] != true || st["sha"] != second {
		t.Errorf("status of a kept record = %v", st)
	}

	// An entry somebody else wrote, with no From: trailer, is reflected all the same, its `from`
	// said empty: a record is a ref anybody who can push may write.
	byHand := gitOut(t, dir, "commit-tree", treeA, "-m", "by hand")
	gitOut(t, dir, "update-ref", "refs/viff/hand", byHand)

	recs := reflectedRecords(t, g)
	if p := recs["live"]; entity.Str(p, "sha") != second || entity.Str(p, "tree") != treeB || entity.Str(p, "from") != three {
		t.Errorf("the reflected record = %v", entity.ToObj(recs["live"]))
	}
	if p := recs["hand"]; entity.Str(p, "sha") != byHand || entity.Str(p, "tree") != treeA || entity.Get(p, "from") == nil || entity.Str(p, "from") != "" {
		t.Errorf("the reflected record written by hand = %v", entity.ToObj(recs["hand"]))
	}

	// Refused for good: a tree the repository lacks, a from it lacks, no tree, a full refname, and
	// every name git takes as no ref, which would otherwise fail at the push and be tried again.
	refused := map[string]entity.Obj{
		"nosuch":   {"to": "record.x", "tree": "0123456789abcdef0123456789abcdef01234567"},
		"nofrom":   {"to": "record.x", "tree": treeA, "from": "0123456789abcdef0123456789abcdef01234567"},
		"notree":   {"to": "record.x"},
		"refsname": {"to": "record.refs/heads/main", "tree": treeA},
	}
	for i, name := range []string{"a..b", "a:b", "a b", "a~1", "a^", "a?", "a[", "a@{1}", "x.lock", "a.", "a//b", "a/.b", "a\\b", "a/"} {
		refused[fmt.Sprintf("badname%d", i)] = entity.Obj{"to": "record." + name, "tree": treeA}
	}
	for name, in := range refused {
		r.ask(t, name, in)
	}
	r.pass(t)
	for name := range refused {
		if st := r.status(t, name); st["failed"] != viffio.PermanentFailure {
			t.Errorf("%s: status %v, want a permanent refusal", name, st)
		}
	}
	if refSha(t, dir, "refs/viff/x") != "" {
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
	if err := os.MkdirAll(filepath.Join(dir, ".viff", "charters"), 0o755); err != nil {
		t.Fatal(err)
	}
	one := commit(t, dir, ".viff/charters/hello.tony", helloCharter, "hello")
	treeA := treeSha(t, dir, "main", ".viff/charters")
	two := commit(t, dir, ".viff/charters/other.tony", otherCharter, "other")
	treeB := treeSha(t, dir, "main", ".viff/charters")
	g := viffio.NewGit()
	g.AddRepo("r", dir)
	r := newGitOutRig(t, g, "r")
	r.ask(t, "install", entity.Obj{"to": "install.repos.r", "tree": treeA, "sha": one})
	r.ask(t, "rec", entity.Obj{"to": "record.live", "tree": treeA, "from": one})
	r.pass(t)
	entry := refSha(t, dir, "refs/viff/live")

	check := func(round string) entity.Obj {
		t.Helper()
		r.ask(t, "live", entity.Obj{"to": "check.repos.r", "record": "live", "round": round})
		r.pass(t)
		return r.status(t, "live")
	}
	if st := check("1"); st["verdict"] != "installs" || fmt.Sprint(st["changes"]) != "0" ||
		st["record"] != "live" || st["sha"] != entry || st["tree"] != treeA || st["ref"] != nil || st["verdictSince"] != "1" {
		t.Fatalf("the record in step with what runs: status %v", st)
	}
	// The same verdict at the same entry a round later holds since the round it began.
	if st := check("1a"); st["verdictSince"] != "1" {
		t.Fatalf("the same verdict a round later: status %v", st)
	}
	r.ask(t, "rec", entity.Obj{"to": "record.live", "tree": treeB, "from": two})
	r.pass(t)
	// A new entry begins its verdict again, the same verdict or not.
	if st := check("2"); fmt.Sprint(st["changes"]) != "1" || fmt.Sprint(st["differingFor"]) != "1" || st["tree"] != treeB ||
		st["verdict"] != "installs" || st["verdictSince"] != "2" {
		t.Fatalf("the record moved to charters that do not run: status %v", st)
	}
	r.ask(t, "gone", entity.Obj{"to": "check.repos.r", "record": "nosuch", "round": "1"})
	r.ask(t, "both", entity.Obj{"to": "check.repos.r", "record": "live", "ref": "main", "round": "1"})
	r.pass(t)
	if st := r.status(t, "gone"); st["verdict"] != "missing" || st["error"] != nil {
		t.Fatalf("a record the repository does not have: status %v", st)
	}
	if st := r.status(t, "both"); st["failed"] != viffio.PermanentFailure {
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
	if err := os.MkdirAll(filepath.Join(work, ".viff", "charters"), 0o755); err != nil {
		t.Fatal(err)
	}
	one := commit(t, work, ".viff/charters/hello.tony", helloCharter, "hello")
	treeA := treeSha(t, work, "main", ".viff/charters")
	commit(t, work, ".viff/charters/other.tony", otherCharter, "other")
	treeB := treeSha(t, work, "main", ".viff/charters")
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitRun(t, work, "clone", "--bare", work, remote)

	g := viffio.NewGit()
	if _, err := g.AddMirror(context.Background(), "m", remote, filepath.Join(t.TempDir(), "m")); err != nil {
		t.Fatal(err)
	}
	r := newGitOutRig(t, g, "m")
	r.ask(t, "live", entity.Obj{"to": "record.live", "tree": treeA, "from": one})
	r.pass(t)
	entry := refSha(t, remote, "refs/viff/live")
	if entry == "" || refSha(t, remote, entry+"^{tree}") != treeA {
		t.Fatalf("the remote's refs/viff/live = %q", entry)
	}
	if p := reflectedRecords(t, g)["live"]; entity.Str(p, "sha") != entry {
		t.Errorf("the reflected record after the push = %v", entity.ToObj(p))
	}

	// Somebody wrote the record on the remote meanwhile: the lease refuses the overwrite.
	gitRun(t, work, "fetch", remote, "refs/viff/live:refs/viff/live")
	other := gitOut(t, work, "commit-tree", treeB, "-p", entry, "-m", "by hand")
	gitRun(t, work, "push", remote, other+":refs/viff/live")
	r.ask(t, "live", entity.Obj{"to": "record.live", "tree": treeB, "from": one})
	r.pass(t)
	if got := refSha(t, remote, "refs/viff/live"); got != other {
		t.Errorf("a stale lease overwrote the remote: %q, want %s", got, other)
	}
	if st := r.status(t, "live"); st["error"] == nil || st["failed"] == viffio.PermanentFailure {
		t.Errorf("status after a refused lease = %v, want a retryable error", st)
	}
}
