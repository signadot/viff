package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
)

// taskFor waits for the task a rule spawned about subject and answers its record.
func taskFor(t *testing.T, srv *Server, rule, subject string) entity.Entity {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		for _, ent := range entity.Below(srv.Store, mirror.IsTaskish, 2, entity.VerseRoot, mirror.System) {
			if !ent.Ref.Under(entity.VerseRoot, mirror.System, mirror.InstanceKind) {
				continue
			}
			if entity.Str(ent.Payload, "flow") == subject && entity.Str(ent.Payload, "chain") == rule {
				return ent
			}
		}
		select {
		case <-deadline:
			t.Fatalf("%s never spawned a task about %s", rule, subject)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A RULE'S TASK ACTS FOR WHOEVER WROTE THE RECORD, where the rule says `as: subject`. The
// installer's charter watches `ask.*`; daniel writes an ask; the task that answers it is made by
// the pair of daniel and the rule, which acts as daniel — so the task record is daniel's, and says
// who asked. The same charter's rule without `as` spawns as its installer, as every spawn did.
func TestASpawnAsTheSubjectActsForTheRecordsOwner(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const daniel, installer = "user:daniel@example.com", "user:installer@example.com"
	// The ask charter's shape: its installer owns `ask` and holds out `owner` beneath it, so an
	// ask is its writer's record, and the rule — acting as the owner above — may latch it.
	askPlace := entity.RefAt("ask")
	if err := entity.Claim(srv.Store, askPlace, installer); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(srv.Store, askPlace, []entity.Grant{{At: askPlace.Segs(), Below: true,
		To: map[entity.Level][]string{entity.OwnerLevel: {daniel}}}}); err != nil {
		t.Fatal(err)
	}
	resp := installAs(t, installer, ts.URL, `[
		{name: for-the-asker, condition: {subject: {path: "ask.*", value: !and [{question: null}, !not {asked: null}]}},
		 action: {spawn: {driver: scripted, prompt: "true", as: subject}, onDone: {patch: {asked: true}}}},
		{name: for-the-rule, condition: {subject: {path: "ask.*", value: !and [{question: null}, !not {ruled: null}]}},
		 action: {spawn: {driver: scripted, prompt: "true"}, onDone: {patch: {ruled: true}}}},
	]`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("install: %s: %s", resp.Status, body)
	}
	resp.Body.Close()

	ask := entity.RefAt("ask", "one")
	if _, _, err := entity.As(srv.Store, daniel).Write(entity.Write{At: ask.At(),
		Value: entity.Obj{"question": "land it?"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if o, _ := entity.OwnerOf(srv.Store, ask); o != daniel {
		t.Fatalf("the fixture is wrong: the ask is %q's", o)
	}

	asked := taskFor(t, srv, "for-the-asker", ask.String())
	if got := entity.Str(asked.Payload, "asker"); got != daniel {
		t.Errorf("the task says it acts for %q, want the ask's owner", got)
	}
	if got, _ := entity.OwnerOf(srv.Store, asked.Ref); got != daniel {
		t.Errorf("the task spawned as the subject is %q's, want the asker's", got)
	}

	plain := taskFor(t, srv, "for-the-rule", ask.String())
	if got := entity.Str(plain.Payload, "asker"); got != "" {
		t.Errorf("a rule without `as` says it acts for %q", got)
	}
	if got, _ := entity.OwnerOf(srv.Store, plain.Ref); got != installer {
		t.Errorf("the task spawned as the rule is %q's, want its installer's", got)
	}
}

// WHO A TASK ACTS FOR is asked for, never named: `as` takes one value, and a charter writing the
// asker itself is refused at install, where its author can be told. So is a gate naming
// $principal where nobody is: on a spawn not `as: subject`, or on the rule's own gate.
func TestAChartersSpawnCannotNameWhoItActsFor(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for name, c := range map[string]struct{ spawn, says string }{
		"an as that is not subject":  {`{driver: scripted, prompt: "true", as: object}`, "spawn `as: object`"},
		"an asker the charter names": {`{driver: scripted, prompt: "true", asker: "user:ceo@example.com"}`, "spawn `asker:` is the engine's to set"},
		"$principal on a rule's task": {`{driver: scripted, prompt: "true", gates: [{name: shell, mode: ask, answerers: [$principal]}]}`,
			"this rule's task acts for nobody"},
	} {
		spawn := c.spawn
		resp := installAs(t, "user:installer@example.com", ts.URL, `{name: r,
			condition: {subject: {path: "ask.*", value: !and [{question: null}, !not {asked: null}]}},
			action: {spawn: `+spawn+`, onDone: {patch: {asked: true}}}}`)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: install = %s, want refused", name, resp.Status)
		} else if !strings.Contains(string(body), c.says) {
			t.Errorf("%s: refused for another reason: %s", name, body)
		}
	}
	resp := installAs(t, "user:installer@example.com", ts.URL, `{name: g,
		condition: {subject: {path: "ask.*", value: !and [{question: null}, !not {asked: null}]}},
		gates: [{name: ok, mode: ask, answerers: [$principal]}], action: {patch: {asked: true}}}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "a rule's own gate is asked by the rule") {
		t.Errorf("$principal on the rule's gate: install = %s %s, want refused", resp.Status, body)
	}
	// Acting for the subject's owner, the task has somebody to ask.
	resp = installAs(t, "user:installer@example.com", ts.URL, `{name: p,
		condition: {subject: {path: "ask.*", value: !and [{question: null}, !not {asked: null}]}},
		action: {spawn: {driver: scripted, prompt: "true", as: subject,
			gates: [{name: shell, mode: ask, answerers: [$principal]}]}, onDone: {patch: {asked: true}}}}`)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Errorf("$principal on an `as: subject` task: install = %s %s, want it installed", resp.Status, body)
	}
}

// A RULE ACTS AS WHOEVER WROTE A RECORD ONLY IN ITS INSTALLER'S PLACE. Its task runs on the
// record owner's authority, so a rule over somebody else's space would run its author's task as
// them. Over Daniel's space it is refused at install, wherever in the rule the `as: subject`
// is; over the installer's own place it installs; and a rule over Daniel's space that acts as
// itself installs as before.
func TestARuleActsAsTheSubjectOnlyInItsInstallersPlace(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const daniel, installer = "user:daniel@example.com", "user:installer@example.com"
	for place, owner := range map[string]string{"spaces.daniel": daniel, "ask": installer} {
		ref, err := entity.ParseAddr(place)
		if err != nil {
			t.Fatal(err)
		}
		if err := entity.Claim(srv.Store, ref, owner); err != nil {
			t.Fatal(err)
		}
	}
	rule := func(name, path, action string) string {
		return `{name: ` + name + `, condition: {subject: {path: "` + path + `", value: !and [{question: null}, !not {done: null}]}}, action: ` + action + `}`
	}
	for _, c := range []struct {
		name, charter string
		want          int
	}{
		{"as the subject over daniel's space", rule("harvest", "spaces.daniel.*",
			`{spawn: {driver: scripted, prompt: "true", as: subject}, onDone: {patch: {done: true}}}`), http.StatusForbidden},
		{"as the subject in a hook, over daniel's space", rule("harvest-later", "spaces.daniel.*",
			`{spawn: {driver: scripted, prompt: "true"}, onDone: {patch: {done: true}, spawn: {driver: scripted, prompt: "true", as: subject}}}`), http.StatusForbidden},
		{"as the subject in the installer's place", rule("asks", "ask.*",
			`{spawn: {driver: scripted, prompt: "true", as: subject}, onDone: {patch: {done: true}}}`), http.StatusCreated},
		{"as itself over daniel's space", rule("watch-daniel", "spaces.daniel.*",
			`{spawn: {driver: scripted, prompt: "true"}, onDone: {patch: {done: true}}}`), http.StatusCreated},
	} {
		resp := installAs(t, installer, ts.URL, c.charter)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: install = %s, want %d: %s", c.name, resp.Status, c.want, body)
		} else if c.want == http.StatusForbidden && !strings.Contains(string(body), "acting as the owner of what it selects") {
			t.Errorf("%s: refused for another reason: %s", c.name, body)
		}
	}
}

// taskQuestion waits for the question the task at ref raises, and answers its id.
func taskQuestion(t *testing.T, srv *Server, ref entity.Ref) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		for _, ent := range entity.Below(srv.Store, mirror.IsQuestion, 1, entity.VerseRoot, mirror.AskSystem) {
			if id := entity.Str(ent.Payload, "task"); id != "" && mirror.Ref(id) == ref {
				return entity.Str(ent.Payload, "id")
			}
		}
		select {
		case <-deadline:
			t.Fatalf("task %s never asked", ref)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A TASK ACTING FOR SOMEBODY ASKS THEM. Its own gate names nobody, so it would be open to every
// person; run for daniel's ask, it is held out to daniel alone — nobody else says go for him.
// The same rule without `as` asks as it always did, openly.
func TestATaskActingForTheAskerAsksThem(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	const daniel, installer = "user:daniel@example.com", "user:installer@example.com"
	askPlace := entity.RefAt("ask")
	if err := entity.Claim(srv.Store, askPlace, installer); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(srv.Store, askPlace, []entity.Grant{{At: askPlace.Segs(), Below: true,
		To: map[entity.Level][]string{entity.OwnerLevel: {daniel}}}}); err != nil {
		t.Fatal(err)
	}
	resp := installAs(t, installer, ts.URL, `[
		{name: for-the-asker, condition: {subject: {path: "ask.*", value: !and [{question: null}, !not {asked: null}]}},
		 action: {spawn: {driver: scripted, prompt: "true", as: subject}, onDone: {patch: {asked: true}}}},
		{name: for-the-rule, condition: {subject: {path: "ask.*", value: !and [{question: null}, !not {ruled: null}]}},
		 action: {spawn: {driver: scripted, prompt: "true"}, onDone: {patch: {ruled: true}}}},
	]`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("install: %s: %s", resp.Status, body)
	}
	resp.Body.Close()
	ask := entity.RefAt("ask", "one")
	if _, _, err := entity.As(srv.Store, daniel).Write(entity.Write{At: ask.At(),
		Value: entity.Obj{"question": "land it?"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	asked := taskFor(t, srv, "for-the-asker", ask.String())
	if got := heldOutTo(t, srv, taskQuestion(t, srv, asked.Ref)); len(got) != 1 || got[0] != daniel {
		t.Errorf("the task acting for daniel asks %v, want daniel alone", got)
	}
	plain := taskFor(t, srv, "for-the-rule", ask.String())
	if got := heldOutTo(t, srv, taskQuestion(t, srv, plain.Ref)); len(got) == 1 && got[0] == daniel {
		t.Errorf("the rule's own task asks daniel alone, as if it acted for him")
	}
}

// taskStatus waits for the task at ref to leave `running`, and answers where it ended.
func taskStatus(t *testing.T, srv *Server, ref entity.Ref) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if e, ok, _ := srv.Store.Get(ref); ok {
			if st := entity.Str(e.Payload, "status"); st != "" && st != "running" {
				return st
			}
		}
		select {
		case <-deadline:
			return "running"
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A TASK ACTING FOR SOMEBODY IS SEEN TO FINISH. Its record is the asker's, and what verse writes
// on it as it runs is written as them: were it written as the pair, the rule's installer would
// have to hold the asker's record too, and the task would stand `running` after it was done.
func TestATaskActingForTheAskerIsSeenToFinish(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	const daniel, installer = "user:daniel@example.com", "user:installer@example.com"
	askPlace := entity.RefAt("ask")
	if err := entity.Claim(srv.Store, askPlace, installer); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(srv.Store, askPlace, []entity.Grant{{At: askPlace.Segs(), Below: true,
		To: map[entity.Level][]string{entity.OwnerLevel: {daniel}}}}); err != nil {
		t.Fatal(err)
	}
	resp := installAs(t, installer, ts.URL, `{name: for-the-asker,
		condition: {subject: {path: "ask.*", value: !and [{question: null}, !not {asked: null}]}},
		action: {spawn: {driver: scripted, prompt: "true", as: subject, vector: auto}, onDone: {patch: {asked: "yes"}}}}`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("install: %s: %s", resp.Status, body)
	}
	resp.Body.Close()
	ask := entity.RefAt("ask", "one")
	if _, _, err := entity.As(srv.Store, daniel).Write(entity.Write{At: ask.At(),
		Value: entity.Obj{"question": "land it?"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	waitField(t, srv, ask, "asked")
	if got := taskStatus(t, srv, taskFor(t, srv, "for-the-asker", ask.String()).Ref); got != "done" {
		t.Errorf("the task acting for daniel ended %q, want done", got)
	}
}
