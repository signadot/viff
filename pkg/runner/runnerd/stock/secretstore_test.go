package stock

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// THE SECRET STORE IS A DIRECTORY THE FILE NAMES, absolute and below /, and under no directory
// every sandbox has: `secrets: {dir: /etc/verse/secrets}` would hand every task every secret.
func TestTheSecretStoresDirectoryIsWhereNoSandboxHasIt(t *testing.T) {
	write := func(body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "runner.tony")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for body, says := range map[string]string{
		"script: {}\nsecrets: {}\n":                              "secrets: `dir` names the directory",
		"script: {}\nsecrets: {dir: secrets}\n":                  "secrets.dir: \"secrets\" is not an absolute path below /",
		"script: {}\nsecrets: {dir: /}\n":                        "secrets.dir: \"/\" is not an absolute path below /",
		"script: {}\nsecrets: {dir: /etc/verse/secrets}\n":       "secrets.dir: /etc/verse/secrets is under a directory every task's sandbox has",
		"script: {}\nsecrets: {dir: /opt/secrets}\n":             "every task would read every secret",
		"script: {}\nsecrets: {dir: /usr/../etc/secrets}\n":      "every task would read every secret",
		"script: {}\nsecrets: {dir: /secrets, mode: \"0400\"}\n": "mode",
	} {
		if _, _, err := Load(write(body)); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%q: %v, want refused: %s", body, err, says)
		}
	}
	_, c, err := Load(write("script: {}\nsecrets: {dir: /secrets}\n"))
	if err != nil || c.secretsDir() != "/secrets" {
		t.Fatalf("a store at /secrets: %q, %v", c.secretsDir(), err)
	}
	if _, c, err := Load(write("script: {}\n")); err != nil || c.secretsDir() != "" {
		t.Errorf("no store: %q, %v", c.secretsDir(), err)
	}
}

// THE STORE IS ONE OF THE RUNNER'S OWN DIRECTORIES WHEN IT STARTS: apart from its work and its
// control directory, and reached by no `readOnly` path. A store that is not there yet, and a
// runner whose tasks are not confined, are each said and no refusal: the first is a mount that
// has not come, and the second is what `-sandbox none` is.
func TestARunnerStartsWithItsStoreKeptFromItsTasks(t *testing.T) {
	start := func(c Config, work, control string) (string, error) {
		var log bytes.Buffer
		_, err := Standalone(c, Secrets{}, Tokens{}, runnerd.Unconfined{}, []string{os.Args[0]}, work, control, nil, &log)
		return log.String(), err
	}
	base := t.TempDir()
	work, control, store := filepath.Join(base, "work"), filepath.Join(base, "control"), filepath.Join(base, "secrets")
	for _, d := range []string{work, control, store} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	with := func(dir string, readOnly ...string) Config {
		return Config{Script: &Script{}, SecretStore: &SecretStore{Dir: dir}, ReadOnly: readOnly}
	}
	for name, c := range map[string]struct {
		c    Config
		says string
	}{
		"in the work directory":      {with(filepath.Join(work, "secrets")), "secrets.dir: " + filepath.Join(work, "secrets") + " overlaps the runner's work directory"},
		"holding the control":        {with(base), "overlaps the runner's control directory"},
		"a readOnly path over it":    {with(store, base), "readOnly: " + base + " overlaps the runner's"},
		"a readOnly path within it":  {with(store, filepath.Join(store, "slack-bot")), "overlaps the runner's secrets directory"},
		"a readOnly path that is it": {with(store, store), "overlaps the runner's secrets directory"},
	} {
		if _, err := start(c.c, work, control); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want refused: %s", name, err, c.says)
		}
	}
	log, err := start(with(store, filepath.Join(base, "cache")), work, control)
	if err != nil {
		t.Fatalf("a store beside the runner's own: %v", err)
	}
	if !strings.Contains(log, "runs its tasks unconfined") || strings.Contains(log, "no secret is there") {
		t.Errorf("a store that is there, on an unconfined runner, said %q", log)
	}
	log, err = start(with(filepath.Join(base, "not-mounted")), work, control)
	if err != nil || !strings.Contains(log, "not-mounted") || !strings.Contains(log, "no secret is there while it is not") {
		t.Errorf("a store that is not there yet: %v, said %q", err, log)
	}
	if log, err := start(Config{Script: &Script{}}, work, control); err != nil || strings.Contains(log, "secrets") {
		t.Errorf("a runner with no store: %v, said %q", err, log)
	}
}

