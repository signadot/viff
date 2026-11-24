package viffd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.viff.dev/viff/internal/trigger/charter"
	"go.viff.dev/viff/pkg/runner/runnerd/runnerdtest"

	"go.viff.dev/viff/internal/trigger"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	viffio "go.viff.dev/viff/pkg/io"
)

const (
	slackIO  = "agent:slack-iod"
	slackOut = "slack.ws.out"
	alice    = "user:alice@example.com"
)

// outboxServer is a viff whose root holds nothing out, with an io whose out half reads
// slack.ws.out as agent:slack-iod. Its registration is written straight into the store: what is
// tested here is what a package's declaration does with it, not how an io is installed.
func outboxServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t), WithPerms(Perms{ChildGrants: entity.Levels{}}))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	reg := entity.Obj{
		"name": "slack", "root": "slack",
		"out": entity.Obj{"places": []any{slackOut}, "as": slackIO},
	}.Payload()
	entitytest.Set(t, srv.Store, viffio.Ref("slack"), reg)
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
	decl, ok, _ := srv.Store.Get(charter.PackageRef("demo.announce"))
	if got := trigger.SendsOf(decl.Payload); !ok || len(got) != 1 ||
		got[0].From != "demo.announce.out.slack" || got[0].To != slackOut {
		t.Fatalf("the declaration records %v, want demo.announce.out.slack → %s", got, slackOut)
	}

	// Listed as the charter wrote it, so `viff c -d` writes a file that installs back.
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
		"a from inside another": {`{at: demo.x, sends: [{from: out, to: slack.ws.out}, {from: out.linear, to: slack.ws.out}]}`,
			"inside demo.x.out"},
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

// AN OUTBOX FOLLOWS ITS IO OUT AND BACK IN: the io's removal takes back the grant on every outbox
// sending to it, and installing it again grants them, with the package left as it was.
func TestAnOutboxFollowsItsIO(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t), WithPerms(Perms{ChildGrants: entity.Levels{}}))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	const ana = "user:ana@example.com"
	io := map[string]any{
		"name": "workspace", "root": "slack", "url": "http://slack-iod:9801",
		"owns": []any{map[string]any{"at": "slack.ws"}},
		"out":  map[string]any{"places": []any{slackOut}, "as": slackIO},
	}
	if resp := installIO(t, ts, ana, io); resp.StatusCode != http.StatusCreated {
		t.Fatalf("io install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if resp := installAs(t, alice, ts.URL, announce); resp.StatusCode >= 300 {
		t.Fatalf("package install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	// A FRESH NAME each time: the io owns a status it created, so rewriting one asks nothing of the
	// outbox's grant.
	n := 0
	answers := func() bool {
		n++
		status := entity.RefAt("demo", "announce", "out", "slack", fmt.Sprintf("m%d%s", n, viffio.StatusSuffix))
		return write(srv.Store, slackIO, status, entity.Obj{"posted": "x"}) == nil
	}
	if !answers() {
		t.Fatal("the io could not answer in the outbox it was declared")
	}
	if resp := deleteAs(t, ana, ts.URL+"/iods/workspace"); resp.StatusCode >= 300 {
		t.Fatalf("io removal: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if answers() {
		t.Fatal("the io's grant in the outbox outlived the io")
	}
	if resp := installIO(t, ts, ana, io); resp.StatusCode != http.StatusCreated {
		t.Fatalf("io re-install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if !answers() {
		t.Fatal("the io installed again cannot answer in the outbox still declared for it")
	}
}

// AN OUTBOX'S GRANT IS ITS OWN LINE (git-issue ne9fce9dh12ks1q4qsn0): held twice and taken back
// once, the io holds nothing there, and taking it back takes nothing else the place holds out — a
// line the installer wrote there for somebody else still stands.
func TestAnOutboxTakesBackOnlyItsOwnGrant(t *testing.T) {
	srv, ts := outboxServer(t)
	for range 2 {
		if resp := installAs(t, alice, ts.URL, announce); resp.StatusCode >= 300 {
			t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
		}
	}
	box := entity.RefAt("demo", "announce", "out", "slack")
	input := entity.RefAt("demo", "announce", "out", "slack", "hello")
	if err := write(srv.Store, alice, input, entity.Obj{"to": "C1", "text": "hi"}); err != nil {
		t.Fatalf("the installer could not ask in the outbox: %v", err)
	}
	const bob = "user:bob@example.com"
	if err := entity.AddGrant(srv.Store, box, entity.Grant{At: entity.SegsOf(box), Below: true,
		To: entity.Levels{entity.Read: {bob}}}); err != nil {
		t.Fatal(err)
	}

	if resp := installAs(t, alice, ts.URL, `{at: demo.announce, rules: []}`); resp.StatusCode >= 300 {
		t.Fatalf("re-install without sends: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if _, ok, err := entity.As(srv.Store, slackIO).Get(input); err == nil && ok {
		t.Error("the io still reads the outbox: a second hold stacked, or the release took nothing")
	}
	if _, ok, err := entity.As(srv.Store, bob).Get(input); err != nil || !ok {
		t.Errorf("the installer's own grant went with the outbox's: ok %v, %v", ok, err)
	}
}
