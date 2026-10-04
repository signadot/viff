package versed

import (
	"net/http"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
)

// AN ASK CARRIES ITS LABELS, whoever raised it: a person's from the verb, and a rule's gate from
// the charter. They are what a resolver matches on besides what the ask is about.
func TestAnAskCarriesItsLabels(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)

	id := askID(t, ask(t, "alice@example.com", ts.URL, `{name: land, question: "land it?", labels: {team: payments, kind: deploy}}`))
	posed(t, srv, mirror.AskRef(id))
	e, _, _ := srv.Store.Get(mirror.AskRef(id))
	if got := mirror.LabelsOf(e.Payload); got["team"] != "payments" || got["kind"] != "deploy" || len(got) != 2 {
		t.Fatalf("a person's ask carries labels %v", got)
	}

	const labelled = `{
	name: land, condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
	gates: [{name: ok, mode: ask, labels: {team: payments}}], action: {patch: {stage: landed}},
}`
	if resp := installAs(t, "alice@example.com", ts.URL, labelled); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	pr := entity.RefAt("demo", "pr", "1")
	owns(t, srv, "user:alice@example.com", nil, pr)
	entitytest.Set(t, srv.Store, pr, entity.Obj{"stage": "open"}.Payload())
	qid := waitRuleQuestion(t, srv, "land", pr.String())
	posed(t, srv, mirror.QuestionRef(qid)) // the record lands after the grants waitRuleQuestion sees
	e, _, _ = srv.Store.Get(mirror.QuestionRef(qid))
	if got := mirror.LabelsOf(e.Payload); got["team"] != "payments" || len(got) != 1 {
		t.Fatalf("a rule's gate carries labels %v", got)
	}
}

// A label a match could not name is refused where it is written: in a charter, at install.
func TestAGateLabelIsCheckedAtInstall(t *testing.T) {
	_, ts := shortKeep(t, askKeepDefault)
	const bad = `{
	name: land, condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
	gates: [{name: ok, mode: ask, labels: {"a b": c}}], action: {patch: {stage: landed}},
}`
	resp := installAs(t, "alice@example.com", ts.URL, bad)
	if msg := bodyOf(t, resp); resp.StatusCode == http.StatusCreated || !strings.Contains(msg, "a label's name is a word") {
		t.Fatalf("a gate label with a space installed: %s: %s", resp.Status, msg)
	}
}
