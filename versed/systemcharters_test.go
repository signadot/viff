package versed

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/system/repocharters"
)

// A charter handed to the server as text is installed as the system once the sources are
// restored, since its outboxes send to places the iods read, or, refused then for want of them, as
// soon as the iods are registered: the repo-charters reconciler
// `verse up` renders from the config is one, and its rules are the system's, so they fire as the
// system and their asks reach whom the charter names.
func TestSystemChartersAreInstalledAsTheSystemAfterTheIOs(t *testing.T) {
	text, err := repocharters.Render(repocharters.Params{Repo: "r", GitHub: "acme/r", InstallAt: "repos.r", Answerers: []string{"user:*"}})
	if err != nil {
		t.Fatal(err)
	}
	srv := testServer(t)
	srv.SystemCharters = []SystemCharter{{Name: "repo-charters r", Text: text}}
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	// With no io registered, the text was refused at start and nothing stands: the outboxes
	// name places no io reads.
	if reconcilerStands(srv) {
		t.Fatal("the reconciler stood before any io was registered")
	}
	for _, io := range []map[string]any{
		{"name": "git", "root": "git", "url": "http://git-iod:9801", "owns": []any{map[string]any{"at": "git.r"}},
			"out": map[string]any{"places": []any{"git.r.out"}, "as": "agent:git-iod"}},
		{"name": "github", "root": "github", "url": "http://github-iod:9801", "owns": []any{map[string]any{"at": "github.acme.r"}},
			"out": map[string]any{"places": []any{"github.acme.r.out"}, "as": "agent:github-iod"}},
	} {
		resp := installIO(t, ts, "user:"+tester, io)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("installing the %s io = %s", io["name"], resp.Status)
		}
	}
	// Registering them is what it was waiting for: installed then, with no restart (a wipe, then
	// repopulate).
	if !reconcilerStands(srv) {
		t.Fatal("the rendered reconciler was not installed as a package once the iods stood")
	}
	for _, eng := range srv.packageEngines() {
		if _, ok := eng.Trigger("propose"); !ok {
			continue
		}
		for _, rule := range []string{"main", "propose", "install", "installed", "install-failed"} {
			if got := eng.OwnerOf(rule); got != entity.SystemCharter {
				t.Errorf("%s is %q's, want the system's", rule, got)
			}
		}
	}
}

// reconcilerStands reports whether the reconciler stands as a package.
func reconcilerStands(srv *Server) bool {
	for _, eng := range srv.packageEngines() {
		if _, ok := eng.Trigger("propose"); ok {
			return true
		}
	}
	return false
}
