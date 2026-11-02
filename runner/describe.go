package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/systems"
)

// Description is what a runner answers on GET /describe: what it offers. Verse reads it when the
// runner is registered, and whenever the runner restarts or reports a change, and keeps it with
// the registration.
//
// Each offer is a doer, an agent or scripts, and the systems it holds for its tasks: for each,
// what the runner builds of it (Builds, its nature) and what its operator allows (Ceiling, a
// choice), published apart so a refusal can say which. A launch's environment lies within both.
//
//tony:schemagen=verse-runner-description,notag
type Description struct {
	// Version is the shape of the description, DescriptionVersion when a runner writes it. One
	// kept from an older runner is refused rather than read as this one (Check): the runner is
	// asked again.
	Version int     `tony:"field=version,omitzero"`
	Offers  []Offer `tony:"field=offers"`
}

// DescriptionVersion is the shape a runner describes itself in.
const DescriptionVersion = 2

// TokenHeader is the header a runner answers GET /describe with, carrying the workload token
// its runtime gave it, to a verse it verified as its own: the runner's half of the handshake.
// Verse checks the token's subject against the runner's registration before it records the
// description, so a runner replaced behind the same url is found by the step that admitted the
// first. A runner that verifies no verse answers none, since a token is for its verse alone.
const TokenHeader = "Verse-Runner-Token"

// VerseSubject is the `sub` of the token a verse presents to its runners: the verse itself. The
// verse's issuer signs other tokens too, a gate credential among them (`sub` the rule and how its
// gate passed), for an audience the charter declares; the subject is what tells the verse's own
// token from those, so a runner's door admits this one alone.
const VerseSubject = "verse"

// A STATIC TOKEN IS KNOWN BY ITS HASH. A runner with no runtime to issue it a workload token, on a
// machine off Kubernetes, proves itself with a token it and its registrar hold, as an iod may
// (auth.iod.static). The registration names it by its hash, `sha256:<hex>`, as its subject, so
// verse never holds the token and the registration stays everyone's to read; and that subject is
// also the audience the runner holds verse's tokens to, read off its own token as a workload
// token's `sub` is. A hash everyone may read is only as good as the token is long, so a static
// token is refused under StaticTokenMin bytes.

// StaticSubjectPrefix begins the subject of a runner proved by a static token.
const StaticSubjectPrefix = "sha256:"

// StaticTokenMin is the fewest bytes a runner's static token may hold.
const StaticTokenMin = 32

// IsStaticSubject is whether s names a static token by its hash, rather than a workload token's
// subject.
func IsStaticSubject(s string) bool { return strings.HasPrefix(s, StaticSubjectPrefix) }

// isJWT is whether token has a JWT's shape, three parts: a static token is anything else.
func isJWT(token string) bool { return strings.Count(token, ".") == 2 }

// StaticSubjectOf is the subject a bearer proves as a static token: its hash, where it is not a
// JWT. A token too short to be a runner's still hashes, and matches no registration.
func StaticSubjectOf(token string) (string, bool) {
	if token == "" || isJWT(token) {
		return "", false
	}
	return StaticSubjectOfHash(sha256.Sum256([]byte(token))), true
}

// StaticSubjectOfHash is the subject of the static token whose hash is h.
func StaticSubjectOfHash(h [sha256.Size]byte) string {
	return StaticSubjectPrefix + hex.EncodeToString(h[:])
}

// StaticSubject is the subject a registration names a runner's static token by, refusing a token
// too short for its hash to be published, and one that reads as a JWT.
func StaticSubject(token string) (string, error) {
	if isJWT(token) {
		return "", errors.New("the token has a JWT's shape: a static token is not a JWT, and a workload token is registered by its subject (-subject)")
	}
	if len(token) < StaticTokenMin {
		return "", fmt.Errorf("a static token is named by a hash everyone may read, so it holds %d bytes at least, and this one holds %d", StaticTokenMin, len(token))
	}
	sub, _ := StaticSubjectOf(token)
	return sub, nil
}

// SubjectOf is what a runner's own token says it is: a workload token's `sub`, or a static
// token's hash.
func SubjectOf(token string) (string, error) {
	if isJWT(token) {
		return TokenSubject(token)
	}
	return StaticSubject(token)
}

// TokenSubject is the `sub` of a JWT, read without verifying it: what a runner's own token says
// it is, which is the audience the runner holds its verse's tokens to. The token is the
// runtime's and nothing here trusts the claim beyond naming the door.
func TokenSubject(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("the token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("the token's claims do not decode: %w", err)
	}
	var claims map[string]any
	if err := codec.Decode(bytes.NewReader(payload), &claims, codec.JSON); err != nil {
		return "", fmt.Errorf("the token's claims do not read: %w", err)
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", fmt.Errorf("the token names no subject")
	}
	return sub, nil
}

// ReadTokenFile is the workload token a file holds, as it is now: the runtime rotates the token
// under the process, so a side that presents one reads the file at each use. A file that is
// missing or holds nothing is an error naming it, so a path given wrong is refused where it was
// named rather than guessed at from the other side of the handshake.
func ReadTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("workload token: %w", err)
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("workload token: %s holds none", path)
	}
	return tok, nil
}

