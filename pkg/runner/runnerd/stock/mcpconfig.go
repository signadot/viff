package stock

import (
	"fmt"
	"maps"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/runner/runnerd/mcpclient"
	"go.viff.dev/viff/pkg/runner/runnerd/secretstore"
	"go.viff.dev/viff/pkg/systems"
)

// MCP is the remote MCP servers the runner calls for its tasks (`mcp:`), by name: the runner is
// the client, holds each server's credential, and makes each call, and a task never reaches a
// server. A doer's `mcp` ceiling names the servers its tasks may call, one to a block.
type MCP struct {
	Servers map[string]MCPServer `tony:"field=servers"`
}

// MCPServer is one server the runner calls.
type MCPServer struct {
	// URL is the server's streamable HTTP endpoint: `https`, or `http` on a loopback host for a
	// test. It holds no user and no query: the task's process is sent the configuration, and a
	// key in a url would reach it. A key goes in Auth.
	URL string `tony:"field=url"`
	// Auth is how the runner signs a request to the server; absent, it sends no credential.
	Auth *MCPAuth `tony:"field=auth,omitzero"`
	// Timeout is how long one call has, a duration (`90s`); absent is mcpclient.Timeout, and no
	// more than mcpclient.TimeoutMost.
	Timeout string `tony:"field=timeout,omitzero"`
}

// MCPAuth is the credential a server's requests carry: a secret of the runner's store, read at
// each request, so a rotation holds for the next call of a running task.
type MCPAuth struct {
	// Secret is the secret's name in the store (`secrets.dir`).
	Secret string `tony:"field=secret"`
	// Header is the header the value goes in, as it is; absent is `Authorization: Bearer <value>`.
	Header string `tony:"field=header,omitzero"`
}

// serverName is what a server may be called: lowercase letters, digits and `_`, a letter first,
// and no `-`, so the first `-` after `mcp-` in a tool's name ends the server's and no two tools
// of a task share a name.
var serverName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// headerName is a header's name as HTTP spells one (a token).
var headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// transportHeaders is the headers the MCP client sets itself, which a credential may not take.
var transportHeaders = []string{"accept", "connection", "content-length", "content-type", "host", "last-event-id",
	"mcp-protocol-version", "mcp-session-id", "transfer-encoding"}

// mcpServers is the servers the runner calls, by name, in order; none where it has no `mcp:`.
func (c Config) mcpServers() []string {
	if c.MCP == nil {
		return nil
	}
	return slices.Sorted(maps.Keys(c.MCP.Servers))
}

// hasServers is mcp performed: where the runner names servers and a doer's ceiling names the
// system.
func hasServers(c Config) bool { return len(c.mcpServers()) > 0 && len(c.naming("mcp")) > 0 }

// timeout is how long one call of the server has.
func (s MCPServer) timeout() time.Duration {
	if d, err := time.ParseDuration(s.Timeout); err == nil && s.Timeout != "" {
		return d
	}
	return mcpclient.Timeout
}

// checkMCP checks the `mcp:` section: each server's name, its url, its credential and its timeout.
func (c *Config) checkMCP() error {
	if c.MCP == nil {
		return nil
	}
	if len(c.MCP.Servers) == 0 {
		return fmt.Errorf("mcp: `servers` names the servers the runner calls; with none, leave `mcp:` out")
	}
	for _, name := range c.mcpServers() {
		s, at := c.MCP.Servers[name], "mcp.servers."+name
		if !serverName.MatchString(name) {
			return fmt.Errorf("%s: a server's name is lowercase letters, digits and `_`, a letter first and no `-`: "+
				"a tool is called mcp-<server>-<tool>, and the first `-` after mcp- ends the server's name", at)
		}
		if err := checkServerURL(s.URL); err != nil {
			return fmt.Errorf("%s.url: %w", at, err)
		}
		if a := s.Auth; a != nil {
			switch {
			case c.secretsDir() == "":
				return fmt.Errorf("%s.auth.secret: a server's credential is a secret of the store, and the runner has none (`secrets.dir`)", at)
			case a.Header != "" && !headerName.MatchString(a.Header):
				return fmt.Errorf("%s.auth.header: %q is no header's name", at, a.Header)
			case slices.Contains(transportHeaders, strings.ToLower(a.Header)):
				return fmt.Errorf("%s.auth.header: %s is a header the MCP client sets itself", at, a.Header)
			}
			if err := secretstore.CheckName(a.Secret); err != nil {
				return fmt.Errorf("%s.auth.secret: %w", at, err)
			}
		}
		if s.Timeout != "" {
			d, err := time.ParseDuration(s.Timeout)
			switch {
			case err != nil || d <= 0:
				return fmt.Errorf("%s.timeout: %q is no duration (`90s`, `5m`)", at, s.Timeout)
			case d > mcpclient.TimeoutMost:
				return fmt.Errorf("%s.timeout: %s is longer than a call may take (%s)", at, d, mcpclient.TimeoutMost)
			}
		}
	}
	return nil
}

