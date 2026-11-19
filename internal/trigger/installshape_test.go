package trigger

import (
	"errors"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// A SPAWN'S SHAPE IS CHECKED WHERE IT IS WRITTEN, as the runner checks it at the launch: a
// charter the runner would refuse at every firing is refused at its install, with the reason.
func TestASpawnsShapeIsCheckedAtInstall(t *testing.T) {
	for _, c := range []struct{ what, spawn, want string }{
		{"a script with a prompt", `{script: "true", prompt: go}`, "`prompt` and `script` exclude each other"},
		{"an agent for a script", `{script: "true", agent: {name: verse}}`, "a script takes no `agent`"},
		{"neither a prompt nor a script", `{agent: {name: verse}}`, "a `prompt`, for an agent, or a `script`"},
		{"a person", `{person: "user:ana", prompt: go}`, "no runner takes a person yet"},
		{"a withhold, which is no more", `{script: "true", autonomy: [{match: {system: commands}, then: withhold}, {then: ask}]}`, `"withhold" is not one of deny, ask, allow`},
		{"a row the environment gives nothing to", `{script: "true", environment: {systems: {commands: {run: {}}}}, autonomy: [{match: {system: files, verb: write}, then: deny}, {then: ask}]}`, "autonomy[0]: matches nothing the environment gives (system=files verb=write)"},
		{"a row no shape has a value in common with", `{script: "true", environment: {systems: {git: {repo: acme/app, push: {ref: !glob "refs/heads/verse/*"}}}}, autonomy: [{match: {system: git, verb: push, ref: !glob "refs/tags/*"}, then: allow}, {then: ask}]}`, "matches nothing the environment gives"},
		{"a timeout that is no duration", `{script: "true", timeout: soon}`, "is not a duration"},
		{"a lease that is no duration", `{script: "true", lease: long}`, "is not a duration"},
		{"a lease under a minute", `{script: "true", lease: 30s}`, "the shortest a lease may be"},
		{"a variable name that is no name", `{script: "true", environment: {vars: {"A-B": x}}}`, "a variable's name"},
		{"a host the proxy cannot take", `{script: "true", environment: {systems: {network: {mode: allow, tcp-client: [{host: "**.example.com"}]}}}}`, "a pattern is written `!glob` or `!regexp`"},
		{"a wildcard over a top-level domain", `{script: "true", environment: {systems: {network: {mode: allow, tcp-client: [{host: !glob "*.com"}]}}}}`, "matches too much"},
		{"an allowlist of everything", `{script: "true", environment: {systems: {network: {mode: allow, tcp-client: [{host: null}]}}}}`, "any host is mode open"},
		{"a port spelled with a zero", `{script: "true", environment: {systems: {network: {mode: allow, tcp-client: [{host: example.com, port: "0443"}]}}}}`, "the field is a number: write 0443, unquoted"},
		{"a host with a trailing dot", `{script: "true", environment: {systems: {network: {mode: allow, tcp-client: [{host: "example.com."}]}}}}`, "is not a host"},
		{"a host in brackets", `{script: "true", environment: {systems: {network: {mode: allow, tcp-client: [{host: "[example.com]"}]}}}}`, "is not a host"},
		{"a port that is no port", `{script: "true", environment: {systems: {network: {mode: allow, tcp-client: [{host: api.example.com, port: http}]}}}}`, "the field is a whole number"},
		{"hosts under a mode that is not allow", `{script: "true", environment: {systems: {network: {mode: open, tcp-client: [{host: example.com}]}}}}`, "tcp-client is for mode allow"},
		{"a virtual key written as an annotation", `{agent: {name: verse, annotations: {verse.portkey/virtual-key: vk-prod}}, prompt: go}`, "is verse's to write"},
		{"a row whose match is bound", `{script: "true", autonomy: [{name: head, match: {system: git, verb: push, ref: $[subject.head]}, then: ask}, {then: ask}]}`, "is not bound"},
		{"a shape that is bound", `{script: "true", environment: {systems: {git: {repo: acme/app, push: {ref: $[subject.head]}}}}}`, "is not bound"},
	} {
		t.Run(c.what, func(t *testing.T) {
			eng := New(entitytest.Store(t), task.AutoResolver{})
			err := eng.AddTrigger(mustParseCharter(t, `
- name: r
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  action: {patch: {stage: started}, spawn: `+c.spawn+`}
`))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("install = %v, want it refused saying %q", err, c.want)
			}
		})
	}
}

