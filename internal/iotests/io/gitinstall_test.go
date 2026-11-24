package io_test

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/entity"
	viffio "go.viff.dev/viff/pkg/io"
)

// treeSha is the tree object of a path at a rev.
func treeSha(t *testing.T, dir, rev, path string) string {
	t.Helper()
	got := refSha(t, dir, rev+":"+path)
	if got == "" {
		t.Fatalf("no tree at %s:%s", rev, path)
	}
	return got
}

const helloCharter = `at: repos.r.hello
rules:
- name: hello
  condition:
    subject: {path: "repos.r.hello.in.*"}
  action:
    patch: {seen: true}
`

// An install input installs a tree of charters out of the repository into the viff instance at an
// address, as this io: archived from the tree object, every charter file in it in one request
// that prunes the address, recorded for the commit it was installed for, and done once per
// commit. A tree the repository lacks or a file that is not a charter is refused for good; the
// viff's own refusal of a charter is too.
func TestTheGitOutHalfInstallsATreeOfCharters(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(dir, ".viff", "charters", "README.md"), []byte("not a charter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", ".viff/charters/README.md")
	two := commit(t, dir, "unrelated", "x", "readme beside the charters")
	treeB := treeSha(t, dir, "main", ".viff/charters")
	broken := commit(t, dir, ".viff/charters/broken.tony", "at: [not: a: charter\n", "broken")
	treeC := treeSha(t, dir, "main", ".viff/charters")

	g := viffio.NewGit()
	g.AddRepo("r", dir)
	r := newGitOutRig(t, g, "r")

	installed := func() int {
		resp, err := r.viff.Get("/charter", url.Values{"at": {"repos.r.hello"}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if installed() == http.StatusOK {
		t.Fatal("the charter stands before anything installed it")
	}

	r.ask(t, "install", entity.Obj{"to": "install.repos.r", "tree": treeA, "sha": one})
	r.pass(t)
	st := r.status(t, "install")
	if st["tree"] != treeA || st["sha"] != one || fmt.Sprint(st["charters"]) != "1" || st["address"] != "repos.r" || st["error"] != nil {
		t.Fatalf("status after the install = %v", st)
	}
	if installed() != http.StatusOK {
		t.Fatal("the charter does not stand after the install")
	}
	// Installed for this commit: nothing to do.
	before := st["at"]
	r.pass(t)
	if st := r.status(t, "install"); st["at"] != before {
		t.Errorf("a pass with nothing to do rewrote the status: %v", st)
	}
	// The same tree at a new commit (a rollback) is installed again, and the README in it is
	// not a charter and is left alone.
	r.ask(t, "install", entity.Obj{"to": "install.repos.r", "tree": treeB, "sha": two})
	r.pass(t)
	if st := r.status(t, "install"); st["tree"] != treeB || st["sha"] != two || fmt.Sprint(st["charters"]) != "1" || st["error"] != nil {
		t.Fatalf("status after the second install = %v", st)
	}
	// A file that does not parse is refused for good, naming it; the charter that stood stands.
	r.ask(t, "install", entity.Obj{"to": "install.repos.r", "tree": treeC, "sha": broken})
	r.pass(t)
	if st := r.status(t, "install"); st["failed"] != viffio.PermanentFailure || !strings.Contains(fmt.Sprint(st["error"]), "broken.tony") ||
		st["tree"] != treeC || st["sha"] != broken || st["address"] != "repos.r" {
		t.Fatalf("a broken file: status %v, want a permanent refusal naming it, with the tree and commit refused", st)
	}
	if installed() != http.StatusOK {
		t.Error("a refused install took the standing charter down")
	}
	// Refused for good: a tree the repository lacks, no tree, no address.
	for name, in := range map[string]entity.Obj{
		"notree":  {"to": "install.repos.r", "tree": "0123456789abcdef0123456789abcdef01234567"},
		"nothing": {"to": "install.repos.r"},
		"noaddr":  {"to": "install.", "tree": treeA},
	} {
		r.ask(t, name, in)
	}
	r.pass(t)
	for _, name := range []string{"notree", "nothing", "noaddr"} {
		if st := r.status(t, name); st["failed"] != viffio.PermanentFailure {
			t.Errorf("%s: status %v, want a permanent refusal", name, st)
		}
	}
	if st := r.status(t, "notree"); st["tree"] != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("a tree the repository lacks: status %v, want the tree beside the refusal", st)
	}
	// THE EMPTY TREE is the charters directory gone: checked, it installs and prunes what stands;
	// installed, it takes every charter at the address down.
	r.ask(t, "check", entity.Obj{"to": "check.repos.r", "tree": viffio.EmptyTree})
	r.pass(t)
	if st := r.status(t, "check"); st["verdict"] != "installs" || !strings.Contains(fmt.Sprint(st["pruned"]), "repos.r.hello") {
		t.Fatalf("the empty tree's check = %v, want it to install and prune repos.r.hello", st)
	}
	if installed() != http.StatusOK {
		t.Fatal("checking the empty tree took the charter down")
	}
	r.ask(t, "install", entity.Obj{"to": "install.repos.r", "tree": viffio.EmptyTree, "sha": two})
	r.pass(t)
	if st := r.status(t, "install"); st["tree"] != viffio.EmptyTree || fmt.Sprint(st["charters"]) != "0" || st["error"] != nil {
		t.Fatalf("status after installing the empty tree = %v", st)
	}
	if installed() == http.StatusOK {
		t.Error("the charter stands after the empty tree was installed")
	}
}

// A check input asks the viff instance what an install of the tree would do and installs nothing: a tree
// that would install says so with what it adds; a file in the tree that does not parse is a
// verdict of `refused`, not a failed input; a tree checked already costs nothing; and the check
// and the install stay two inputs.
func TestTheGitOutHalfChecksATreeOfCharters(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".viff", "charters"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, ".viff/charters/hello.tony", helloCharter, "hello")
	treeA := treeSha(t, dir, "main", ".viff/charters")
	commit(t, dir, ".viff/charters/broken.tony", "at: [not: a: charter\n", "broken")
	treeB := treeSha(t, dir, "main", ".viff/charters")

	g := viffio.NewGit()
	g.AddRepo("r", dir)
	r := newGitOutRig(t, g, "r")
	installed := func() int {
		resp, err := r.viff.Get("/charter", url.Values{"at": {"repos.r.hello"}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	r.ask(t, "check", entity.Obj{"to": "check.repos.r", "tree": treeA})
	r.pass(t)
	st := r.status(t, "check")
	if st["verdict"] != "installs" || st["tree"] != treeA || st["address"] != "repos.r" || st["error"] != nil ||
		!strings.Contains(fmt.Sprint(st["packages"]), "rules.hello") {
		t.Fatalf("status after the check = %v", st)
	}
	if installed() == http.StatusOK {
		t.Fatal("the check installed the charter")
	}
	before := st["checkedAt"]
	r.pass(t)
	if st := r.status(t, "check"); st["checkedAt"] != before {
		t.Errorf("a tree checked already was checked again: %v", st)
	}
	// A file that does not parse: the verdict, with the file named, and no failure.
	r.ask(t, "check", entity.Obj{"to": "check.repos.r", "tree": treeB})
	r.pass(t)
	if st := r.status(t, "check"); st["verdict"] != "refused" || !strings.Contains(fmt.Sprint(st["refusal"]), "broken.tony") ||
		st["failed"] != nil || st["error"] != nil || st["packages"] != nil {
		t.Fatalf("a broken file: status %v, want a verdict of refused naming it", st)
	}
	// A tree the repository lacks is not refused for good: a pull request's tree can be asked
	// about before this io has fetched the branch that carries it, so it is asked again.
	r.ask(t, "missing", entity.Obj{"to": "check.repos.r", "tree": "0123456789abcdef0123456789abcdef01234567"})
	r.pass(t)
	if st := r.status(t, "missing"); st["failed"] == viffio.PermanentFailure || st["verdict"] != nil || st["error"] == nil {
		t.Fatalf("a missing tree: status %v, want an error asked again, not a refusal for good", st)
	}
}

const otherCharter = `at: repos.r.other
rules:
- name: other
  condition:
    subject: {path: "repos.r.other.in.*"}
  action:
    patch: {seen: true}
`

// A check of a ref resolves what the ref holds now, each round: in step with what runs it differs
// by nothing; moved to charters that do not run it counts the difference, and the rounds in a row
// it has differed at one commit; a round checked already costs nothing; a ref the repository does
// not have is the verdict `missing`.
func TestTheGitOutHalfChecksARefAgainstWhatRunsEachRound(t *testing.T) {
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
	gitRun(t, dir, "branch", "viff/live", one)
	two := commit(t, dir, ".viff/charters/other.tony", otherCharter, "other")

	g := viffio.NewGit()
	g.AddRepo("r", dir)
	if err := g.AddTree(".viff/charters"); err != nil {
		t.Fatal(err)
	}
	r := newGitOutRig(t, g, "r")
	r.ask(t, "install", entity.Obj{"to": "install.repos.r", "tree": treeA, "sha": one})
	r.pass(t)

	check := func(round string) entity.Obj {
		t.Helper()
		r.ask(t, "live", entity.Obj{"to": "check.repos.r", "ref": "viff/live", "round": round})
		r.pass(t)
		return r.status(t, "live")
	}
	st := check("1")
	if st["verdict"] != "installs" || fmt.Sprint(st["changes"]) != "0" || fmt.Sprint(st["differingFor"]) != "0" ||
		st["sha"] != one || st["tree"] != treeA || st["ref"] != "viff/live" || st["round"] != "1" {
		t.Fatalf("viff/live in step with what runs: status %v", st)
	}
	before := st["checkedAt"]
	r.pass(t)
	if st := r.status(t, "live"); st["checkedAt"] != before {
		t.Errorf("a round checked already was checked again: %v", st)
	}
	gitRun(t, dir, "branch", "-f", "viff/live", two)
	if st := check("2"); fmt.Sprint(st["changes"]) != "1" || fmt.Sprint(st["differingFor"]) != "1" || st["sha"] != two {
		t.Fatalf("viff/live moved to charters that do not run: status %v", st)
	}
	if st := check("3"); fmt.Sprint(st["differingFor"]) != "2" {
		t.Fatalf("a second round differing at the same commit: status %v", st)
	}
	// Back in step: the status says so WHOLE. A status is a patch, and a round's `packages`
	// replaces the round before's rather than landing on top of it, so neither the package the
	// tree no longer holds nor what an earlier round would have added stands in it.
	gitRun(t, dir, "branch", "-f", "viff/live", one)
	st = check("4")
	if fmt.Sprint(st["changes"]) != "0" || fmt.Sprint(st["differingFor"]) != "0" || st["sha"] != one {
		t.Fatalf("viff/live back in step: status %v", st)
	}
	if pkgs := fmt.Sprint(st["packages"]); strings.Contains(pkgs, "added") || strings.Contains(pkgs, "repos.r.other") {
		t.Fatalf("a round back in step carries the round before's lists: %s", pkgs)
	}
	r.ask(t, "gone", entity.Obj{"to": "check.repos.r", "ref": "no/such", "round": "1"})
	r.pass(t)
	if st := r.status(t, "gone"); st["verdict"] != "missing" || st["error"] != nil {
		t.Fatalf("a ref the repository does not have: status %v", st)
	}
}
