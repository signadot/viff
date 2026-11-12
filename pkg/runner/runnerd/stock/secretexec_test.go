package stock

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd"
	"github.com/signadot/verse/pkg/runner/runnerd/claude"
	"github.com/signadot/verse/pkg/systems"
)

// THE SECRET SYSTEM IS PERFORMED WHERE THERE IS A STORE AND A CEILING NAMES IT, and each block
// of the ceiling names one secret of the store as a literal: a name is what a doer is trusted
// with, and the empty block would be every secret the store comes to hold. `as` may be a
// pattern or left out, and what is no variable's name is refused by the document. The system
// names no secret under `secrets.systems`: each block names its own.
func TestASecretCeilingNamesOneSecretAsALiteral(t *testing.T) {
	with := func(text string) *Config {
		return &Config{SecretStore: &SecretStore{Dir: "/secrets"},
			Script: &Script{Systems: map[string]System{"secret": {Ceiling: ceiling(t, text)}}}}
	}
	for says, c := range map[string]*Config{
		"script.systems.secret: this runner does not perform secret (it performs: nothing); a secret store (`secrets.dir`) and a sandbox": {
			Script: &Script{Systems: map[string]System{"secret": {Ceiling: ceiling(t, `[{name: npm-token}]`)}}}},
		"script.systems.secret.ceiling: names no secret; a secret ceiling block names the one secret":          with(`{as: NPM_TOKEN, exec: {}}`),
		"script.systems.secret.ceiling[1].name: !glob npm-* is a pattern, and a secret's name is a literal":    with(`[{name: npm-token}, {name: !glob "npm-*"}]`),
		"script.systems.secret.ceiling.name: !or [npm-token, pypi-token] is a pattern, and a secret's name is": with(`{name: !or [npm-token, pypi-token]}`),
		"script.systems.secret.ceiling[1].name: \"../etc\" is no secret's name":                                with(`[{name: npm-token}, {name: ../etc}]`),
		"script.systems.secret.ceiling.name: \"Npm-Token\": the field is lowercase":                            with(`{name: Npm-Token}`),
		"script.systems.secret.ceiling.as: \"npm-token\": the field is a variable's name":                      with(`{name: npm-token, as: npm-token}`),
		"script.systems.secret.ceiling.exec[0].env: LD_PRELOAD=1: a key is a variable's name":                  with(`{name: npm-token, exec: {env: {"LD_PRELOAD=1": null}}}`),
		"secrets.systems.secret: secret names its secret in each block of a doer's ceiling (`name`), not here": {
			SecretStore: &SecretStore{Dir: "/secrets", Systems: map[string]string{"secret": "npm-token"}}, Script: &Script{}},
	} {
		if err := c.Check(); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%v\n got: %v\nwant: %s", c.Script.Systems, err, says)
		}
	}
	for _, text := range []string{
		`{name: npm-token}`,
		`{name: npm-token, as: NPM_TOKEN, exec: {argv: !regexp "^npm publish ", dir: !glob "packages/*", env: {NODE_OPTIONS: null}}}`,
		`[{name: npm-token, as: !glob "NPM_*"}, {name: pypi-token, as: !or [PYPI_TOKEN, TWINE_PASSWORD]}]`,
	} {
		c := with(text)
		if err := c.Check(); err != nil {
			t.Errorf("%s: %v", text, err)
		}
		if got := c.performs(); !slices.Equal(got, []string{"secret"}) {
			t.Errorf("%s: performs %v", text, got)
		}
	}
}

