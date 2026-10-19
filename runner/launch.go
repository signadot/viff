package runner

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/task"
)

// Launch is the body of `POST /tasks`: the work verse asks a runner to start, in the spawn
// spec's shape. It is the spec's spawn with verse's references resolved: verse sends it as is
// and a runner reads it as is.
//
// A runner checks it strictly: an unknown key, a doer that is both or neither of an agent and a
// script, or a value its offer does not take is refused with `400` and the reason, and so is
// anything it cannot honour yet. A refused launch is a failed spawn, never a task run without
// what it asked for.
//
//tony:schemagen=verse-runner-launch,notag
type Launch struct {
	// ID is the task's id. Verse mints it, so that it matches the task's record.
	ID string `tony:"field=id"`
	// Doer is who does the work and what it runs with.
	Doer Doer `tony:"field=doer"`
	// Prompt is the job, in text. Required with an agent; a script takes none (its input is
	// with.env).
	Prompt string `tony:"field=prompt,omitzero"`
	// Policy is what the task is given and what happens to each step it takes (task.Policy): a
	// verb no row names is not the task's, the first row that matches a step denies it, asks a
	// person first, or lets it go, and the last row, which names no step, decides the calls of
	// named verbs no row above matched. Absent gives the task no verb of any system.
	Policy task.Policy `tony:"field=policy,omitzero"`
	// Answer is the shape the result must have, as a tony match pattern. Absent, the result is
	// the agent's final message, or the script's output, as text.
	Answer *ir.Node `tony:"field=answer,omitzero"`
	// Timeout is how long the task may run, as a duration ("1h"). Time spent waiting for an
	// answer doesn't count; time paused does. Absent is no limit.
	Timeout string `tony:"field=timeout,omitzero"`
	// Converse makes the run a conversation: when the agent goes quiet the person is handed the
	// turn, and the run ends when they end it. Set only by verse (`verse task run --attach`), and
	// only for an offer whose `converse` is true.
	Converse bool `tony:"field=converse,omitzero"`
	// Occurrence is which run this is: the store revision it is about. The runner holds it and
	// reports it back on the task's snapshot.
	Occurrence int64 `tony:"field=occurrence,omitzero"`
	// Provenance is on whose account, and about what. A record, never authority.
	Provenance Provenance `tony:"field=provenance,omitzero"`
	// Credential is the token verse minted for the task: a short-lived JWT, signed by verse as an
	// issuer, that vouches to a system outside verse that a gate let this work through, and how.
	// THE TOKEN ITSELF, and a secret: the question of whether this task gets one was answered
	// before it existed, where verse minted it, and what a runner receives is the answer. Absent
	// for a task that asked for none.
	//
	// Every runner takes it, as part of the contract, and hands it to the task the same way: as
	// a file of its own outside the task's workspace, which `VERSE_CREDENTIAL_FILE` names, and
	// never as a value in the task's environment, which every process the task starts would
	// inherit. A runner never says it back: not on a snapshot, an event or its log. A sub-agent
	// doesn't get it. In a runner with no sandbox (`verse up`) the file is under the host's
	// /tmp, readable by every task the runner's user runs; a sandboxed task has its own /tmp.
	Credential string `tony:"field=credential,omitzero"`
}

// Doer is who does the work: an agent the runner offers, or a script. Exactly one of Agent and
// Script.
//
//tony:schemagen=verse-runner-doer,notag
type Doer struct {
	// Agent is the name of an agent the runner offers (`verse`, `claude`, `codex`).
	Agent string `tony:"field=agent,omitzero"`
	// Script is a script's text, run by its `#!` line, else by bash.
	Script string `tony:"field=script,omitzero"`
	// With is what the doer runs with.
	With With `tony:"field=with,omitzero"`
	// Annotations are settings for the runner, such as `verse.portkey/virtual-key`. The runner
	// refuses one it does not know.
	Annotations map[string]string `tony:"field=annotations,omitzero"`
}

// With is what a doer runs with. Model and Effort choose how an agent works; Git and Network are
// what the run is set up with, and are enforced: a runner that can't honour them refuses the
// launch.
//
//tony:schemagen=verse-runner-with,notag
type With struct {
	// Model is passed to the agent as is. Agents only; absent is the runner's default.
	Model string `tony:"field=model,omitzero"`
	// Effort is one of the agent's effort levels, where its offer takes one. Agents only.
	Effort string `tony:"field=effort,omitzero"`
	// Git is the repos the run works in, checked out in its workspace: the facts of the `git`
	// system. What may leave each is the policy's.
	Git []Repo `tony:"field=git,omitzero"`
	// GitIssue is the repository trackers the run works with: the facts of the `git-issue`
	// system, each a repo of Git, in whose checkout the tracker is.
	GitIssue []Tracker `tony:"field=git-issue,omitzero"`
	// Network is what the run may reach; an agent always reaches its own model. Absent is none.
	// It is built once, when the run starts, and nothing asks about a connection: a runner that
	// can't build the network asked for refuses the launch.
	Network *Network `tony:"field=network,omitzero"`
	// Env is variables set in the task's shell, by name, a sub-agent's included. Values, never
	// names of the runner's own variables: a runner refuses a name it owns (the variables it
	// passes a shell, and VERSE_CREDENTIAL_FILE). It is how a script gets data from the subject
	// without splicing it into its text.
	Env map[string]string `tony:"field=env,omitzero"`
}

// NetworkMode is the run's network mode: Network's, or NetworkNone when it names none.
func (w With) NetworkMode() string {
	if w.Network == nil {
		return NetworkNone
	}
	return w.Network.Mode
}