// checkServerURL refuses a url the runner would not call: one that is not `https` (or `http` on a
// loopback host, for a test), names no host, or holds a user, a query or a fragment.
func checkServerURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case raw == "":
		return fmt.Errorf("names the server's endpoint (https://mcp.example.com/mcp)")
	case err != nil:
		return fmt.Errorf("%q does not read as a url: %w", raw, err)
	case u.User != nil:
		return fmt.Errorf("%q holds a user: the task's process is sent the configuration, and a key goes in `auth`", raw)
	case u.RawQuery != "" || u.ForceQuery:
		return fmt.Errorf("%q holds a query: the task's process is sent the configuration, and a key goes in `auth`", raw)
	case u.Fragment != "":
		return fmt.Errorf("%q holds a fragment", raw)
	case u.Host == "" || u.Opaque != "":
		return fmt.Errorf("%q names no host", raw)
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && loopback(u.Hostname()):
		return nil
	}
	return fmt.Errorf("%q is not https (http is taken on a loopback host alone, for a test)", raw)
}

// loopback is whether a host is this machine's alone.
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkMCPCeiling refuses an `mcp` ceiling block that does not name one server the runner calls,
// as a literal: a server is what a doer is trusted with, and the empty block would be every server
// the configuration comes to name.
func (c *Config) checkMCPCeiling(at string, ceiling systems.Ceiling) error {
	for i, b := range ceiling {
		at := at + ".mcp.ceiling" + blockAt(ceiling, i)
		name, ok := serverNamed(b)
		switch {
		case b.Attributes["server"] == nil:
			return fmt.Errorf("%s: names no server; an mcp ceiling block names the one server it is for (`server`)", at)
		case !ok:
			return fmt.Errorf("%s.server: %s is a pattern, and a server's name is a literal, one to a block: a server is what a doer is trusted with",
				at, systems.Words(b.Attributes["server"]))
		case !slices.Contains(c.mcpServers(), name):
			return fmt.Errorf("%s.server: the runner calls no server %s (it calls: %s)", at, name, strings.Join(c.mcpServers(), ", "))
		}
	}
	return nil
}

// serverNamed is the one server an `mcp` ceiling block names, false where it names none, a
// pattern, or several.
func serverNamed(b systems.CeilingBlock) (string, bool) {
	names, ok := systems.Literals(b.Attributes["server"])
	if !ok || len(names) != 1 {
		return "", false
	}
	return names[0], true
}

// heldMCP is c with each server whose secret is not in the store left out, and each doer's ceiling
// block naming one left out with it, the system closed to a doer left with no block; and what it
// closed. A server with no `auth` is never left out. What is closed stays closed until a restart,
// though a value that changes is the next request's: what is offered is fixed at the start.
func (c Config) heldMCP() (Config, []string) {
	if c.MCP == nil {
		return c, nil
	}
	var warnings []string
	servers := map[string]MCPServer{}
	store := secretstore.Open(c.secretsDir())
	for _, name := range c.mcpServers() {
		s := c.MCP.Servers[name]
		if s.Auth != nil {
			if _, err := store.Get(s.Auth.Secret); err != nil {
				warnings = append(warnings, fmt.Sprintf("%v, in %s: mcp.servers.%s is left out, and every ceiling block naming it; a spawn that names it is refused at install",
					err, c.secretsDir(), name))
				continue
			}
		}
		servers[name] = s
	}
	if len(servers) == len(c.MCP.Servers) {
		return c, warnings
	}
	c.MCP = &MCP{Servers: servers}
	for _, doer := range c.doers() {
		sys, ok := c.systemsOf(doer)["mcp"]
		if !ok {
			continue
		}
		var kept systems.Ceiling
		for _, b := range sys.Ceiling {
			if name, _ := serverNamed(b); slices.Contains(c.mcpServers(), name) {
				kept = append(kept, b)
			}
		}
		switch {
		case len(kept) == len(sys.Ceiling):
		case len(kept) == 0:
			c = c.withSection(doer, "mcp", nil)
			warnings = append(warnings, fmt.Sprintf("mcp is closed to %s: no server %s.systems.mcp names is left", doer, sectionOf(doer)))
		default:
			c = c.withSection(doer, "mcp", &System{Ceiling: kept})
		}
	}
	return c, warnings
}

// mcpClientServers is the servers the runner calls, as the client takes them: each with its
// credential's name and its timeout, Held having left out any whose secret is not there.
func (c Config) mcpClientServers() mcpclient.Servers {
	if c.MCP == nil {
		return nil
	}
	out := mcpclient.Servers{}
	for name, s := range c.MCP.Servers {
		cs := mcpclient.Server{URL: s.URL, Timeout: s.timeout()}
		if s.Auth != nil {
			cs.Secret, cs.Header = s.Auth.Secret, s.Auth.Header
		}
		out[name] = cs
	}
	return out
}

// mcpBuilds is what the runner builds of mcp, by its nature: a call of the servers it names, as
// an offer's `builds` says it.
func (c Config) mcpBuilds() systems.Ceiling {
	return systems.Ceiling{{Attributes: map[string]*ir.Node{"server": systems.OneOf(c.mcpServers()...)},
		Verbs: map[string][]systems.Shape{"call": {{}}}}}
}
