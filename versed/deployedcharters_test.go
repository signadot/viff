package versed

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
)

// systemCharter is the charter a deployment has versed install at start as the system (deploy's
// `systemCharter`), before any of the rest.
const systemCharter = "verse/workspace/Signadot/gate-ask.tony"

// deployOrder is the charters a deployment installs, in the order deploy/repopulate.sh installs
// them: a file, or a directory whose files install as one step. The order is part of what is
// tested: a package may read what the one before it writes.
var deployOrder = []string{
	"verse/repo/verse/comment-drift",
	"signadot/repo/signadot/comment-drift.tony",
	"signadot/repo/signadot/pr.tony",
	"verse/workspace/Signadot/deploy-announce.tony",
	"verse/repo/verse/issue-next.tony",
}

// EVERY DEPLOYED CHARTER INSTALLS, into one verse, in the order a deployment installs them. A
// charter in the tree is what a newcomer copies and what a deployment runs, and a check added
// at install is a check these files have to pass: a file that stops installing is found here
// and not at the next ship.
func TestEveryDeployedCharterInstalls(t *testing.T) {
	// Every charter in the tree is in the order: one added beside these is installed here too.
	var inTree []string
	err := filepath.WalkDir("../deploy/charters", func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".tony") {
			inTree = append(inTree, strings.TrimPrefix(path, "../deploy/charters/"))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	listed := []string{systemCharter}
	for _, name := range deployOrder {
		files, err := charterFilesUnder(filepath.Join("../deploy/charters", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			listed = append(listed, strings.TrimPrefix(f, "../deploy/charters/"))
		}
	}
	sort.Strings(listed)
	sort.Strings(inTree)
	if strings.Join(listed, "\n") != strings.Join(inTree, "\n") {
		t.Fatalf("the charters in deploy/charters are not the ones installed here:\n in the tree: %v\n installed:   %v",
			inTree, listed)
	}

	// STARTED, as a deployment's verse is: the base charter is installed first, then the
	// deployment's system charter, and a deployed charter builds on what they declare (the
	// docket's stratum, `asks`, and the Slack place's, `gate-asked`).
	srv := testServer(t)
	srv.SystemCharter = filepath.Join("../deploy/charters", systemCharter)
	srv.Start(t.Context())
	for _, rule := range []string{"gate-ask", "gate-ask-person"} {
		if got := srv.Eng.OwnerOf(rule); got != entity.SystemCharter {
			t.Fatalf("%s is %q's, want the system's: installed as anybody else it does not see a person's ask", rule, got)
		}
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	// The Slack io, as repopulate installs it before the charters that send through it.
	withSlackIO(t, srv)
	for _, name := range deployOrder {
		files, err := charterFilesUnder(filepath.Join("../deploy/charters", name))
		if err != nil {
			t.Fatal(err)
		}
		// A directory is one request, as `verse c -a -d` sends it.
		var docs []string
		for _, f := range files {
			doc, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			docs = append(docs, string(doc))
		}
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/charter", strings.NewReader(strings.Join(docs, "\n---\n")))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "text/tony")
		resp, err := sendAsTester(req)
		if err != nil {
			t.Fatal(err)
		}
		body := readBody(t, resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("%s did not install: %s: %s", name, resp.Status, body)
		}
		// A deployed charter holds nothing: a rule waiting for a stratum that no deployed file
		// declares is a rule that never fires in production.
		if strings.Contains(body, "held:") {
			t.Errorf("%s installed with rules held:\n%s", name, body)
		}
	}
}

// charterFilesUnder is a deployed charter's files: the file itself, or every .tony file under a
// directory, in lexical order.
func charterFilesUnder(path string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".tony") {
			out = append(out, p)
		}
		return err
	})
	return out, err
}