// A BLOCK WHOSE SECRET IS NOT IN THE STORE AT THE START IS LEFT OUT of the doer's ceiling, and
// said, as a system is closed without its credential; a doer whose blocks are all left out has
// the system closed, and a runner none of whose doers keeps a block performs it no more. A doer
// whose secrets are there is not touched, and the configuration Held was given is not changed.
func TestASecretBlockWhoseSecretIsAbsentIsLeftOut(t *testing.T) {
	store := t.TempDir()
	put := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(store, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	none := func(string) string { return "" }
	c := Config{
		SecretStore: &SecretStore{Dir: store},
		Agents: map[string]Agent{claude.Agent: {Systems: map[string]System{
			"commands": {Ceiling: systems.Open()}, "files": {Ceiling: systems.Open()},
			"secret": {Ceiling: ceiling(t, `[{name: npm-token, as: NPM_TOKEN}, {name: pypi-token}]`)}}}},
		Script: &Script{Systems: map[string]System{"secret": {Ceiling: ceiling(t, `{name: npm-token}`)}}},
	}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	// Nothing in the store: every block is left out, and the system is closed everywhere.
	got, warnings := c.Held(none)
	if len(warnings) != 5 {
		t.Fatalf("warnings: %q", warnings)
	}
	for _, want := range []string{
		"the secret npm-token: it is not in the store, in " + store + ": agents.claude.systems.secret.ceiling[0] is left out; a spawn that names it is refused at install",
		"the secret pypi-token: it is not in the store, in " + store + ": agents.claude.systems.secret.ceiling[1] is left out",
		"secret is closed to claude: no secret agents.claude.systems.secret names is in the store",
		"the secret npm-token: it is not in the store, in " + store + ": script.systems.secret.ceiling is left out",
		"secret is closed to script: no secret script.systems.secret names is in the store",
	} {
		if !slices.ContainsFunc(warnings, func(w string) bool { return strings.Contains(w, want) }) {
			t.Errorf("no warning says %q:\n%s", want, strings.Join(warnings, "\n"))
		}
	}
	if len(got.performs()) != 0 {
		t.Errorf("performs %v", got.performs())
	}
	if _, ok := got.Agents[claude.Agent].Systems["secret"]; ok {
		t.Error("the agent's section still names secret")
	}
	if len(c.Agents[claude.Agent].Systems["secret"].Ceiling) != 2 || len(c.Script.Systems["secret"].Ceiling) != 1 {
		t.Error("Held changed the configuration it was given")
	}
	// pypi-token alone: the agent keeps that block alone, the script has the system closed. Its
	// offer holds the system still, as every doer's does for a performed system, with nothing
	// in it; the agent's holds the one block.
	put("pypi-token", "pypi-x")
	got, warnings = c.Held(none)
	if len(warnings) != 3 || !strings.Contains(warnings[0], "the secret npm-token: it is not in the store") ||
		!strings.Contains(warnings[2], "secret is closed to script") {
		t.Errorf("warnings: %q", warnings)
	}
	if kept := got.Agents[claude.Agent].Systems["secret"].Ceiling; len(kept) != 1 {
		t.Errorf("the agent keeps %d blocks", len(kept))
	} else if name, _ := secretNamed(kept[0]); name != "pypi-token" {
		t.Errorf("the agent keeps %s", name)
	}
	if _, ok := got.Script.Systems["secret"]; ok || !slices.Equal(got.performs(), []string{"secret"}) {
		t.Errorf("performs %v, script's section %v", got.performs(), got.Script.Systems["secret"])
	}
	for _, o := range got.Describe(nil).Offers {
		s, ok := o.Systems["secret"]
		switch {
		case !ok:
			t.Errorf("the offer %s does not hold secret", o.Name())
		case o.Name() == ScriptDoer && len(s.Ceiling) != 0:
			t.Errorf("the scripts' offer holds secret open: %v", s.Ceiling)
		case o.Name() == claude.Agent && len(s.Ceiling) != 1:
			t.Errorf("the agent's offer holds secret with %d blocks", len(s.Ceiling))
		}
	}
	// An empty file is an absent secret.
	put("npm-token", "")
	if _, warnings = c.Held(none); len(warnings) != 3 || !strings.Contains(warnings[0], "the secret npm-token: its file is empty") {
		t.Errorf("warnings: %q", warnings)
	}
	// Both there: nothing is said, and nothing changes.
	put("npm-token", "npm_x\n")
	if got, warnings = c.Held(none); len(warnings) != 0 || len(got.Agents[claude.Agent].Systems["secret"].Ceiling) != 2 ||
		len(got.Script.Systems["secret"].Ceiling) != 1 {
		t.Errorf("warnings %q, ceilings %v / %v", warnings, got.Agents[claude.Agent].Systems["secret"].Ceiling, got.Script.Systems["secret"].Ceiling)
	}
	for _, o := range got.Describe(nil).Offers {
		if s, ok := o.Systems["secret"]; !ok || len(s.Ceiling) == 0 {
			t.Errorf("the offer %s does not hold secret open: %v", o.Name(), s.Ceiling)
		}
	}
}

// confined stands in for a sandbox that confines, which this test does not need to build: Standalone
// reads its name alone.
type confined struct{ runnerd.Unconfined }

func (confined) Name() string { return "confined" }

// UNDER `-sandbox none` THE SYSTEM IS CLOSED WHOLE, whatever the store holds: a command run
// unconfined would run as the runner's user on the whole machine, the secret in its
// environment. Standalone closes it before the task's configuration is marshalled, and says so.
func TestSecretIsClosedWhereTasksRunUnconfined(t *testing.T) {
	store := t.TempDir()
	if err := os.WriteFile(filepath.Join(store, "npm-token"), []byte("npm_x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Config{SecretStore: &SecretStore{Dir: store},
		Script: &Script{Systems: map[string]System{"secret": {Ceiling: ceiling(t, `{name: npm-token}`)}}}}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	c, warnings := c.Held(func(string) string { return "" })
	if len(warnings) != 0 || !slices.Equal(c.performs(), []string{"secret"}) {
		t.Fatalf("held: %q, performs %v", warnings, c.performs())
	}
	// served is what the runner describes and what its task's process is sent, started with sb.
	served := func(sb runnerd.Sandbox) (string, runner.Description, taskConfig) {
		t.Helper()
		var log bytes.Buffer
		h, err := Standalone(c, Secrets{}, Tokens{}, sb, []string{os.Args[0]}, t.TempDir(), t.TempDir(), nil, &log)
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(h.HTTPHandler())
		defer srv.Close()
		resp, err := http.Get(srv.URL + "/describe")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var desc runner.Description
		if err := codec.Decode(resp.Body, &desc, codec.Tony); err != nil {
			t.Fatal(err)
		}
		var tc taskConfig
		if err := codec.Unmarshal(h.Config, &tc, codec.Tony); err != nil {
			t.Fatal(err)
		}
		return log.String(), desc, tc
	}
	log, desc, tc := served(runnerd.Unconfined{})
	if !strings.Contains(log, "-sandbox none: secret is closed to every doer, though script.systems.secret names it") {
		t.Errorf("the log: %s", log)
	}
	for _, o := range desc.Offers {
		if _, ok := o.Systems["secret"]; ok {
			t.Errorf("the offer %s holds secret", o.Name())
		}
	}
	// What the task's process is sent names the store's directory still (`secrets:`), and no
	// section for the system.
	if _, ok := tc.Config.Script.Systems["secret"]; ok || len(tc.Config.performs()) != 0 {
		t.Errorf("the task's configuration names secret:\n%+v", tc.Config)
	}
	// Confined, the system is offered and sent as written.
	log, desc, tc = served(confined{})
	if strings.Contains(log, "secret is closed") {
		t.Errorf("the log: %s", log)
	}
	if len(desc.Offers) != 1 || len(desc.Offers[0].Systems["secret"].Ceiling) != 1 {
		t.Errorf("the offers: %+v", desc.Offers)
	}
	if len(tc.Config.Script.Systems["secret"].Ceiling) != 1 || !slices.Equal(tc.Config.performs(), []string{"secret"}) {
		t.Errorf("the task's configuration: %+v", tc.Config)
	}
}
