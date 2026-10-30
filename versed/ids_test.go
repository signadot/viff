package versed

import (
	"github.com/signadot/verse/runner/runnerd/runnerdtest"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// A rule is (scope, charter address, name), and every engine shares one registry: baseline's
// `review`, a package's `review` and a scope's `review` about one subject ask three questions,
// not one question three waiters have joined. Withdrawing one of them from its scope has to take
// that one off the docket and leave the others standing, which a shared id cannot do.
func TestSameNamedRulesInThreeEnginesAskThreeQuestions(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	withoutTheDocketDefault(t, srv)

	subject := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, srv.Store, subject, entity.Obj{"stage": "open"}.Payload())
	rule := `{
		name: review,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		gates: [{name: ok, mode: ask}],
		action: {patch: {stage: reviewing}},
	}`
	addRule(t, ts, rule)
	addScopedRule(t, ts, "me", rule)
	if code, msg := declareReads(t, ts.URL, "notes", "demo"); code != 201 {
		t.Fatalf("declaring reads = %d: %s", code, msg)
	}
	// A package may only read a subject outside its address, so its review derives inside it,
	// which puts the rule in a level below what it derives.
	for _, def := range []string{
		`{name: prs, level: 0, match: {path: "demo"}}`,
		`{name: notes, level: 10, match: {path: "notes"}}`,
		strings.Replace(strings.Replace(rule, "patch: {stage: reviewing}",
			`derive: [{path: "notes.reviewed", value: {seen: yes}}]`, 1), "name: review,", "name: review, stratum: prs,", 1),
	} {
		if code, msg := installAt(t, ts.URL, "notes", def); code != 201 {
			t.Fatalf("installing the package = %d: %s", code, msg)
		}
	}

	var ids []string
	deadline := time.After(3 * time.Second)
	for len(ids) < 3 {
		ids = ids[:0]
		for _, p := range srv.Reg.Pending() {
			if p.Question.Origin == "review" && p.Question.Subject == subject.String() {
				ids = append(ids, p.Question.ID)
			}
		}
		select {
		case <-deadline:
			t.Fatalf("questions about %s from the three reviews = %v, want three", subject, ids)
		case <-time.After(5 * time.Millisecond):
		}
	}
	sort.Strings(ids)
	want := []string{
		"me::verse.trigger.review.ok~",
		"notes:verse.trigger.review.ok~",
		"verse.trigger.review.ok~",
	}
	for i, id := range ids {
		if !strings.HasPrefix(id, want[i]) {
			t.Fatalf("question ids = %v; want them to lead %v", ids, want)
		}
	}
}