// CheckSubject is whether s can be a runner's subject, on either side of the handshake: a
// registration's `subject` and a runner's `verse.audience` are compared with a token's as
// written, so one with space in it matches no token anybody issues, and one naming a static
// token is its hash in full, sha256:<64 hex digits>.
func CheckSubject(s string) error {
	if s != strings.TrimSpace(s) || strings.ContainsAny(s, " \t\r\n") {
		return fmt.Errorf("subject %q is a token's subject, compared as it is written, and holds no space", s)
	}
	if IsStaticSubject(s) {
		h := strings.TrimPrefix(s, StaticSubjectPrefix)
		if _, err := hex.DecodeString(h); err != nil || len(h) != 2*sha256.Size || h != strings.ToLower(h) {
			return fmt.Errorf("subject %q names a static token by its hash, %s and %d lowercase hex digits", s, StaticSubjectPrefix, 2*sha256.Size)
		}
	}
	return nil
}

// Check is what a description must say for verse to register the runner, or to read one it
// kept: its version, at least one offer, each holding the network with the modes it builds, each
// ceiling and each `builds` within its system's document, each setting of an agent typed and none
// called `name` or `annotations`.
func (d Description) Check() error {
	if d.Version != DescriptionVersion {
		return fmt.Errorf("it describes itself in version %d, not %d: the runner is older or newer than this verse, and is asked again (`verse runner check`)",
			d.Version, DescriptionVersion)
	}
	if len(d.Offers) == 0 {
		return errors.New("it offers nothing; a runner offers an agent or scripts")
	}
	// Each doer at most once: a launch names its doer by the agent's name, or by nothing for a
	// script, so two offers of one doer could not be told apart by the launch meant for either.
	seen := map[string]bool{}
	for _, o := range d.Offers {
		if (o.Agent == nil) == !o.Script {
			return errors.New("an offer is an agent or scripts, one of the two")
		}
		if seen[o.Name()] {
			return fmt.Errorf("it offers %s twice; a launch names its doer, and could not say which of the two it means", o.Name())
		}
		seen[o.Name()] = true
		if o.Agent != nil {
			if o.Agent.Name == "" {
				return errors.New("an agent offer names its agent")
			}
			for _, name := range slices.Sorted(maps.Keys(o.Agent.Settings)) {
				if ReservedSetting(name) {
					return fmt.Errorf("offer %s: a setting may not be called %s: an agent is written with it beside its settings", o.Name(), name)
				}
				if err := o.Agent.Settings[name].check(); err != nil {
					return fmt.Errorf("offer %s: settings.%s: %w", o.Name(), name, err)
				}
			}
		}
		for _, step := range o.Requires {
			system, verb, ok := systems.Split(step)
			if _, isVerb := systemsVerb(system, verb); !ok || !isVerb {
				return fmt.Errorf("offer %s: requires %q, which is no step of a system", o.Name(), step)
			}
			sys, held := o.Systems[system]
			if !held {
				return fmt.Errorf("offer %s: requires %s, of a system it does not hold", o.Name(), step)
			}
			if !sys.Ceiling.Gives(step) {
				return fmt.Errorf("offer %s: requires %s, which its ceiling for %s does not give: no launch could run it", o.Name(), step, system)
			}
		}
		net, held := o.Systems["network"]
		if !held || len(net.Builds) == 0 {
			return fmt.Errorf("offer %s: the network's `builds` names no network the runner builds", o.Name())
		}
		for _, name := range slices.Sorted(maps.Keys(o.Systems)) {
			sys := o.Systems[name]
			if err := sys.Builds.Check(name); err != nil {
				return fmt.Errorf("offer %s: systems.%s.builds%w", o.Name(), name, err)
			}
			if err := sys.Ceiling.Check(name); err != nil {
				return fmt.Errorf("offer %s: systems.%s.ceiling%w", o.Name(), name, err)
			}
		}
	}
	return nil
}

// systemsVerb is a system's verb, by their names.
func systemsVerb(system, verb string) (systems.Verb, bool) {
	d, ok := systems.Get(system)
	if !ok {
		return systems.Verb{}, false
	}
	return d.Verb(verb)
}

// Offer is one kind of doer a runner runs: an agent, by name, or scripts. A runner offers each doer
// at most once, and several runners may offer one doer: verse routes a launch to one of them.
//
//tony:schemagen=verse-runner-offer,notag
type Offer struct {
	// Agent is the agent offered, and what it takes. Nil for scripts.
	Agent *AgentOffer `tony:"field=agent,omitzero"`
	// Script is set on the offer for scripts.
	Script bool `tony:"field=script,omitzero"`
	// Version is what the runner runs for this offer (a Claude Code version, say), and it goes
	// on the record of every task the offer runs.
	Version string `tony:"field=version,omitzero"`
	// Systems are the systems the offer holds for its tasks, by name (package systems): what the
	// runner builds of each and the ceiling over it. A system absent here is not held: a launch
	// whose environment gives it is refused.
	Systems map[string]System `tony:"field=systems,omitzero"`
	// Requires is the steps the offer's doer can't run without (`commands.run`, `files.write`):
	// a launch whose environment gives none of one is refused where it is written, naming what to
	// add, rather than run to fail at its first action. Verbs, not systems: a doer needs a verb,
	// and a system's other verbs are no stand-in for it. Absent is nothing required.
	Requires []string `tony:"field=requires,omitzero"`
	// Converse is whether a launch for this offer may ask for a conversation (Launch.Converse):
	// the agent hands the person the turn when it goes quiet. A script offer never does.
	Converse bool `tony:"field=converse,omitzero"`
}

