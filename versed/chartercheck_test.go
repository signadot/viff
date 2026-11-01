package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/trigger"
)

const checkedPackage = `at: repos.r.hello
strata:
- {name: low, level: 0, match: {path: repos.r.hello.low}}
- {name: high, level: 1, match: {path: repos.r.hello.high}}
rules:
- name: lift
  stratum: low
  condition: {subject: {path: "repos.r.hello.low.*"}}
  action:
    derive:
    - {path: repos.r.hello.high, id: seen, value: {seen: true}}
`

// checkAt posts docs to the charter check at an address, as user, and reads its answer. query is
// any more of the check's query, as runners=to-come.
func checkAt(t *testing.T, ts *httptest.Server, at, docs string, query ...string) api.CharterCheck {
	t.Helper()
	q := "?at=" + at + "&format=tony"
	for _, kv := range query {
		q += "&" + kv
	}
	resp := postAs(t, "user:"+tester, ts.URL+"/charter/check"+q, "application/tony", docs)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("check = %s: %s", resp.Status, body)
	}
	var out api.CharterCheck
	if err := codec.Unmarshal([]byte(body), &out, codec.Tony); err != nil {
		t.Fatalf("reading %q: %v", body, err)
	}
	return out
}

// THE CHARTER CHECK SAYS WHAT AN INSTALL WOULD DO, AND DOES NONE OF IT. A tree that would
// install says `installs` and what it adds; nothing is installed by asking; once it is
// installed, a change says what it changes and what the prune would take; a tree that would be
// refused says `refused` with the install's own words.
func TestTheCharterCheckSaysWhatAnInstallWouldDo(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	describeLocal(t, srv, offering()) // the local runner: scripts only, as a keyless `verse up`'s
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	got := checkAt(t, ts, "repos.r", checkedPackage)
	if got.Verdict != "installs" || len(got.Packages) != 1 || got.Packages[0].At != "repos.r.hello" ||
		strings.Join(got.Packages[0].Added, ",") != "rules.lift,strata.high,strata.low" {
		t.Fatalf("a tree that would install: %+v", got)
	}
	if _, ok := srv.standingPackage(mustAt(t, "repos.r.hello")); ok {
		t.Fatal("the check installed the package, or made an engine for it")
	}

	installCharter(t, ts, "", checkedPackage)
	changed := strings.Replace(checkedPackage, "value: {seen: true}", "value: {seen: yes}", 1)
	other := "---\nat: repos.r.other\nrules:\n- {name: note, condition: {subject: {path: \"repos.r.other.*\"}}, action: {patch: {noted: true}}}\n"
	got = checkAt(t, ts, "repos.r", changed+other)
	if got.Verdict != "installs" || len(got.Packages) != 2 {
		t.Fatalf("a change: %+v", got)
	}
	for _, p := range got.Packages {
		switch p.At {
		case "repos.r.hello":
			if strings.Join(p.Changed, ",") != "rules.lift" || len(p.Added) != 0 || len(p.Removed) != 0 {
				t.Errorf("the changed package: %+v", p)
			}
		case "repos.r.other":
			if strings.Join(p.Added, ",") != "rules.note" {
				t.Errorf("the new package: %+v", p)
			}
		}
	}
	// A tree that leaves hello out would prune it.
	if got := checkAt(t, ts, "repos.r", other[len("---\n"):]); got.Verdict != "installs" || strings.Join(got.Pruned, ",") != "repos.r.hello" {
		t.Errorf("a tree leaving a package out: %+v", got)
	}

	// Refused, in the install's words: a derive sideways.
	sideways := strings.Replace(checkedPackage, "path: repos.r.hello.high, id: seen", "path: repos.r.hello.low, id: seen", 1)
	if got := checkAt(t, ts, "repos.r", sideways); got.Verdict != "refused" || !strings.Contains(got.Refusal, "lift") {
		t.Fatalf("a sideways derive: %+v", got)
	}
	// And outside the address the tree is checked at.
	if got := checkAt(t, ts, "repos.elsewhere", checkedPackage); got.Verdict != "refused" {
		t.Fatalf("a package outside the address: %+v", got)
	}
	// And a spawn whose agent no runner offers, which the install refuses: the check said
	// `installs` for it, and the install it spoke for was refused.
	spawns := "at: repos.r.work\nrules:\n- name: work\n  condition: {subject: {path: \"repos.r.work.*\", value: {stage: todo}}}\n" +
		"  action:\n    spawn: {agent: {name: claude}, prompt: go}\n    onDone: {patch: {stage: done}}\n"
	if got := checkAt(t, ts, "repos.r", checkedPackage+"---\n"+spawns); got.Verdict != "refused" ||
		!strings.Contains(got.Refusal, "no runner offers the agent claude") {
		t.Fatalf("a spawn no runner offers: %+v", got)
	}
	// Asked to vet as a restore places a rule, for a verse whose runners are not up yet, the
	// check holds it for a runner instead, and says so.
	if got := checkAt(t, ts, "repos.r", checkedPackage+"---\n"+spawns, "runners=to-come"); got.Verdict != "installs" ||
		len(got.Packages) != 2 || strings.Join(got.Packages[1].Held, ",") != "work" {
		t.Fatalf("a spawn no runner offers, its runner to come: %+v", got)
	}
	resp := postAs(t, "user:"+tester, ts.URL+"/charter/check?at=repos.r&runners=sometime", "application/tony", checkedPackage)
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "checked or to-come") {
		t.Fatalf("runners=sometime = %s: %s; want it refused", resp.Status, body)
	}
	// And an outbox to no io.
	sends := "at: repos.r.out\nreads: []\nsends: [{from: o, to: slack.Nowhere.out}]\n"
	if got := checkAt(t, ts, "repos.r", checkedPackage+"---\n"+sends); got.Verdict != "refused" || !strings.Contains(got.Refusal, "no io's out place") {
		t.Fatalf("an outbox to no io: %+v", got)
	}
}

func mustAt(t *testing.T, at string) trigger.Subtree {
	t.Helper()
	st, err := trigger.ParseAt(at)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