// NO TASK'S SANDBOX HAS THE STORE: a task's process, and whatever it runs, finds no directory
// there, though the runner reads it and they are the same user.
func TestATaskCannotReadTheSecretStore(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("no bwrap")
	}
	sb, err := runnerd.Bwrap()
	if err != nil {
		t.Skip(err)
	}
	base := t.TempDir()
	store, cache, work := filepath.Join(base, "secrets"), filepath.Join(base, "cache"), filepath.Join(base, "work")
	for _, d := range []string{store, cache, work} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(store, "slack-bot"), []byte("xoxb-a-secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runnerd.CheckSecretStore(store, runnerd.KeptDirs(work, "", "", "", "")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", "cat "+store+"/slack-bot; ls "+store+"; ls "+cache+" && echo the cache is here")
	if err := sb.Wrap(cmd, work, runnerd.Network{Mode: runnerd.NetworkNone}, []string{cache}, ""); err != nil {
		t.Fatal(err)
	}
	out, _ := cmd.CombinedOutput()
	if strings.Contains(string(out), "xoxb-a-secret") || strings.Contains(string(out), "slack-bot\n") {
		t.Fatalf("the task read the store: %s", out)
	}
	if !strings.Contains(string(out), "the cache is here") {
		t.Fatalf("the sandbox did not run as the test means it to: %s", out)
	}
}

// THE TABLE OF PERFORMED SYSTEMS IS HELD TO THE SYSTEMS' DOCUMENTS AND TO ITSELF: each entry is
// a system a document describes, named once; one that is closed without its credential has a
// variable to read it from, and one with a variable has where the runner keeps it; and no
// `shellEnv` may name a variable any of them reads.
func TestThePerformedSystemsAreOneTable(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range performedSystems {
		if _, ok := systems.Get(p.system); !ok {
			t.Errorf("%s: no system's document describes it", p.system)
		}
		if seen[p.system] {
			t.Errorf("%s is in the table twice", p.system)
		}
		seen[p.system] = true
		if p.when == nil {
			t.Errorf("%s: an entry says when the runner performs it", p.system)
		}
		if p.own && p.env == "" {
			t.Errorf("%s is closed without a credential, and names no variable to read one from", p.system)
		}
		if (p.env == "") != (p.token == nil) {
			t.Errorf("%s: a variable and where the runner keeps what it read go together", p.system)
		}
		if p.env != "" && !slices.Contains(credentials, p.env) {
			t.Errorf("%s: %s is not among the credentials no shellEnv may name", p.system, p.env)
		}
	}
	// What the table says is what the runner said before there was one.
	everything := Config{Repos: &Repos{Mirrors: "/m"}, reachesVerse: true, SecretStore: &SecretStore{Dir: "/secrets"},
		MCP: &MCP{Servers: map[string]MCPServer{"linear": {URL: "https://mcp.linear.app/mcp"}}}, Script: &Script{Systems: map[string]System{
			"slack": {Ceiling: systems.Open()}, "linear-issue": {Ceiling: systems.Open()}, "secret": {Ceiling: ceiling(t, `{name: npm-token}`)},
			"mcp": {Ceiling: ceiling(t, `{server: linear}`)}}}}
	if got := everything.performs(); !slices.Equal(got, []string{"git", "git-issue", "github", "github-issue", "linear-issue", "slack", "secret", "mcp", "verse"}) {
		t.Errorf("a runner that performs everything performs %v", got)
	}
	// A secret ceiling with no store to read from performs nothing.
	everything.SecretStore = nil
	if got := everything.performs(); slices.Contains(got, "secret") {
		t.Errorf("a runner with no store performs secret: %v", got)
	}
	if got := (Config{Script: &Script{}}).performs(); len(got) != 0 {
		t.Errorf("a runner with scripts alone performs %v", got)
	}
	local := Config{Repos: &Repos{Mirrors: "/m", Forge: "/srv/forge"}, Script: &Script{}}
	if got := local.performs(); !slices.Equal(got, []string{"git", "git-issue"}) {
		t.Errorf("a runner whose forge is a directory performs %v", got)
	}
	for system, want := range map[string]string{
		"git": "checks out repos", "git-issue": "checks out repos",
		"github": "whose forge is github.com", "github-issue": "whose forge is github.com",
		"slack": "performs none of its verbs", "network": "performs none of its verbs",
		"secret": "a secret store (`secrets.dir`) and a sandbox",
		"mcp":    "an `mcp.servers` section names the servers",
	} {
		if got := needsFor(system); !strings.Contains(got, want) {
			t.Errorf("needsFor(%s) = %q, want it to say %q", system, got, want)
		}
	}
	if !slices.Equal(credentials[:3], []string{EnvGitHubToken, EnvLinearAPIKey, EnvSlackBotToken}) {
		t.Errorf("the runner's own credentials are %v", credentials[:3])
	}
}

