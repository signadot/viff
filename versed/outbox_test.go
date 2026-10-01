package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/trigger"
)

const (
	slackIO  = "agent:slack-io"
	slackOut = "slack.ws.out"
	alice    = "user:alice@example.com"
)

// outboxServer is a verse whose root holds nothing out, with an io whose out half reads
// slack.ws.out as agent:slack-io. Its registration is written straight into the store: what is
// tested here is what a package's declaration does with it, not how an io is installed.
func outboxServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t), WithPerms(Perms{ChildGrants: entity.Levels{}}))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	reg := entity.Obj{
		"name": "slack", "root": "slack",
		"out": entity.Obj{"places": []any{slackOut}, "as": slackIO},
	}.Payload()
	entitytest.Set(t, srv.Store, verseio.Ref("slack"), reg)
	return srv, ts
}

const announce = `{at: demo.announce, sends: [{from: out.slack, to: slack.ws.out}], rules: []}`

// A PACKAGE DECLARES AN OUTBOX, and the io that reads its target is the one that may list it and
// work in it: it reads what is asked there and writes each status beside its input, and does
// nothing else in the package. The package installed without the declaration, it could do neither.
func TestAPackageDeclaresAnOutbox(t *testing.T) {
	srv, ts := outboxServer(t)
	status := "demo.announce.out.slack.hello~status"
	if resp := installAs(t, alice, ts.URL, `{at: demo.announce, rules: []}`); resp.StatusCode >= 300 {
		t.Fatalf("install without sends: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if resp := putAs(t, slackIO, ts.URL, status, entity.Obj{"posted": "x"}.Payload()); resp.StatusCode < 400 {
		t.Fatalf("the io wrote in the package before it declared an outbox: %s", resp.Status)
	}
	if resp := installAs(t, alice, ts.URL, announce); resp.StatusCode >= 300 {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	decl, ok, _ := srv.Store.Get(trigger.PackageRef("demo.announce"))
	if got := trigger.SendsOf(decl.Payload); !ok || len(got) != 1 ||
		got[0].From != "demo.announce.out.slack" || got[0].To != slackOut {
		t.Fatalf("the declaration records %v, want demo.announce.out.slack → %s", got, slackOut)
	}

	// Listed as the charter wrote it, so `verse c -d` writes a file that installs back.
	if resp := getAs(t, alice, ts.URL+"/charter?at=demo.announce"); !strings.Contains(bodyOf(t, resp), "from: out.slack") {
		t.Errorf("the charter's listing does not show its outbox as written")
	}
	resp := getAs(t, slackIO, ts.URL+"/outboxes?to="+slackOut)
	if body := bodyOf(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(body, "demo.announce.out.slack") {
		t.Fatalf("the io listing its outboxes: %s %s", resp.Status, body)
	}
	if resp := getAs(t, alice, ts.URL+"/outboxes?to="+slackOut); resp.StatusCode != http.StatusForbidden {
		t.Errorf("somebody other than the io listed its outboxes: %s", resp.Status)
	}

	if resp := putAs(t, alice, ts.URL, "demo.announce.out.slack.hello", entity.Obj{"to": "#general", "text": "hi"}.Payload()); resp.StatusCode >= 300 {
		t.Fatalf("the package's owner asking in its outbox: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if resp := getAs(t, slackIO, ts.URL+"/state?at=demo.announce.out.slack"); resp.StatusCode != http.StatusOK ||
		!strings.Contains(bodyOf(t, resp), "#general") {
		t.Fatal("the io cannot read what is asked in the outbox")
	}
	if resp := putAs(t, slackIO, ts.URL, status, entity.Obj{"posted": "x"}.Payload()); resp.StatusCode >= 300 {
		t.Fatalf("the io writing a status in the outbox: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if resp := getAs(t, alice, ts.URL+"/state?at="+status); resp.StatusCode != http.StatusOK {
		t.Errorf("the package's owner cannot read what became of its ask: %s", resp.Status)
	}
	if resp := putAs(t, slackIO, ts.URL, "demo.announce.elsewhere", entity.Obj{"x": 1}.Payload()); resp.StatusCode < 400 {
		t.Errorf("the io wrote in the package outside its outbox: %s", resp.Status)
	}
}

// AN OUTBOX GOES WITH ITS DECLARATION: a re-install without it, or the package's removal, takes
// the io's hold back and drops it from the listing.
func TestAnOutboxGoesWithItsDeclaration(t *testing.T) {
	srv, ts := outboxServer(t)
	if resp := installAs(t, alice, ts.URL, announce); resp.StatusCode >= 300 {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if resp := installAs(t, alice, ts.URL, `{at: demo.announce, rules: []}`); resp.StatusCode >= 300 {
		t.Fatalf("re-install without sends: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if got := srv.outboxesTo(slackOut); len(got) != 0 {
		t.Fatalf("a dropped outbox is still listed: %v", got)
	}
	if resp := putAs(t, slackIO, ts.URL, "demo.announce.out.slack.hello~status", entity.Obj{"posted": "x"}.Payload()); resp.StatusCode < 400 {
		t.Errorf("the io still writes in an outbox nobody declares: %s", resp.Status)
	}
}

// WHAT A DECLARATION MAY SAY is checked at install, and a refused one installs nothing.
func TestAnOutboxDeclarationIsChecked(t *testing.T) {
	srv, ts := outboxServer(t)
	for name, c := range map[string]struct{ charter, says string }{
		"no io reads it": {`{at: demo.x, sends: [{from: out, to: mail.out}], rules: []}`, "no io's out place"},
		"a from twice": {`{at: demo.x, sends: [{from: out, to: slack.ws.out}, {from: out, to: slack.ws.out}]}`,
			"sent from twice"},
		"no address":       {`{sends: [{from: out, to: slack.ws.out}], rules: []}`, "belongs to a package"},
		"a key it has not": {`{at: demo.x, sends: [{from: out, to: slack.ws.out, via: x}]}`, "neither"},
		"half an entry":    {`{at: demo.x, sends: [{from: out}]}`, "names both"},
	} {
		resp := installAs(t, alice, ts.URL, c.charter)
		if body := bodyOf(t, resp); resp.StatusCode < 400 || !strings.Contains(body, c.says) {
			t.Errorf("%s: %s %q, want refused saying %q", name, resp.Status, body, c.says)
		}
	}
	// A good declaration in a file refused for something else is put back with the rest.
	resp := installAs(t, alice, ts.URL, `{at: demo.y, sends: [{from: out, to: slack.ws.out}],
		rules: [{name: does-nothing, condition: {subject: {path: "demo.y.*"}}}]}`)
	if resp.StatusCode < 400 {
		t.Fatalf("a file with a rule that does nothing installed: %s", resp.Status)
	}
	if got := srv.outboxesTo(slackOut); len(got) != 0 {
		t.Errorf("a refused declaration left outboxes: %v", got)
	}
	if resp := putAs(t, slackIO, ts.URL, "demo.y.out.m~status", entity.Obj{"posted": "x"}.Payload()); resp.StatusCode < 400 {
		t.Errorf("a refused install left the io holding its outbox: %s", resp.Status)
	}
}
