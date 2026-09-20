package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/signadot/verse/source"
	"github.com/signadot/verse/source/sourced"
)

// a source face an operator can actually install: the reflection contract, served by the same
// sourced handler verse's own in-process source would be.
func sourceFace(t *testing.T, repos ...string) *httptest.Server {
	t.Helper()
	g := source.NewGit()
	for _, id := range repos {
		dir := t.TempDir()
		initRepo(t, dir)
		g.AddRepo(id, dir)
	}
	srv := httptest.NewServer(sourced.New(g).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "Tester"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// bodyOf reads a response body for a message assertion — a refusal that does not SAY what to
// do is a refusal an operator cannot act on, so the messages are asserted rather than the codes
// alone.
func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// An operator can install one, list it, and read it back — and the install PROBED, which is the
// half that only happens on the way through the server.
func TestSourceAddListShow(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	face := sourceFace(t, "verse")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := postTony(t, ts.URL+"/sources", map[string]any{
		"root": "git", "name": "laptop", "url": face.URL,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s, want 201", resp.Status)
	}
	var got map[string]any
	decodeResp(t, resp, &got)
	if got["at"] != "verse.sources.laptop" {
		t.Fatalf("at = %v, want verse.sources.laptop", got["at"])
	}
	if got["reach"] != "a source" {
		t.Fatalf("reach = %v — a live source face read as something else", got["reach"])
	}

	// The ownership claim the operator did not write, taken from what the source SAID. Without
	// it `source add <ref> <url>` would install a registration that claims nothing, and the
	// next install in this system would be refused for a reason belonging to this one.
	sr, err := ts.Client().Get(ts.URL + "/sources/laptop")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	var one map[string]any
	decodeResp(t, sr, &one)
	owns, _ := one["owns"].([]any)
	first, _ := owns[0].(map[string]any)
	if len(owns) != 1 || first["at"] != "git.verse" {
		t.Fatalf("show = %+v, want the ownership the probe read", one)
	}

	lr, err := ts.Client().Get(ts.URL + "/sources")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var list []map[string]any
	decodeResp(t, lr, &list)
	if len(list) != 1 || list[0]["at"] != "verse.sources.laptop" || list[0]["url"] != face.URL {
		t.Fatalf("list = %+v, want the one installed source with its url", list)
	}
}

// The check with no runner analogue, and the one that is unrecoverable later: a registration
// pointed at a source that reflects something else would write the wrong slice forever, from a
// source that thinks it is right.
func TestSourceAddRefusesASystemMismatch(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	face := sourceFace(t, "verse")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := postTony(t, ts.URL+"/sources", map[string]any{
		"root": "github", "name": "laptop", "url": face.URL,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a github registration on a git source = %s, want 400", resp.Status)
	}
	if body := bodyOf(t, resp); !strings.Contains(body, "github") || !strings.Contains(body, "git") {
		t.Fatalf("the refusal should name both systems, got: %s", body)
	}
}

// Single-writer is what makes a reflected slice need no CAS. Two sources over different repos
// are provably apart and install alongside each other; two over the same one are refused until
// the operator says they know.
func TestSourceAddProvesDisjointness(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	verse := sourceFace(t, "verse")
	kernel := sourceFace(t, "kernel")
	again := sourceFace(t, "verse")

	if resp := postTony(t, ts.URL+"/sources", map[string]any{
		"root": "git", "name": "a", "url": verse.URL,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first install = %s", resp.Status)
	}
	// Different repos: provably disjoint, so no flag. This is the case the whole rule exists
	// for — if it needed --allow-overlap the flag would be the ordinary path and mark nothing.
	if resp := postTony(t, ts.URL+"/sources", map[string]any{
		"root": "git", "name": "b", "url": kernel.URL,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("two sources over different repos should install: %s", resp.Status)
	}
	// The same repo, twice: refused, with both claims in the message, because the operator is
	// the one who can tell whether they really are apart.
	resp := postTony(t, ts.URL+"/sources", map[string]any{
		"root": "git", "name": "c", "url": again.URL,
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("two sources over one repo = %s, want 409", resp.Status)
	}
	body := bodyOf(t, resp)
	for _, want := range []string{"git.verse", "verse.sources.a", "--allow-overlap"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the refusal should mention %q, got: %s", want, body)
		}
	}
	// …and the way past it, with the cost stated rather than discovered.
	if resp := postTony(t, ts.URL+"/sources?allow-overlap=true", map[string]any{
		"root": "git", "name": "c", "url": again.URL,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("--allow-overlap should install anyway: %s", resp.Status)
	}
}

// A source that claims nothing can be proved apart from nothing, so it is refused beside
// anything in its system rather than having ownership guessed for it.
func TestASourceThatClaimsNothingIsRefused(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	face := sourceFace(t, "verse")
	if resp := postTony(t, ts.URL+"/sources", map[string]any{
		"root": "git", "name": "a", "url": face.URL,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first install = %s", resp.Status)
	}
	// --no-probe means nothing asked it what it owns, so the registration carries no claim.
	resp := postTony(t, ts.URL+"/sources?probe=false", map[string]any{
		"root": "git", "name": "b", "url": "http://nowhere:9801",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a source claiming nothing = %s, want 409", resp.Status)
	}
	if body := bodyOf(t, resp); !strings.Contains(body, "claimed no ownership") {
		t.Fatalf("the refusal should say WHY it could prove nothing, got: %s", body)
	}
}

// The other two probe dispositions, and they differ on whether waiting could change the answer.
func TestSourceAddProbeDispositions(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Something that answers and is not a source: refused, since waiting will not fix it.
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	if resp := postTony(t, ts.URL+"/sources", map[string]any{
		"root": "git", "name": "a", "url": other.URL,
	}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a non-source = %s, want 400", resp.Status)
	}

	// Nothing there at all: INSTALLED, with a warning. A manifest landing before its pod is the
	// ordinary state of a source deployed alongside the entity that names it, so refusing would
	// make deployment order-dependent for a fact that fixes itself.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	resp := postTony(t, ts.URL+"/sources", map[string]any{
		"root": "git", "name": "b", "url": dead.URL,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("an unreachable source = %s, want 201 with a warning", resp.Status)
	}
	var got map[string]any
	decodeResp(t, resp, &got)
	if got["reach"] != "unreachable" || got["warning"] == nil {
		t.Fatalf("an unreachable install should say so on the response: %+v", got)
	}
}

// `dir` is described WITHOUT a network, by building the source and asking it — the same
// question sourced answers over HTTP, put to the same value. That is what keeps it first-class
// rather than a path with fewer checks than the others.
func TestADirSourceIsDescribedInTask(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	dir := t.TempDir()
	initRepo(t, dir)
	resp := postTony(t, ts.URL+"/sources", map[string]any{
		"root": "git", "name": "local", "dir": dir,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a dir source = %s, want 201: %s", resp.Status, bodyOf(t, resp))
	}
	var got map[string]any
	decodeResp(t, resp, &got)
	if got["reach"] != "a source" {
		t.Fatalf("a dir source should describe itself in task, got %+v", got)
	}
	// And it claims what it will write, with no probe and no url.
	sr, _ := ts.Client().Get(ts.URL + "/sources/local")
	var one map[string]any
	decodeResp(t, sr, &one)
	if owns, _ := one["owns"].([]any); len(owns) != 1 {
		t.Fatalf("a dir source should claim its repo: %+v", one)
	}
}
