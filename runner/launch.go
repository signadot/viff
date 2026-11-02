package runner

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// Launch is the body of `POST /tasks`: the work verse asks a runner to start, in the spawn
// spec's shape. It is the spec's spawn with verse's references resolved: verse sends it as is
// and a runner reads it as is.
//
// What runs is said by which key is there: a `prompt`, for an agent (the runner's default
// unless `agent` names one), or a `script`. The environment is what the run can reach, fixed
// when it starts; the autonomy decides each step within it, and may be replaced as it runs.
//
// A runner checks it strictly: an unknown key, neither or both of a prompt and a script, or a
// value its offer does not take is refused with `400` and the reason, and so is anything it
// cannot honour yet. A refused launch is a failed spawn, never a task run without
// what it asked for.
//
//tony:schemagen=verse-runner-launch,notag
type Launch struct {
	// ID is the task's id. Verse mints it, so that it matches the task's record.
	ID string `tony:"field=id"`
	// Prompt is the job, in text, for an agent. Exactly one of Prompt and Script.
	Prompt string `tony:"field=prompt,omitzero"`
	// Script is a script's text, run by its `#!` line, else by bash: the work and who does it at
	// once. Its input is the environment's vars.
	Script string `tony:"field=script,omitzero"`
	// Agent is the agent a prompt is for, and how it works. Absent is the runner's default
	// agent. A script takes none.
	Agent *Agent `tony:"field=agent,omitzero"`
	// Person is reserved for a person doing the work, `user:…`; no runner takes one yet.
	Person string `tony:"field=person,omitzero"`
	// Environment is what the run can reach, and the variables its shell is given
	// (systems.Environment): fixed when the run starts, set up from by the runner, checked
	// against its ceilings, and what the task's tools come from.
	Environment systems.Environment `tony:"field=environment,omitzero"`
	// Autonomy is what happens to each step the task takes (task.Autonomy): the first row that
	// matches it denies it, asks a person first, or lets it go, and the last row, which names
	// no step, decides the rest. It may be replaced while the task runs. Absent asks before
	// every step.
	Autonomy task.Autonomy `tony:"field=autonomy,omitzero"`
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

// Agent is the agent a prompt is for, and how it works: its name, the settings its offer takes
// (Offer.Agent.Settings: `model`, `effort`, a runner's own such as `temperature`), and the
// annotations verse or the runner write for it. Written flat, its settings beside its name:
// `{name: codex, model: gpt-5-codex, effort: high}`.
//
//tony:schemagen=verse-runner-agent,notag,codec=custom
type Agent struct {
	// Name is the name of an agent the runner offers (`verse`, `codex`). Empty is the runner's
	// default agent, given the settings below.
	Name string
	// Settings are the agent's settings, by name, each a value its offer takes; one left out
	// is the agent's own default, where it has one. `model` is the one verse reads.
	Settings map[string]string
	// Annotations are settings for the runner that verse or the runner write, never a spawn's
	// author knob, such as `verse.portkey/virtual-key`. The runner refuses one it does not know.
	Annotations map[string]string
}

// SettingModel is the setting verse reads of every agent: the model it runs, on the record.
const SettingModel = "model"

// The keys an agent is written with beside its settings, which no setting may be called.
const (
	agentName        = "name"
	agentAnnotations = "annotations"
)

// ReservedSetting is whether a name is no setting's: one an agent is written with beside them.
func ReservedSetting(name string) bool { return name == agentName || name == agentAnnotations }

// Model is the agent's `model` setting, "" where it names none.
func (a Agent) Model() string { return a.Settings[SettingModel] }

// ToTonyIR writes the agent flat: its name, its settings, its annotations.
func (a Agent) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	var kvs []ir.KeyVal
	if a.Name != "" {
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(agentName), Val: ir.FromString(a.Name)})
	}
	for _, k := range slices.Sorted(maps.Keys(a.Settings)) {
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(k), Val: ir.FromString(a.Settings[k])})
	}
	if len(a.Annotations) > 0 {
		var ann []ir.KeyVal
		for _, k := range slices.Sorted(maps.Keys(a.Annotations)) {
			ann = append(ann, ir.KeyVal{Key: ir.FromString(k), Val: ir.FromString(a.Annotations[k])})
		}
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(agentAnnotations), Val: ir.FromKeyVals(ann)})
	}
	return ir.FromKeyVals(kvs), nil
}