// Network is what a run may reach beside its model: NetworkNone, NetworkOpen, or NetworkAllow
// with the hosts in Allow. A host is `example.com`, `*.example.com` (its subdomains, not itself),
// or either with a port; without one it is the web ports, 80 and 443. Allowed hosts are reached
// through a proxy the runner serves the task (HTTP_PROXY, HTTPS_PROXY), and nothing else is.
//
//tony:schemagen=verse-runner-network,notag
type Network struct {
	Mode  string   `tony:"field=mode"`
	Allow []string `tony:"field=allow,omitzero"`
}

// The values of Network.Mode.
const (
	NetworkNone  = "none"
	NetworkOpen  = "open"
	NetworkAllow = "allow"
)

// Provenance is on whose account a launch is, and what it is about. Each field is absent when
// verse did not know it. It is a record a runner can write down (a commit trailer), never
// authority.
//
//tony:schemagen=verse-runner-provenance,notag
type Provenance struct {
	// Principal is who is answerable for the launch.
	Principal string `tony:"field=principal,omitzero"`
	// Subject is the record the launch is about (an entity ref).
	Subject string `tony:"field=subject,omitzero"`
	// Chain is the action inside its rule that spawned it ("review", "review.onDone").
	Chain string `tony:"field=chain,omitzero"`
}

// Check is what a launch has to satisfy whichever runner reads it: the shape of the spawn spec.
// What a particular runner offers, and what it can honour yet, is the runner's to check
// beside it (runnerd).
func (l *Launch) Check() error {
	if l.ID == "" {
		return errors.New("a launch carries an `id`: verse mints it")
	}
	switch d := l.Doer; {
	case d.Agent != "" && d.Script != "":
		return errors.New("doer: `agent` and `script` exclude each other")
	case d.Agent == "" && d.Script == "":
		return errors.New("doer: an `agent` or a `script`")
	case d.Agent != "" && strings.TrimSpace(l.Prompt) == "":
		return fmt.Errorf("an agent needs a `prompt`: what is %s to do?", d.Agent)
	case d.Script != "" && l.Prompt != "":
		return errors.New("a script takes no `prompt`: give it data in with.env")
	case d.Script != "" && d.With.Model != "":
		return errors.New("with: `model` is for an agent, not a script")
	case d.Script != "" && d.With.Effort != "":
		return errors.New("with: `effort` is for an agent, not a script")
	}
	if err := l.Policy.Check(); err != nil {
		return err
	}
	for name := range l.Doer.With.Env {
		if !envName.MatchString(name) {
			return fmt.Errorf("with.env: %q is not a variable name", name)
		}
	}
	if err := CheckWith(l.Doer.With); err != nil {
		return err
	}
	if n := l.Doer.With.Network; n != nil {
		switch {
		case n.Mode != NetworkNone && n.Mode != NetworkOpen && n.Mode != NetworkAllow:
			return fmt.Errorf("with.network: mode %q is not one of none, open, allow", n.Mode)
		case n.Mode == NetworkAllow && len(n.Allow) == 0:
			return errors.New("with.network: `allow` names the hosts it allows")
		case n.Mode != NetworkAllow && len(n.Allow) > 0:
			return fmt.Errorf("with.network: hosts are allowed only with mode allow, not %s", n.Mode)
		}
		for i, entry := range n.Allow {
			if err := CheckAllow(entry); err != nil {
				return fmt.Errorf("with.network.allow[%d]: %w", i, err)
			}
		}
	}
	if l.Timeout != "" {
		if d, err := time.ParseDuration(l.Timeout); err != nil || d <= 0 {
			return fmt.Errorf("timeout: %q is not a duration", l.Timeout)
		}
	}
	return nil
}

// envName is what a variable's name may be.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Name is who the doer is, as a task's record says it: the agent's name, or "script".
func (d Doer) Name() string {
	if d.Agent != "" {
		return d.Agent
	}
	return "script"
}

// hostName is a DNS name as the proxy compares it: lowercase labels of letters, digits and
// inner hyphens, joined by dots, with no trailing dot.
var hostName = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)*$`)

// CheckAllow is what an entry of `with.network.allow` has to be: a host, or `*.` and a domain
// under it, with a port or without one, which is the web ports. An address is written the one way
// Go writes it (`2001:db8::1`, `10.0.0.1`), which is how the proxy reads a destination. `*` alone is an open network,
// which a launch says as itself, and a `*` anywhere else would match nothing.
func CheckAllow(entry string) error {
	e := strings.ToLower(strings.TrimSpace(entry))
	if e == "" {
		return errors.New("an empty entry")
	}
	if e == "*" {
		return errors.New("`*` is not an allowlist — a launch that means it wants an open network, " +
			"which says so where anything can read it")
	}
	host := e
	if h, port, err := net.SplitHostPort(e); err == nil {
		host = h
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return fmt.Errorf("%q: the port %q is not a port", entry, port)
		}
	} else if strings.Contains(e, ":") && net.ParseIP(e) == nil {
		return fmt.Errorf("%q is not a host and a port", entry)
	}
	if after, ok := strings.CutPrefix(host, "*."); ok {
		if after == "" || !strings.Contains(after, ".") {
			return fmt.Errorf("%q matches too much — a wildcard needs a domain under it", entry)
		}
		host = after
	}
	if ip := net.ParseIP(host); ip != nil && ip.String() != host {
		return fmt.Errorf("%q: the proxy compares an address as written, so write it %s", entry, ip)
	}
	if net.ParseIP(host) == nil && !hostName.MatchString(host) {
		return fmt.Errorf("%q is not a host: a name, or `*.` and a domain, and a port if not the web's", entry)
	}
	return nil
}
