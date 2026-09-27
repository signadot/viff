package versed

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// deployOrder is the charters a deployment installs, in the order deploy/repopulate.sh installs
// them. The order is part of what is tested: a charter may name a stratum the one before it
// declared.
var deployOrder = []string{
	"verse/repo/verse/comment-drift.tony",
	"signadot/repo/signadot/comment-drift.tony",
	"signadot/repo/signadot/pr.tony",
	"verse/workspace/Signadot/gate-ask.tony",
	"verse/workspace/Signadot/deploy-announce.tony",
	"verse/repo/verse/issue-triage.tony",
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
	listed := append([]string(nil), deployOrder...)
	sort.Strings(listed)
	sort.Strings(inTree)
	if strings.Join(listed, "\n") != strings.Join(inTree, "\n") {
		t.Fatalf("the charters in deploy/charters are not the ones installed here:\n in the tree: %v\n installed:   %v",
			inTree, listed)
	}

	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for _, name := range deployOrder {
		doc, err := os.ReadFile(filepath.Join("../deploy/charters", name))
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/charter", strings.NewReader(string(doc)))
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