// FromTonyIR reads the agent flat: `name`, `annotations`, and every other key a setting, its value
// a string (a number or a boolean read as it is spelled).
func (a *Agent) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	node = ir.Uncomment(node)
	if node == nil || node.Type == ir.NullType {
		*a = Agent{}
		return nil
	}
	if node.Type != ir.ObjectType {
		return errors.New("agent: an object, `{name: …, <setting>: …}`")
	}
	out := Agent{}
	for i, key := range node.Fields {
		key = ir.Uncomment(key)
		if key == nil || key.Type != ir.StringType {
			return errors.New("agent: a key is `name`, `annotations` or a setting's name")
		}
		val := ir.Uncomment(node.Values[i])
		switch key.String {
		case agentName:
			v, err := scalarString(val)
			if err != nil {
				return fmt.Errorf("agent.name: %w", err)
			}
			out.Name = v
		case agentAnnotations:
			if val == nil || val.Type != ir.ObjectType {
				return errors.New("agent.annotations: an object of annotations")
			}
			out.Annotations = map[string]string{}
			for j, k := range val.Fields {
				k = ir.Uncomment(k)
				v, err := scalarString(val.Values[j])
				if err != nil {
					return fmt.Errorf("agent.annotations.%s: %w", k.String, err)
				}
				out.Annotations[k.String] = v
			}
		default:
			v, err := scalarString(val)
			if err != nil {
				return fmt.Errorf("agent.%s: a setting is a value: %w", key.String, err)
			}
			if out.Settings == nil {
				out.Settings = map[string]string{}
			}
			out.Settings[key.String] = v
		}
	}
	*a = out
	return nil
}

// scalarString is a value as text: a string, or a number or boolean as it is spelled.
func scalarString(n *ir.Node) (string, error) {
	n = ir.Uncomment(n)
	switch {
	case n == nil || n.Type == ir.NullType:
		return "", errors.New("null is no value")
	case n.Type == ir.StringType:
		return n.String, nil
	case n.Type == ir.BoolType:
		return strconv.FormatBool(n.Bool), nil
	case n.Type == ir.NumberType:
		switch {
		case n.Number != "":
			return n.Number, nil
		case n.Int64 != nil:
			return strconv.FormatInt(*n.Int64, 10), nil
		case n.Float64 != nil:
			return strconv.FormatFloat(*n.Float64, 'g', -1, 64), nil
		}
	}
	return "", errors.New("a string, a number or a boolean")
}

// Who is who does the work, as a task's record says it: the agent's name, "script", or "" for a
// prompt that names none, which a runner refuses: verse binds its default agent before the launch.
func (l *Launch) Who() string {
	switch {
	case l.Script != "":
		return "script"
	case l.Agent != nil:
		return l.Agent.Name
	}
	return ""
}

// AgentSettings is the launch's agent's model, effort and annotations, empty for none.
func (l *Launch) AgentSettings() Agent {
	if l.Agent == nil {
		return Agent{}
	}
	return *l.Agent
}

// Repos is the repositories the run checks out: its environment's `git` blocks.
func (l *Launch) Repos() []Repo {
	var out []Repo
	for _, b := range l.Environment.Systems["git"] {
		out = append(out, Repo{Repo: b.Attributes["repo"], From: b.Attributes["from"]})
	}
	return out
}

// Pushes is whether the environment gives the run a push to the repository (its canonical
// name), or, with "", to any: work in a repository it can't push to has nowhere to go.
func (l *Launch) Pushes(repo string) bool {
	for _, b := range l.Environment.Systems["git"] {
		if repo != "" && systems.Canonical(systems.FormRepo, b.Attributes["repo"]) != repo {
			continue
		}
		if len(b.Given("git")["push"]) > 0 {
			return true
		}
	}
	return false
}

// Trackers is the repositories whose own trackers the run works with: its `git-issue` blocks.
func (l *Launch) Trackers() []Tracker {
	var out []Tracker
	for _, b := range l.Environment.Systems["git-issue"] {
		out = append(out, Tracker{Repo: b.Attributes["repo"]})
	}
	return out
}

// Vars is the variables the task's shell is given.
func (l *Launch) Vars() map[string]string { return l.Environment.Vars }

// Network is what the runner builds for the run from its environment's `network` block: its
// mode, absent being none, and under allow the hosts its `tcp-client` shapes name by a literal
// or a glob, as entries (`host`, `host:port`, `*.domain`).
// The task's proxy decides each connection by the shapes themselves (runnerd.connectionRefused),
// a host written as any other pattern included; these entries are what Check holds a shape's
// host and port to.
func (l *Launch) Network() Network {
	blocks := l.Environment.Systems["network"]
	if len(blocks) == 0 {
		return Network{Mode: NetworkNone}
	}
	b := blocks[0]
	n := Network{Mode: b.Attributes["mode"]}
	if n.Mode == "" {
		n.Mode = NetworkNone
	}
	for _, s := range b.Verbs["tcp-client"] {
		entry, ok := hostEntry(s["host"])
		if !ok {
			continue
		}
		if p := ir.Uncomment(s["port"]); p != nil && p.Tag == "" && p.Type == ir.NumberType && p.Int64 != nil {
			entry = net.JoinHostPort(entry, strconv.FormatInt(*p.Int64, 10))
		}
		n.Allow = append(n.Allow, entry)
	}
	return n
}

