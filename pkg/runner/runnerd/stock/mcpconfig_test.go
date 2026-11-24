package stock

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd"
	"go.viff.dev/viff/pkg/systems"
)

// mcpRunner is a runner that calls linear, with the secret linear-mcp of a store at dir, and
// search, with none, its scripts' ceiling naming both.
func mcpRunner(t *testing.T, dir string) Config {
	t.Helper()
	return Config{SecretStore: &SecretStore{Dir: dir},
		MCP: &MCP{Servers: map[string]MCPServer{
			"linear": {URL: "https://mcp.linear.app/mcp", Auth: &MCPAuth{Secret: "linear-mcp"}, Timeout: "90s"},
			"search": {URL: "https://search.example.com/mcp"},
		}},
		Script: &Script{Systems: map[string]System{"mcp": {Ceiling: ceiling(t, `[{server: linear, call: {tool: !glob "get_*"}}, {server: search}]`)}}}}
}

// THE SERVERS ARE CHECKED AS THE CONFIGURATION IS LOADED: a name a tool's name could not end, a
// url the runner would not call or that would carry a key to the task's process, a credential
// with no store or in a header the client sets itself, a timeout past what a call may take, and a
// ceiling block that names no server the runner calls, or a pattern of them.
func TestTheMCPServersAreChecked(t *testing.T) {
	ok := mcpRunner(t, "/secrets")
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	if got := ok.performs(); !slices.Contains(got, "mcp") {
		t.Errorf("a runner naming servers and a ceiling naming mcp performs %v", got)
	}
	server := func(name string, s MCPServer) *Config {
		c := mcpRunner(t, "/secrets")
		c.MCP.Servers[name] = s
		return &c
	}
	withCeiling := func(text string) *Config {
		c := mcpRunner(t, "/secrets")
		c.Script.Systems["mcp"] = System{Ceiling: ceiling(t, text)}
		return &c
	}
	noStore := mcpRunner(t, "/secrets")
	noStore.SecretStore = nil
	noServers := mcpRunner(t, "/secrets")
	noServers.MCP = nil
	empty := mcpRunner(t, "/secrets")
	empty.MCP = &MCP{}
	for says, c := range map[string]*Config{
		"mcp.servers.lin-ear: a server's name is lowercase letters, digits and `_`":                               server("lin-ear", MCPServer{URL: "https://x.example/mcp"}),
		"mcp.servers.Linear: a server's name":                                                                     server("Linear", MCPServer{URL: "https://x.example/mcp"}),
		"mcp.servers.x.url: names the server's endpoint":                                                          server("x", MCPServer{}),
		`mcp.servers.x.url: "http://x.example/mcp" is not https`:                                                  server("x", MCPServer{URL: "http://x.example/mcp"}),
		`mcp.servers.x.url: "https://k:s@x.example/mcp" holds a user`:                                             server("x", MCPServer{URL: "https://k:s@x.example/mcp"}),
		`mcp.servers.x.url: "https://x.example/mcp?key=s" holds a query`:                                          server("x", MCPServer{URL: "https://x.example/mcp?key=s"}),
		`mcp.servers.x.url: "https://x.example/mcp#k" holds a fragment`:                                           server("x", MCPServer{URL: "https://x.example/mcp#k"}),
		`mcp.servers.x.url: "mailto:a@x.example" names no host`:                                                   server("x", MCPServer{URL: "mailto:a@x.example"}),
		"mcp.servers.x.auth.header: \"X Key\" is no header's name":                                                server("x", MCPServer{URL: "https://x.example/mcp", Auth: &MCPAuth{Secret: "k", Header: "X Key"}}),
		"mcp.servers.x.auth.header: Mcp-Session-Id is a header the MCP client sets itself":                        server("x", MCPServer{URL: "https://x.example/mcp", Auth: &MCPAuth{Secret: "k", Header: "Mcp-Session-Id"}}),
		"mcp.servers.x.auth.secret: \"../k\" is no secret's name":                                                 server("x", MCPServer{URL: "https://x.example/mcp", Auth: &MCPAuth{Secret: "../k"}}),
		"mcp.servers.x.auth.secret: a secret's name is not empty":                                                 server("x", MCPServer{URL: "https://x.example/mcp", Auth: &MCPAuth{}}),
		"mcp.servers.x.timeout: \"soon\" is no duration":                                                          server("x", MCPServer{URL: "https://x.example/mcp", Timeout: "soon"}),
		"mcp.servers.x.timeout: \"-1s\" is no duration":                                                           server("x", MCPServer{URL: "https://x.example/mcp", Timeout: "-1s"}),
		"mcp.servers.x.timeout: 1h0m0s is longer than a call may take (10m0s)":                                    server("x", MCPServer{URL: "https://x.example/mcp", Timeout: "1h"}),
		"mcp.servers.linear.auth.secret: a server's credential is a secret of the store, and the runner has none": &noStore,
		"mcp: `servers` names the servers the runner calls":                                                       &empty,
		"script.systems.mcp: this runner does not perform mcp (it performs: nothing); an `mcp.servers` section":   &noServers,
		"script.systems.mcp.ceiling: names no server":                                                             withCeiling(`{call: {}}`),
		"script.systems.mcp.ceiling.server: !glob * is a pattern":                                                 withCeiling(`{server: !glob "*"}`),
		"script.systems.mcp.ceiling.server: the runner calls no server github (it calls: linear, search)":         withCeiling(`{server: github}`),
	} {
		if err := c.Check(); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%v, want %q", err, says)
		}
	}
	// A loopback url may be http, for a test; a header of the server's own takes the value.
	for _, s := range []MCPServer{
		{URL: "http://127.0.0.1:8123/mcp"}, {URL: "http://localhost/mcp"}, {URL: "http://[::1]:80/mcp"},
		{URL: "https://mcp.signadot.com/stream", Auth: &MCPAuth{Secret: "signadot-api-key", Header: "signadot-api-key"}},
	} {
		if err := server("x", s).Check(); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
}