// A SYSTEM NAMES ITS SECRET IN ONE MAP, `secrets.systems`, and only a system whose credential is
// its own may: the forge's token is several systems', and is not the store's. The name is a
// name of the store's, and there is a store to name it in.
func TestASystemNamesItsSecretInOneMap(t *testing.T) {
	with := func(systems map[string]string) *Config {
		return &Config{Script: &Script{}, SecretStore: &SecretStore{Dir: "/secrets", Systems: systems}}
	}
	for says, c := range map[string]*Config{
		"secrets.systems.jira: the runner performs no system called that (those that may name a secret: linear-issue, slack)": with(map[string]string{"jira": "x"}),
		"secrets.systems.mcp: mcp names each server's secret with the server (`mcp.servers.<name>.auth.secret`)":              with(map[string]string{"mcp": "x"}),
		"secrets.systems.git: git acts with the forge's token (GITHUB_TOKEN), which is not the store's":                       with(map[string]string{"git": "git-token"}),
		"secrets.systems.github-issue: github-issue acts with the forge's token":                                              with(map[string]string{"github-issue": "t"}),
		"secrets.systems.verse: verse acts with no credential of its own to name":                                             with(map[string]string{"verse": "t"}),
		"secrets.systems.slack: \"SLACK_BOT_TOKEN\" is no secret's name":                                                      with(map[string]string{"slack": "SLACK_BOT_TOKEN"}),
		"secrets.systems.slack: \"../slack\" is no secret's name":                                                             with(map[string]string{"slack": "../slack"}),
		"secrets.systems.slack: a secret's name is not empty":                                                                 with(map[string]string{"slack": ""}),
		"secrets: `dir` names the directory":                                                                                  {Script: &Script{}, SecretStore: &SecretStore{Systems: map[string]string{"slack": "slack-bot"}}},
	} {
		if err := c.Check(); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%v: %v, want refused: %s", c.SecretStore, err, says)
		}
	}
	c := with(map[string]string{"slack": "slack-bot", "linear-issue": "linear"})
	if err := c.Check(); err != nil || c.secretOf("slack") != "slack-bot" || c.secretOf("git") != "" {
		t.Errorf("slack and linear-issue each naming a secret: %v", err)
	}
	if (Config{}).secretOf("slack") != "" {
		t.Error("a runner with no store has a secret for slack")
	}
}