// hostEntry is a shape's host as an entry, where it is a literal or a glob: the name, or the
// glob's text (`*.github.com`).
func hostEntry(n *ir.Node) (string, bool) {
	n = ir.Uncomment(n)
	switch {
	case n == nil || n.Type != ir.StringType:
		return "", false
	case n.Tag == "":
		return n.String, true
	case n.Tag == "!glob":
		return n.String, true
	}
	return "", false
}

// tcpShapes is the `tcp-client` shapes of the environment's network.
func tcpShapes(env systems.Environment) []systems.Shape {
	var out []systems.Shape
	for _, b := range env.Systems["network"] {
		out = append(out, b.Verbs["tcp-client"]...)
	}
	return out
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
	switch {
	case l.Prompt != "" && l.Script != "":
		return errors.New("`prompt` and `script` exclude each other: a prompt is an agent's work, a script is its own")
	case strings.TrimSpace(l.Prompt) == "" && l.Script == "":
		return errors.New("a `prompt`, for an agent, or a `script`")
	case l.Script != "" && l.Agent != nil:
		return errors.New("a script takes no `agent`: it is its own doer")
	case l.Person != "":
		return errors.New("person: no runner takes a person yet")
	}
	if err := l.Autonomy.Check(); err != nil {
		return fmt.Errorf("autonomy%w", err)
	}
	if err := l.Environment.Check(); err != nil {
		return fmt.Errorf("environment.%w", err)
	}
	if err := CheckRepos(l.Repos()); err != nil {
		return err
	}
	n, shapes := l.Network(), tcpShapes(l.Environment)
	if n.Mode != NetworkAllow && len(shapes) > 0 {
		return fmt.Errorf("environment.systems.network: tcp-client is for mode allow, not %s", n.Mode)
	}
	if n.Mode == NetworkAllow && len(shapes) == 0 {
		return errors.New("environment.systems.network: mode allow names the hosts it allows, under tcp-client")
	}
	for i, shape := range shapes {
		if h := ir.Uncomment(shape["host"]); h == nil || h.Type == ir.NullType {
			return fmt.Errorf("environment.systems.network.tcp-client[%d]: a shape names the host it allows; any host is mode open", i)
		}
	}
	for i, entry := range n.Allow {
		if err := CheckAllow(entry); err != nil {
			return fmt.Errorf("environment.systems.network.tcp-client[%d]: %w", i, err)
		}
	}
	if l.Timeout != "" {
		if d, err := time.ParseDuration(l.Timeout); err != nil || d <= 0 {
			return fmt.Errorf("timeout: %q is not a duration", l.Timeout)
		}
	}
	return nil
}

// CheckReach refuses an autonomy row that can match no step the environment gives
// (systems.Environment.Reaches): a system it has no block of, a verb no block gives, a value no
// block or shape has in common with the row. Such a row never fires, so it is a mistake, said
// where it is written, on the environment as written: a charter's at install, with its `$`
// attributes still to be bound (and so taken to match), a hand run's, a then's, and a mid-run
// replacement's against the task's. A row wider than the environment is not: the environment
// bounds it. The default row, and a row naming no system, decide whatever the environment gives.
func CheckReach(p task.Autonomy, env systems.Environment) error {
	for i, r := range p {
		if r.Match.Any() || r.Match.System == "" {
			continue
		}
		if !env.Reaches(r.Match.System, r.Match.VerbNames(), r.Match.Params) {
			return fmt.Errorf("autonomy[%d]: matches nothing the environment gives (%s), so it never decides: "+
				"give it in environment.systems.%s, or drop the row", i, rowMatch(r.Match), r.Match.System)
		}
	}
	return nil
}

// rowMatch is a row's match in words, for a refusal that names it.
func rowMatch(m *task.Match) string {
	fs := []string{"system=" + m.System}
	switch {
	case m.Verb != "":
		fs = append(fs, "verb="+m.Verb)
	case m.VerbPattern != nil:
		fs = append(fs, "verb="+systems.Words(m.VerbPattern))
	}
	for _, k := range slices.Sorted(maps.Keys(m.Params)) {
		fs = append(fs, k+"="+systems.Words(m.Params[k]))
	}
	return strings.Join(fs, " ")
}

// hostName is a DNS name as the proxy compares it: lowercase labels of letters, digits and
// inner hyphens, joined by dots, with no trailing dot.
var hostName = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)*$`)

// CheckAllow is what a host the network allows (a `tcp-client` shape, as Network reads it) has to be: a host, or `*.` and a domain
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