// A SERVER WHOSE SECRET IS NOT IN THE STORE IS LEFT OUT, of the servers the runner calls and of
// every ceiling, at the start: a doer left with no block has mcp closed, and the offer says so,
// and a server with no `auth` is never left out. The offer's `builds` is the servers the runner
// calls; a spawn naming another is refused at install as one this runner cannot build.
func TestAServerWithoutItsSecretIsLeftOut(t *testing.T) {
	dir := t.TempDir()
	c := mcpRunner(t, dir)
	held, warnings := c.Held(func(string) string { return "" })
	if got := held.mcpServers(); !slices.Equal(got, []string{"search"}) {
		t.Errorf("held, the runner calls %v, want search alone", got)
	}
	if got := held.Script.Systems["mcp"].Ceiling; len(got) != 1 || got[0].Attributes["server"].String != "search" {
		t.Errorf("held, the ceiling is %v, want search's block alone", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "mcp.servers.linear is left out") || !strings.Contains(warnings[0], "linear-mcp") {
		t.Errorf("the warnings: %q", warnings)
	}
	if got := c.mcpServers(); len(got) != 2 {
		t.Errorf("Held changed the configuration it was given: %v", got)
	}
	// With the secret there, nothing is left out, and the value is not read into the warnings.
	if err := os.WriteFile(filepath.Join(dir, "linear-mcp"), []byte("lin_api_s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	held, warnings = c.Held(func(string) string { return "" })
	if len(held.mcpServers()) != 2 || len(warnings) != 0 {
		t.Errorf("with the secret: %v, %q", held.mcpServers(), warnings)
	}
	// A doer whose blocks all name a server left out has mcp closed.
	os.Remove(filepath.Join(dir, "linear-mcp"))
	alone := mcpRunner(t, dir)
	alone.Script.Systems["mcp"] = System{Ceiling: ceiling(t, `{server: linear}`)}
	held, warnings = alone.Held(func(string) string { return "" })
	if _, ok := held.Script.Systems["mcp"]; ok || !strings.Contains(strings.Join(warnings, "\n"), "mcp is closed to script") {
		t.Errorf("a doer left with no block: %v, %q", held.Script.Systems, warnings)
	}
	if slices.Contains(held.performs(), "mcp") {
		t.Errorf("a runner whose doers name no server left performs mcp: %v", held.performs())
	}

	// The offer: the servers under builds, the doer's ceiling as written.
	if err := os.WriteFile(filepath.Join(dir, "linear-mcp"), []byte("lin_api_s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	held, _ = c.Held(func(string) string { return "" })
	d := held.Describe([]string{runner.NetworkNone})
	if err := d.Check(); err != nil {
		t.Fatal(err)
	}
	var o runner.Offer
	for _, offer := range d.Offers {
		if offer.Script {
			o = offer
		}
	}
	mcp, ok := o.Systems["mcp"]
	if !ok || len(mcp.Builds) != 1 || systems.Words(mcp.Builds[0].Attributes["server"]) != "!or [linear, search]" || len(mcp.Ceiling) != 2 {
		t.Fatalf("the offer's mcp: %+v", mcp)
	}
	// A spawn's block for a server the runner calls is taken; one for another is refused at install.
	for server, refused := range map[string]bool{"search": false, "github": true} {
		env := systems.Environment{Systems: systems.Systems{"mcp": {{Attributes: map[string]string{"server": server},
			Verbs: map[string][]systems.Shape{"call": {{}}}}}}}
		err := runnerd.CheckOffer(&runner.Launch{ID: "m1", Script: "#!/bin/sh\n", Environment: env, Autonomy: allowing()}, d)
		if refused != (err != nil) || refused && !strings.Contains(err.Error(), "environment.systems.mcp") {
			t.Errorf("a spawn calling %s: %v", server, err)
		}
	}
}