// A SYSTEM THAT NAMES A SECRET IS HELD BY THE STORE, AND ITS VARIABLE IS NOT READ. Where the
// secret is not there (no file, or an empty one) the system is closed to every doer and the
// runner says which secret and where, as it does for a variable that is not set. Where it is
// there the system is offered, and a variable set beside it is said to be unread.
func TestASystemWhoseSecretIsAbsentIsClosed(t *testing.T) {
	store := t.TempDir()
	c := Config{
		Script:      &Script{Systems: map[string]System{"slack": {Ceiling: systems.Open()}, "linear-issue": {Ceiling: systems.Open()}}},
		SecretStore: &SecretStore{Dir: store, Systems: map[string]string{"slack": "slack-bot"}},
	}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	env := func(set ...string) func(string) string {
		return func(name string) string {
			if slices.Contains(set, name) {
				return "from-the-environment"
			}
			return ""
		}
	}
	put := func(value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(store, "slack-bot"), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Linear names no secret and reads its variable, as before; Slack's variable is set, and
	// does not open it.
	for _, name := range []string{"no file", "an empty file"} {
		if name == "an empty file" {
			put("")
		}
		got, warnings := c.Held(env(EnvLinearAPIKey, EnvSlackBotToken))
		if len(warnings) != 1 || !strings.Contains(warnings[0], "the secret slack-bot: ") || !strings.Contains(warnings[0], ", in "+store+": slack is closed to every doer, though script.systems.slack names it") {
			t.Errorf("%s: warnings %q", name, warnings)
		}
		if !slices.Equal(got.performs(), []string{"linear-issue"}) {
			t.Errorf("%s: performs %v, want slack closed and linear-issue as it was", name, got.performs())
		}
		for _, o := range got.Describe(nil).Offers {
			if _, ok := o.Systems["slack"]; ok {
				t.Errorf("%s: the offer %s holds slack", name, o.Name())
			}
		}
	}
	put("xoxb-in-the-store\n")
	got, warnings := c.Held(env(EnvLinearAPIKey))
	if len(warnings) != 0 || !slices.Equal(got.performs(), []string{"linear-issue", "slack"}) {
		t.Errorf("the secret there: warnings %q, performs %v", warnings, got.performs())
	}
	got, warnings = c.Held(env(EnvLinearAPIKey, EnvSlackBotToken))
	if len(warnings) != 1 || !strings.Contains(warnings[0], EnvSlackBotToken+" is set and is not read: slack acts with the secret slack-bot") ||
		!slices.Contains(got.performs(), "slack") {
		t.Errorf("the secret there and the variable set: warnings %q, performs %v", warnings, got.performs())
	}
	// What the runner acts with: the store's value at each act for slack, whatever its variable
	// holds, and the variable's for linear-issue.
	t.Setenv(EnvSlackBotToken, "xoxb-from-the-environment")
	t.Setenv(EnvLinearAPIKey, "lin-from-the-environment")
	tokens := TokensFromEnv(got)
	if tokens.Slack != "" || tokens.Linear != "lin-from-the-environment" {
		t.Errorf("tokens %+v, want slack's variable unread", tokens)
	}
	sv := got.services(tokens)
	if v, err := sv.SlackToken(); err != nil || v != "xoxb-in-the-store" {
		t.Errorf("slack acts with %q, %v", v, err)
	}
	put("xoxb-rotated")
	if v, err := sv.SlackToken(); err != nil || v != "xoxb-rotated" {
		t.Errorf("after a rotation slack acts with %q, %v", v, err)
	}
	if v, err := sv.LinearAuth(); err != nil || v != "lin-from-the-environment" {
		t.Errorf("linear-issue acts with %q, %v", v, err)
	}
	// And one that goes missing after the start fails the act, with the secret's name.
	if err := os.Remove(filepath.Join(store, "slack-bot")); err != nil {
		t.Fatal(err)
	}
	if v, err := sv.SlackToken(); err == nil || v != "" || err.Error() != "the secret slack-bot: it is not in the store" {
		t.Errorf("a secret gone since the start: %q, %v", v, err)
	}
}

// A SCRIPT POSTS TO SLACK WITH THE STORE'S TOKEN, which neither it nor its task's process is
// given: the runner reads the secret at the act, and the task's process, sent a configuration
// that names the secret, builds its tools and looks nothing up.
func TestAScriptPostsToSlackWithTheStoresToken(t *testing.T) {
	withCallTool(t)
	api := &slackAPI{}
	store := t.TempDir()
	if err := os.WriteFile(filepath.Join(store, "slack-bot"), []byte("xoxb-runners-own\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deploys := map[string][]systems.Shape{"post": {{"channel": ir.FromString("deploys")}}}
	c := every(Config{SecretStore: &SecretStore{Dir: store, Systems: map[string]string{"slack": "slack-bot"}}},
		map[string]System{"slack": {Ceiling: systems.Ceiling{{Verbs: deploys}}}})
	rt := serveConfigured(t, c, Secrets{}, Tokens{SlackAPI: api.serve(t)})
	env := envOf(netOpen, nil, nil)
	env.Systems["slack"] = []systems.Block{{Verbs: deploys}}
	rt.Launch(runner.Launch{ID: "s1", Environment: env, Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}},
		Script: "#!/bin/sh\nverse-call-tool slack-post -channel deploys -text 'v1.3 is rolling out'\necho \"exit=$?\"\nenv | grep -c xoxb || true"})
	snap := ended(t, rt, "s1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "posted in #deploys") || !strings.Contains(snap.Result, "exit=0") {
		t.Fatalf("task: %+v", snap)
	}
	if !strings.HasSuffix(strings.TrimSpace(snap.Result), "0") {
		t.Errorf("the script's environment carries the token:\n%s", snap.Result)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.posted) != 1 || api.posted[0]["auth"] != "Bearer xoxb-runners-own" {
		t.Errorf("Slack saw %+v, want one post with the store's token", api.posted)
	}
}