// A ROW NAMING A REPOSITORY THE FIRING BINDS IS CHECKED AGAINST THE ENVIRONMENT AS WRITTEN: the
// `$` repo could be any, so the row reaches it; it is not held against the placeholder install
// stands in for the repo, which no row names. A block naming no verb gives every verb of its
// system, and installs.
func TestARowOnABoundRepoInstalls(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: r
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  action:
    patch: {stage: started}
    spawn:
      script: "true"
      environment: {systems: {git: [{repo: $[subject.value.repo], fetch: {}, push: {ref: !glob "refs/heads/verse/*"}}]}}
      autonomy: [{match: {system: git, verb: push, repo: github.com/acme/app}, then: allow}, {then: ask}]
`)); err != nil {
		t.Fatalf("a row on the repo the firing binds: %v", err)
	}
	err := eng.AddTrigger(mustParseCharter(t, `
- name: s
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  action: {patch: {stage: started}, spawn: {script: "true", environment: {systems: {git: {repo: acme/app, fetch: {}}, git-issue: {repo: acme/app}}}}}
`))
	if err != nil {
		t.Fatalf("a block naming no verb, which gives them all: %v", err)
	}
}

// WHAT THE FIRING BINDS IS STOOD IN FOR at install where the launch's shape is checked, and
// not where the runner's offer is: two repositories the firing names apart are two checkouts,
// the offer is asked about the environment as written (a host and an owner written before the
// `$` held to its ceiling there), a virtual key is the annotation the firing carries, and an
// agent the firing picks or binds is not checked against an offer at all.
func TestWhatTheFiringBindsIsStoodInForAtInstall(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	var checked []runner.Launch
	eng.SetOfferCheck(func(l runner.Launch) error {
		checked = append(checked, l)
		if l.Who() != "verse" {
			return errors.New("offers no such agent")
		}
		return nil
	})
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: two
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  action:
    patch: {stage: started}
    spawn:
      agent: {name: verse}
      environment:
        systems:
          git:
          - {repo: $[subject.value.repo], from: $[subject.value.head], fetch: {}}
          - {repo: "ghe.corp/$[subject.value.lib]", fetch: {}}
          - {repo: "acme/$[subject.value.app]", fetch: {}}
      prompt: go
      virtualKey: verse.credentials.team
`)); err != nil {
		t.Fatalf("templated repositories: %v", err)
	}
	if len(checked) != 1 || len(checked[0].Repos()) != 3 {
		t.Fatalf("the offer was asked about %+v", checked)
	}
	// The offer is asked about the environment AS WRITTEN, each expression still there: the runner's
	// ceiling holds a binding within where some value of it could be (systems.Ceiling.Contains),
	// which no stand-in for it would be.
	for i, want := range []string{"$[subject.value.repo]", "ghe.corp/$[subject.value.lib]", "acme/$[subject.value.app]"} {
		if got := checked[0].Repos()[i].Repo; got != want {
			t.Errorf("repository %d is %q at the offer check, want it as written, %q", i, got, want)
		}
	}
	if _, ok := checked[0].AgentSettings().Annotations["verse.portkey/virtual-key"]; !ok {
		t.Errorf("the offer was asked about %+v, without the virtual key the firing carries", checked[0].Agent)
	}
	for i, spawn := range []string{
		`{agent: {name: $[subject.agent]}, prompt: go}`,
	} {
		checked = nil
		if err := eng.AddTrigger(mustParseCharter(t, `
- name: picked-`+string(rune('a'+i))+`
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  action: {patch: {stage: started}, spawn: `+spawn+`}
`)); err != nil {
			t.Fatalf("%s: %v", spawn, err)
		}
		if len(checked) != 0 {
			t.Errorf("%s: an agent the firing decides was checked against an offer: %+v", spawn, checked)
		}
	}
}

// A SPAWN'S ENVIRONMENT IS HELD TO ITS RUNNER'S CEILING AT INSTALL, as written: a repository the
// firing binds whole could be within, and installs; one whose owner is written before the `$`
// and lies outside, and a literal one outside, are refused; and a push shape wider than every one
// of the ceiling's is refused naming what the ceiling gives.
func TestASpawnsEnvironmentIsHeldToTheCeilingAtInstall(t *testing.T) {
	ceiling := systems.Ceiling{{
		Attributes: map[string]*ir.Node{"repo": systems.Regexp("^github\\.com/signadot/")},
		Verbs:      map[string][]systems.Shape{"fetch": {{}}, "push": {{"ref": systems.Regexp("^refs/heads/verse/")}}},
	}}
	desc := runner.Description{Version: runner.DescriptionVersion, Offers: []runner.Offer{{Script: true,
		Systems: map[string]runner.System{
			"git":     {Ceiling: ceiling},
			"network": {Builds: systems.Ceiling{{Attributes: map[string]*ir.Node{"mode": systems.OneOf(runner.NetworkNone)}}}},
		}}}}
	for _, c := range []struct{ what, git, want string }{
		{"a repository the firing binds", `{repo: $[subject.value.repo], fetch: {}, push: {ref: !glob "refs/heads/verse/auto/*"}}`, ""},
		{"an owner's repository the firing names", `{repo: "github.com/signadot/$[subject.value.name]", fetch: {}}`, ""},
		{"another owner before the $", `{repo: "github.com/other/$[subject.value.name]", fetch: {}}`, `is beyond !regexp ^github\.com/signadot/`},
		{"a literal repository outside", `{repo: other/app, fetch: {}}`, "repo github.com/other/app is beyond"},
		{"a push wider than the ceiling's", `{repo: signadot/app, push: {ref: !regexp "^refs/heads/"}}`, "within no shape of its: {ref: !regexp ^refs/heads/verse/}"},
	} {
		t.Run(c.what, func(t *testing.T) {
			eng := New(entitytest.Store(t), task.AutoResolver{})
			eng.SetOfferCheck(func(l runner.Launch) error { return runnerd.CheckOfferAsWritten(&l, desc) })
			err := eng.AddTrigger(mustParseCharter(t, `
- name: r
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  action: {patch: {stage: started}, spawn: {script: "true", environment: {systems: {git: `+c.git+`}}}}
`))
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("install = %v, want it refused saying %q", err, c.want)
			case c.want != "" && !strings.Contains(err.Error(), "beyond this runner's ceiling for git"):
				t.Errorf("the refusal does not name the ceiling: %v", err)
			}
		})
	}
}
