package trigger

import (
	"errors"
	"strings"
	"testing"

	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// A SPAWN'S SHAPE IS CHECKED WHERE IT IS WRITTEN, as the runner checks it at the launch: a
// charter the runner would refuse at every firing is refused at its install, with the reason.
func TestASpawnsShapeIsCheckedAtInstall(t *testing.T) {
	for _, c := range []struct{ what, spawn, want string }{
		{"a script with a prompt", `{doer: {script: "true"}, prompt: go}`, "a script takes no `prompt`"},
		{"a model on a script", `{doer: {script: "true", with: {model: large}}}`, "`model` is for an agent"},
		{"no doer", `{prompt: go}`, "an `agent` or a `script`"},
		{"an agent with no prompt", `{doer: {agent: verse}}`, "needs a `prompt`"},
		{"a withhold on a call", `{doer: {script: "true"}, policy: [{match: {system: commands, verb: run, command: "rm *"}, then: withhold}, {then: ask}]}`, "withhold names a system or a verb, not a call"},
		{"a timeout that is no duration", `{doer: {script: "true"}, timeout: soon}`, "is not a duration"},
		{"an environment name that is no name", `{doer: {script: "true", with: {env: {"A-B": x}}}}`, "is not a variable name"},
		{"a host the proxy cannot take", `{doer: {script: "true", with: {network: {mode: allow, allow: ["**.example.com"]}}}}`, "is not a host"},
		{"a wildcard over a top-level domain", `{doer: {script: "true", with: {network: {mode: allow, allow: ["*.com"]}}}}`, "matches too much"},
		{"an allowlist of everything", `{doer: {script: "true", with: {network: {mode: allow, allow: ["*"]}}}}`, "wants an open network"},
		{"two ports", `{doer: {script: "true", with: {network: {mode: allow, allow: ["example.com:443:443"]}}}}`, "is not a host and a port"},
		{"a port spelled with a zero", `{doer: {script: "true", with: {network: {mode: allow, allow: ["example.com:0443"]}}}}`, "is not a port"},
		{"a host with a trailing dot", `{doer: {script: "true", with: {network: {mode: allow, allow: ["example.com."]}}}}`, "is not a host"},
		{"a host in brackets", `{doer: {script: "true", with: {network: {mode: allow, allow: ["[example.com]"]}}}}`, "is not a host"},
		{"a port that is no port", `{doer: {script: "true", with: {network: {mode: allow, allow: ["api.example.com:http"]}}}}`, "is not a port"},
		{"a virtual key written as an annotation", `{doer: {agent: verse, annotations: {verse.portkey/virtual-key: vk-prod}}, prompt: go}`, "is verse's to write"},
		{"a row whose match is bound", `{doer: {script: "true"}, policy: [{name: head, match: {system: git, verb: push, ref: $subject.head}, then: ask}, {then: ask}]}`, "is not bound"},
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

// WHAT THE FIRING BINDS IS STOOD IN FOR at install, and only that: two repositories the firing
// names apart are two checkouts, a host and an owner written before the `$` are what a runner's
// offer is checked for, a virtual key is the annotation the firing carries, and a doer the
// firing picks or binds is not checked against an offer at all.
func TestWhatTheFiringBindsIsStoodInForAtInstall(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	var checked []runner.Launch
	eng.SetOfferCheck(func(_ string, l runner.Launch) error {
		checked = append(checked, l)
		if l.Doer.Agent != "verse" {
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
      doer:
        agent: verse
        with:
          git:
          - {repo: $subject.repo, from: $subject.head}
          - {repo: ghe.corp/$subject.lib}
          - {repo: acme/$subject.app}
      prompt: go
      virtualKey: verse.credentials.team
`)); err != nil {
		t.Fatalf("templated repositories: %v", err)
	}
	if len(checked) != 1 || len(checked[0].Doer.With.Git) != 3 {
		t.Fatalf("the offer was asked about %+v", checked)
	}
	if h := checked[0].Doer.With.Git[1].Host(); h != "ghe.corp" {
		t.Errorf("the second repository is on %q at install, want the host it was written on", h)
	}
	if o, _, _ := strings.Cut(checked[0].Doer.With.Git[2].Path(), "/"); o != "acme" {
		t.Errorf("the third repository is %s at install, want the owner it was written under", checked[0].Doer.With.Git[2].Repo)
	}
	if _, ok := checked[0].Doer.Annotations["verse.portkey/virtual-key"]; !ok {
		t.Errorf("the offer was asked about %+v, without the virtual key the firing carries", checked[0].Doer)
	}
	for i, spawn := range []string{
		`{doer: {agent: $subject.agent}, prompt: go}`,
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
			t.Errorf("%s: a doer the firing decides was checked against an offer: %+v", spawn, checked)
		}
	}
}
