package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/entity"
	viffio "go.viff.dev/viff/pkg/io"
	viffiod "go.viff.dev/viff/pkg/io/iod"
)

// sourceFace is a real source for the client to install: the same iod handler viff's own
// in-process source is, so the install probe has something truthful to find.
func sourceFace(t *testing.T, repo string) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "Tester"},
		// A commit, because an UNBORN repo reflects nothing at all: gitHead answers nil with no
		// HEAD sha, and there are no refs yet either. A removal test needs something reflected
		// to fork on.
		{"commit", "--allow-empty", "-m", "first"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	g := viffio.NewGit()
	g.AddRepo(repo, dir)
	srv := httptest.NewServer(viffiod.New(g).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// `viff iod add` reaches the route, through the client the way a person types it.
//
// This test exists for the reason the runner's does: every other test of this feature drives
// the HTTP layer directly, so all of them can pass while the command an operator would run
// reaches nothing at all. A client is a layer, and a layer with no test is a layer that does
// not work.
func TestSourceAddThroughTheClient(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	store := serve(t)
	face := sourceFace(t, "viff")

	out, err := runCLI(t, "iod", "add", "--root", "git", "laptop", face.URL)
	if err != nil {
		t.Fatalf("io add: %v", err)
	}
	if !strings.Contains(out, "mirror.iods.laptop") {
		t.Fatalf("add printed %q, want the ref it installed", out)
	}
	// The probe ran and said what it found — the half of `add` that only happens on the way
	// through the server.
	if !strings.Contains(out, "a source") {
		t.Fatalf("add printed %q, want the probe's verdict", out)
	}
	if payloadOf(t, store, viffio.Ref("laptop")) == nil {
		t.Fatal("nothing was installed")
	}

	// It is listed, shown, and checked by the same address the operator typed.
	if out, err := runCLI(t, "iods"); err != nil || !strings.Contains(out, "mirror.iods.laptop") {
		t.Fatalf("sources = %q (%v)", out, err)
	}
	if out, err := runCLI(t, "iod", "show", "laptop"); err != nil ||
		!strings.Contains(out, face.URL) {
		t.Fatalf("source show = %q (%v)", out, err)
	}
	// check asks it NOW, and reports what it says beside what was recorded.
	if out, err := runCLI(t, "iod", "check", "mirror.iods.laptop"); err != nil ||
		!strings.Contains(out, "a source") {
		t.Fatalf("source check = %q (%v)", out, err)
	}
	// A bare remove is REFUSED once it has reflected something, and the flags reach the route
	// the way a person types them — the half of a command that is silently unreachable when the
	// client builds its request wrongly.
	waitForCLI(t, store, entity.RefAt(viffio.GitRoot, "viff", "head"))
	if _, err := runCLI(t, "iod", "remove", "laptop"); err == nil {
		t.Fatal("a bare remove with a reflected slice should be refused")
	}
	if _, err := runCLI(t, "iod", "remove", "--keep", "laptop"); err != nil {
		t.Fatalf("source remove --keep: %v", err)
	}
	if payloadOf(t, store, viffio.Ref("viff")) != nil {
		t.Fatal("--keep should leave the reflected slice standing")
	}
	if payloadOf(t, store, viffio.Ref("laptop")) != nil {
		t.Fatal("remove left the registration standing")
	}
}

// The flags, which are the half of a command that is silently unreachable when the client
// builds its request wrongly — the runner's --no-probe rode in a path string and 404d.
func TestSourceAddFlagsThroughTheClient(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	serve(t)

	// --no-probe installs something that is not a source, because nothing asked.
	dead := httptest.NewServer(http.NotFoundHandler())
	defer dead.Close()
	if _, err := runCLI(t, "iod", "add", "--no-probe", "--root", "git", "blind", dead.URL); err != nil {
		t.Fatalf("--no-probe should install without asking: %v", err)
	}
	// Without it, the same url is refused.
	if _, err := runCLI(t, "iod", "add", "git:seeing", dead.URL); err == nil {
		t.Fatal("a url that answers and is not a source should be refused")
	}

}

// --dir is the in-process git source: no url, no network, and it still describes itself.
//
// Its own server, and that is not tidiness. The test above installs a source with --no-probe,
// which leaves it claiming nothing — and a registration that claims nothing can be proved apart
// from nothing, so it refuses every later install in its system. That is the documented
// consequence of the ownership rule and it bit here first: worth knowing that ONE unclaimed
// source makes its whole system need --allow-overlap.
func TestSourceAddDirThroughTheClient(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	serve(t)

	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	out, err := runCLI(t, "iod", "add", "--dir", dir, "local")
	if err != nil {
		t.Fatalf("--dir: %v", err)
	}
	if !strings.Contains(out, "a source") {
		t.Fatalf("a dir source should describe itself in task, got %q", out)
	}
}

// The document form, which is what a deployment writes. It is the path where an unknown field
// has to be NAMED rather than dropped — a `owns` that decoded to nothing would install a source
// that claims nothing, and be refused beside its neighbours for a reason nobody typed.
func TestSourceAddFromAFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	serve(t)
	face := sourceFace(t, "viff")

	path := filepath.Join(t.TempDir(), "source.tony")
	doc := "name: laptop\nroot: git\nurl: \"" + face.URL + "\"\n" +
		"owns:\n- {at: \"git.viff\"}\n"
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "iod", "add", "--file", path); err != nil ||
		!strings.Contains(out, "mirror.iods.laptop") {
		t.Fatalf("io add --file = %q (%v)", out, err)
	}

	// And the typo the document form makes possible, named rather than dropped.
	bad := filepath.Join(t.TempDir(), "bad.tony")
	if err := os.WriteFile(bad, []byte("name: b\nroot: git\nulr: \"http://x:1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, "iod", "add", "--file", bad)
	if err == nil {
		t.Fatal("`ulr:` should be named, not dropped")
	}
	if !strings.Contains(err.Error(), "ulr") {
		t.Fatalf("the refusal should name the field, got: %v", err)
	}
}

// waitForCLI waits for a reflected entity to land, so a removal test is about the fork rather
// than about a race with the reflector's first pass.
func waitForCLI(t *testing.T, store entity.Store, ref entity.Ref) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok, _ := store.Get(ref); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", ref)
}