// Name is who the offer runs, as a message says it: the agent's name, or "script".
func (o Offer) Name() string {
	if o.Agent == nil {
		return "script"
	}
	return o.Agent.Name
}

// AgentOffer is an agent a runner offers: its name, the settings it takes, and the annotations
// verse or the runner may write for it.
//
//tony:schemagen=verse-runner-agent-offer,notag
type AgentOffer struct {
	Name string `tony:"field=name"`
	// Settings are what a launch's agent may set, by name (Agent.Settings), each with the values
	// it takes, the operator's narrowing already in them. A setting not listed is refused.
	Settings map[string]Setting `tony:"field=settings,omitzero"`
	// Annotations is the annotations the agent takes, by name, with the type of their value.
	Annotations map[string]string `tony:"field=annotations,omitzero"`
}

// Setting is one setting an agent takes: its form, the values it takes as patterns (absent, any of
// its form), and whether the agent has a default of its own where a launch names none, which the
// record then says (the task's snapshot, Settings).
//
//tony:schemagen=verse-runner-setting,notag
type Setting struct {
	// Form is the value's kind: string (absent), number or bool. Values are strings on the wire,
	// as a charter binds them; the runner reads them by form.
	Form string `tony:"field=form,omitzero"`
	// Values are the values taken, as patterns (`claude-*`); absent, any of the form.
	Values []string `tony:"field=values,omitzero"`
	// Default is whether the agent has its own value where a launch names none.
	Default bool `tony:"field=default,omitzero"`
}

// The forms of a setting's value.
const (
	FormString = "string"
	FormNumber = "number"
	FormBool   = "bool"
)

func (s Setting) check() error {
	switch s.Form {
	case "", FormString, FormNumber, FormBool:
	default:
		return fmt.Errorf("form: %q is not string, number or bool", s.Form)
	}
	// A value listed is one of its form, or a pattern that could be: a list no value of the form
	// can match takes nothing.
	for _, v := range s.Values {
		if strings.Contains(v, "*") {
			continue
		}
		if err := (Setting{Form: s.Form}).Takes(v); err != nil {
			return fmt.Errorf("values: %w", err)
		}
	}
	return nil
}

// Takes is why a setting does not take a value, or nil: of its form, and matching one of its
// values where it lists them. The value is bound: a charter's `$` setting is left out at install
// (trigger.installLaunch) and checked here once the launch has bound it, `$` and all.
// A setting's `*` matches across `/`, unlike a path's: a model's name is one word, whatever it
// spells (`*` takes `anthropic/claude-sonnet-5`).
func (s Setting) Takes(v string) error {
	switch s.Form {
	case FormNumber:
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return fmt.Errorf("%q is not a number", v)
		}
	case FormBool:
		if v != "true" && v != "false" {
			return fmt.Errorf("%q is not true or false", v)
		}
	}
	if len(s.Values) > 0 && !slices.ContainsFunc(s.Values, func(p string) bool { return settingMatch(p, v) }) {
		return fmt.Errorf("%q is none of %s", v, strings.Join(s.Values, ", "))
	}
	return nil
}

// settingMatch is whether a setting's value is one a pattern of its Values spells: `*` any run of
// characters, `/` among them, and anything else itself.
func settingMatch(pattern, v string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return v == pattern
	}
	first, last := parts[0], parts[len(parts)-1]
	if len(v) < len(first)+len(last) || !strings.HasPrefix(v, first) || !strings.HasSuffix(v, last) {
		return false
	}
	v = v[len(first) : len(v)-len(last)]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(v, p)
		if i < 0 {
			return false
		}
		v = v[i+len(p):]
	}
	return true
}

// System is one system an offer holds: what the runner builds of it, and the ceiling over it.
//
//tony:schemagen=verse-runner-system,notag
type System struct {
	// Builds is what the runner can build of the system, by its nature (its sandbox), in the
	// ceiling's form: the network's modes, today. Never written in a config: the runner says it.
	// Empty is the whole of the system's document.
	Builds systems.Ceiling `tony:"field=builds,omitzero"`
	// Ceiling is the operator's bound on the system (systems.Ceiling): blocks in the
	// environment's form, closed, which a launch's environment must lie within, and which the
	// runner holds each act to again. Empty is nothing of the system, but for the network, whose
	// empty ceiling is no bound beyond what the runner builds.
	Ceiling systems.Ceiling `tony:"field=ceiling,omitzero"`
}
