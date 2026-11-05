package versed

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd"
	"github.com/signadot/verse/pkg/runner/runnerd/stock"
	"github.com/signadot/verse/pkg/systems/systemstest"
	"github.com/signadot/verse/pkg/task"
)

// systemCharter is the charter a deployment has versed install at start as the system (deploy's
// `systemCharter`), before any of the rest: the example a deployment copies.
const systemCharter = "../../docs/reference/examples/gate-ask/gate-ask.tony"

// deployOrder is the example charters a deployment installs after the system's, in the order it
// installs them: a file, or a directory whose files install as one step. The order is part of
// what is tested: a package may read what the one before it writes.
var deployOrder = []string{
	"../../docs/reference/examples/deploy-announce/deploy-announce.tony",
}

// renderedExamples is the example charter versed renders itself from its config rather than a
// deployment copying it (internal/repocharters, TestTheReferenceExampleIsTheRenderedTemplate), so
// it is not installed here: installed beside the one versed renders it would be a second reconciler.
var renderedExamples = []string{
	"../../docs/reference/examples/repo-charters/repo-charters.tony",
}

// repoCharters is the repository's own charters, and repoChartersAt where this test's deployment
// has the git io install them (a deployment's auth file names its own): the whole
// directory as one request with a prune, as `verse c -a <address> -d <dir> --prune` sends it.
const (
	repoCharters   = "../../.verse/charters"
	repoChartersAt = "repos.verse"
)

// EVERY DEPLOYED CHARTER INSTALLS, into one verse, in the order a deployment installs them: the
// examples a deployment copies, then the repository's own charters, which the git io installs at
// repos.verse when the reconciler asks. A charter in the tree is what a newcomer copies and what
// a deployment runs, and a check added at install is a check these files have to pass: a file
// that stops installing is found here and not at the next ship, or at the next change to the
// charters on main.
func TestEveryDeployedCharterInstalls(t *testing.T) {
	// Every example charter in the tree is in the order, or named as versed's to render: one added
	// beside these is installed here too, or says why not, and the examples README's promise that
	// each is tested holds.
	var inTree []string
	err := filepath.WalkDir("../../docs/reference/examples", func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".tony") {
			inTree = append(inTree, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	listed := []string{systemCharter}
	for _, name := range deployOrder {
		files, err := charterFilesUnder(name)
		if err != nil {
			t.Fatal(err)
		}
		listed = append(listed, files...)
	}
	listed = append(listed, renderedExamples...)
	sort.Strings(listed)
	sort.Strings(inTree)
	if strings.Join(listed, "\n") != strings.Join(inTree, "\n") {
		t.Fatalf("the example charters are not the ones installed here:\n in the tree: %v\n installed:   %v", inTree, listed)
	}

	// STARTED, as a deployment's verse is: the base charter is installed first, then the
	// deployment's system charter, and a deployed charter builds on what they declare (the
	// docket's stratum, `asks`, and the Slack place's, `gate-asked`).
	srv := testServer(t)
	srv.SystemCharter = systemCharter
	srv.Start(t.Context())
	for _, rule := range []string{"gate-ask", "gate-ask-person"} {
		if got := srv.Eng.OwnerOf(rule); got != entity.SystemCharter {
			t.Fatalf("%s is %q's, want the system's: installed as anybody else it does not see a person's ask", rule, got)
		}
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	// The Slack io, as repopulate installs it before the charters that send through it, and
	// the runner the charters spawn on, which repopulate installs first for the same reason.
	withSlackIO(t, srv)
	registerDeployed(t, ts, "verse-runner", "../../deploy/verse-runner/verse-runner.yaml")
	for _, name := range deployOrder {
		files, err := charterFilesUnder(name)
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

	// The repository's charters, as the git io's out half sends them: every file in the
	// directory in one request, pruning the address, so what stands at repos.verse afterwards is
	// exactly the tree.
	files, err := charterFilesUnder(repoCharters)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no charters under %s", repoCharters)
	}
	var docs []string
	for _, f := range files {
		doc, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, string(doc))
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/charter?prune="+repoChartersAt, strings.NewReader(strings.Join(docs, "\n---\n")))
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
		t.Fatalf("%s did not install at %s: %s: %s", repoCharters, repoChartersAt, resp.Status, body)
	}
	if strings.Contains(body, "held:") {
		t.Errorf("%s installed with rules held:\n%s", repoCharters, body)
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

// registerDeployed registers, under name, a runner offering what a deployed runner offers: its
// offers built from its config under deploy/, as it describes itself
// and a sandbox building every network. So a deployed charter is held at install to the ceilings
// the deployment runs, and a ceiling that would refuse one is found here.
func registerDeployed(t *testing.T, ts *httptest.Server, name, config string) {
	t.Helper()
	cfg := deployedConfig(t, config)
	desc := cfg.Describe([]string{runner.NetworkNone, runner.NetworkOpen, runner.NetworkAllow})
	release := make(chan struct{})
	build := func(l runner.Launch, _ *task.Registry) *task.Task {
		sup := systemstest.Opened(task.NewSupervisor(l.Environment, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{}), l.Environment)
		return task.NewTask(l.ID, sup, &blockedDriver{release: release}, okExecutor{})
	}
	h := runnerd.NewInProcessHandler(build)
	h.Describe(desc)
	face := provenFace(t, ts.URL, h.HTTPHandler(), func() { close(release) })
	if resp := postTony(t, ts.URL+"/runners", map[string]any{"name": name, "url": face.URL, "subject": face.Subject}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("runner add %s = %s", name, resp.Status)
	}
}

// deployedConfig is a deployed runner's configuration, as its manifest's ConfigMap carries it
// (`verse-runner.tony: |`), loaded and checked as the runner loads it at start.
func deployedConfig(t *testing.T, manifest string) stock.Config {
	t.Helper()
	text, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(text), "verse-runner.tony: |\n")
	if !ok {
		t.Fatalf("%s has no verse-runner.tony", manifest)
	}
	var lines []string
	for _, line := range strings.Split(after, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "    ") {
			break
		}
		lines = append(lines, strings.TrimPrefix(line, "    "))
	}
	path := filepath.Join(t.TempDir(), "verse-runner.tony")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	_, cfg, err := stock.Load(path)
	if err != nil {
		t.Fatalf("%s: the runner would not start: %v", manifest, err)
	}
	return cfg
}

// EVERY DEPLOYED RUNNER'S CONFIGURATION LOADS, its ceilings checked as the runner checks them at
// start, so one it would refuse is found here and not at the deploy.
func TestEveryDeployedRunnerConfigLoads(t *testing.T) {
	for _, manifest := range []string{"../../deploy/verse-runner/verse-runner.yaml", "../../deploy/codex-runner/codex-runner.yaml", "../../deploy/claude-runner/claude-runner.yaml"} {
		cfg := deployedConfig(t, manifest)
		for _, o := range cfg.Describe([]string{runner.NetworkOpen}).Offers {
			if len(o.Systems["git"].Ceiling) == 0 {
				t.Errorf("%s: %s has no git ceiling, so its tasks reach no repository", manifest, o.Name())
			}
		}
	}
}
