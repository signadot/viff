package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
)

// charterPath asks GET /charter with a path and answers the body and the status.
func charterPath(t *testing.T, url, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(url + "/charter?path=" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(body))
}

// two charters, one nested, for the paths below to reach into.
func pathFixture(t *testing.T) (*Server, string) {
	t.Helper()
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	if code, msg := installAt(t, ts.URL, "verse-dev.drift", `{
		name: watcher,
		condition: {subject: {path: [verse-dev, drift, *]}},
		action: {patch: {seen: true}},
	}`); code != 201 {
		t.Fatalf("installing verse-dev.drift = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "", `{
		name: base-rule,
		condition: {subject: {path: [demo, pr, *]}},
		action: {patch: {seen: true}},
	}`); code != 201 {
		t.Fatalf("installing baseline = %d: %s", code, msg)
	}
	return srv, ts.URL
}

// A PATH IS AN ADDRESS AND THEN A REACH INTO WHAT STANDS THERE, in one list of segments, which
// is the grammar `verse data demo.pr.1.stage` already has. The split is the server's: it is the
// end that knows the tree.
func TestACharterPathReachesIntoTheNodeAtIt(t *testing.T) {
	_, url := pathFixture(t)
	for _, c := range []struct{ path, want string }{
		{"verse-dev.drift.rules.watcher", "name: watcher"}, // one rule
		{"verse-dev.drift.rules.watcher.action", "seen"},   // …and into it
		{"verse-dev.drift.writes", "verse-dev.drift"},      // a scalar
		{"verse-dev.drift.rules", "watcher"},               // the list
		{"verse-dev", "drift"},                             // a place: the tree under it
		{"rules.base-rule", "name: base-rule"},             // baseline, no address at all
	} {
		code, body := charterPath(t, url, c.path)
		if code != http.StatusOK {
			t.Errorf("path=%s answered %d: %s", c.path, code, body)
			continue
		}
		if !strings.Contains(body, c.want) {
			t.Errorf("path=%s does not mention %q:\n%s", c.path, c.want, body)
		}
	}
}

// ONE STEP IS ONE SEGMENT, so a name that holds a dot reaches no package spelled with two.
// `verse c verse-dev.drift` typed as one word asks about a place called `verse-dev.drift`, and
// the charter installed at `verse-dev` `drift` is somewhere else entirely — which is what the
// segment-wise split says and what a dotted-string comparison could not
// (docs/working/paths-and-nodes.md).
func TestOneSegmentReachesNoTwoSegmentPackage(t *testing.T) {
	srv, url := pathFixture(t)
	if at, rest := srv.splitCharterPath([]string{"verse-dev.drift"}); at != "" || len(rest) != 1 {
		t.Errorf("split of the one segment `verse-dev.drift` = (%q, %v), want no address at all", at, rest)
	}
	if at, rest := srv.splitCharterPath([]string{"verse-dev", "drift"}); at != "verse-dev.drift" || len(rest) != 0 {
		t.Errorf("split of the two segments = (%q, %v), want the package", at, rest)
	}
	// Over the wire, where the quoting is what says which spelling was meant.
	if code, body := charterPath(t, url, neturl.QueryEscape(`"verse-dev.drift"`)); code != http.StatusNotFound {
		t.Errorf("the one-segment address answered %d: %s", code, body)
	}
	if code, body := charterPath(t, url, "verse-dev.drift"); code != http.StatusOK ||
		!strings.Contains(body, "watcher") {
		t.Errorf("the two-segment address answered %d: %s", code, body)
	}
}

// THE ADDRESS IS THE LONGEST PREFIX THAT NAMES A PLACE, so a package whose address ends in a
// document key shadows that key — and `?at=` is what settles it, which is what the CLI's `--`
// will send. Two spellings for one question, the second for what the first cannot express.
func TestAnAddressEndingInAKeyIsSettledByAt(t *testing.T) {
	srv, url := pathFixture(t)
	if code, msg := installAt(t, url, "verse-dev.rules", `{
		name: shadowed,
		condition: {subject: {path: [verse-dev, rules, *]}},
		action: {patch: {seen: true}},
	}`); code != 201 {
		t.Fatalf("installing verse-dev.rules = %d: %s", code, msg)
	}
	// A charter AT `verse-dev` as well, because that is what gives the address above a `rules`
	// KEY to be shadowed: a place holds no keys of its own, so there would be nothing for `--`
	// to reach and nothing to disambiguate.
	if code, msg := installAt(t, url, "verse-dev", `{
		name: above,
		condition: {subject: {path: [verse-dev, x, *]}},
		action: {patch: {seen: true}},
	}`); code != 201 {
		t.Fatalf("installing verse-dev = %d: %s", code, msg)
	}
	// Greedy: the package wins, because it is a longer place than `verse-dev`.
	if at, rest := srv.splitCharterPath([]string{"verse-dev", "rules"}); at != "verse-dev.rules" || len(rest) != 0 {
		t.Errorf("split = (%q, %v), want the package that is installed there", at, rest)
	}
	code, body := charterPath(t, url, "verse-dev.rules")
	if code != http.StatusOK || !strings.Contains(body, "shadowed") {
		t.Errorf("the package at verse-dev.rules did not answer: %d %s", code, body)
	}
	// …and `at=` says where the address ends, so the KEY is reachable.
	resp, err := http.Get(url + "/charter?at=verse-dev&path=rules")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("at= + path= answered %d: %s", resp.StatusCode, got)
	}
	if strings.Contains(string(got), "shadowed") {
		t.Errorf("at=verse-dev&path=rules answered the PACKAGE at verse-dev.rules:\n%s", got)
	}
	if !strings.Contains(string(got), "above") {
		t.Errorf("at=verse-dev&path=rules did not answer verse-dev's own rules:\n%s", got)
	}
}

// WHAT THE OLD `show` SEARCH WAS FOR, said in the refusal. `verse charter show <name>` read the
// whole tree and looked a name up in every charter — a second way to address things, which goes.
// Being told where the thing you named actually is was the useful half, and it costs a sentence.
func TestAMissingPathSaysWhereTheNameIs(t *testing.T) {
	_, url := pathFixture(t)
	code, body := charterPath(t, url, "rules.watcher")
	if code != http.StatusNotFound {
		t.Fatalf("baseline answered %d for a rule it does not hold: %s", code, body)
	}
	for _, want := range []string{"verse-dev.drift", "rule", "watcher"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not mention %q: %s", want, body)
		}
	}
}

// A path whose first step is not a key of a node is a question about a PLACE, and answering
// "baseline holds no nowhere.at.all" describes the wrong failure: nobody asked baseline.
func TestAPathToNoAddressSaysSo(t *testing.T) {
	_, url := pathFixture(t)
	code, body := charterPath(t, url, "nowhere.at.all")
	if code != http.StatusNotFound {
		t.Fatalf("answered %d: %s", code, body)
	}
	if !strings.Contains(body, "no charter is installed at nowhere.at.all") {
		t.Errorf("the refusal is about the wrong thing: %s", body)
	}
}

// A name that is a LEVEL rather than a rule is reached the same way and named correctly when
// it is missing — "a rules called base" is the kind of sentence that reads as a bug.
func TestStrataReachTheSameWay(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	if code, msg := installAt(t, ts.URL, "notes", `{name: lvl, level: 0, match: {path: [notes]}}`); code != 201 {
		t.Fatalf("installing a level = %d: %s", code, msg)
	}
	code, body := charterPath(t, ts.URL, "notes.strata.lvl")
	if code != http.StatusOK || !strings.Contains(body, "name: lvl") {
		t.Fatalf("notes.strata.lvl answered %d: %s", code, body)
	}
	if code, body := charterPath(t, ts.URL, "strata.lvl"); code != http.StatusNotFound {
		t.Fatalf("baseline answered %d for a level it does not hold: %s", code, body)
	} else if !strings.Contains(body, `level called "lvl"`) {
		t.Errorf("the refusal does not call a level a level: %s", body)
	}
	_ = srv
}

// THE PATH IS SPLIT BY THE KPATH PARSER, not by `strings.Split`, and this is what that buys —
// because it is NOT the case it looks like. No charter name may hold a delimiter: a rule or a
// level called `v1.2` is refused at install, since a name is a bare kpath field and a chain
// addresses the action inside a rule (`review.onDone`). So a dot in a path is always a step.
//
// What the parser is for is the other end: somebody who does not know that types the quoting
// anyway, and a wildcard is a thing a person tries once. Both get an answer about the PATH
// rather than a 404 about a rule that was never going to be found under that spelling.
func TestAPathIsSplitByTheGrammar(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	if code, msg := installAt(t, ts.URL, "notes", `{name: watcher, condition: {subject: {path: [notes, *, *]}}, action: {patch: {seen: true}}}`); code != 201 {
		t.Fatalf("installing = %d: %s", code, msg)
	}
	// The premise: a name cannot hold a delimiter, so a dot in a path is unambiguously a step.
	if code, msg := installAt(t, ts.URL, "notes", `{name: "v1.2", level: 0, match: {path: [notes]}}`); code == 201 {
		t.Fatalf("a level named `v1.2` installed — a path segment is ambiguous now: %s", msg)
	}
	// Quoted, by somebody who does not know that: unquoted by the parser, and it resolves.
	if code, body := charterPath(t, ts.URL, `notes.rules."watcher"`); code != http.StatusOK {
		t.Errorf("a quoted segment answered %d: %s", code, body)
	}
	// A wildcard is not a name, and the refusal says so rather than 404ing about a rule.
	code, body := charterPath(t, ts.URL, "notes.rules.*")
	if code != http.StatusBadRequest {
		t.Errorf("a wildcard path answered %d, want a refusal about the path: %s", code, body)
	}
}

// A REMOVAL TAKES THE SAME PATH A READ TAKES, and what goes is said by the path rather than by
// the verb: an address takes the package down, an address plus `rules <name>` takes one rule out.
// They were `uninstall <address>` and `remove <name>` — two verbs differing in what they remove
// rather than in the word, over two coordinate systems.
func TestRemovingTakesTheSamePath(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	install := func(at, def string) {
		t.Helper()
		if code, msg := installAt(t, ts.URL, at, def); code != 201 {
			t.Fatalf("installing at %q = %d: %s", at, code, msg)
		}
	}
	install("notes", `{name: lvl, level: 0, match: {path: [notes]}}`)
	install("notes", `{name: watcher, stratum: lvl, condition: {subject: {path: [notes, seen, *]}}, action: {patch: {seen: true}}}`)

	// One rule out of a package, by path.
	if code, body := deletePath(t, ts.URL, "notes.rules.watcher", false); code != http.StatusNoContent {
		t.Fatalf("removing a rule answered %d: %s", code, body)
	}
	if code, _ := charterPath(t, ts.URL, "notes.rules.watcher"); code != http.StatusNotFound {
		t.Errorf("the rule is still there: %d", code)
	}
	// The level it lived in, which is reachable only now that nothing holds it.
	if code, body := deletePath(t, ts.URL, "notes.strata.lvl", false); code != http.StatusNoContent {
		t.Fatalf("removing a level answered %d: %s", code, body)
	}
	// And the package itself, by a path that ends at the address.
	install("notes", `{name: again, condition: {subject: {path: [notes, seen, *]}}, action: {patch: {seen: true}}}`)
	if code, body := deletePath(t, ts.URL, "notes", false); code != http.StatusNoContent {
		t.Fatalf("uninstalling answered %d: %s", code, body)
	}
	if got := srv.packagesSeen(); len(got) != 0 {
		t.Errorf("the package is still listed: %v", got)
	}
}

// A PATH THAT REACHES INTO SOMETHING IS NOT A REMOVAL. `rules watcher action` reads fine, and
// removing it would mean editing a rule through a URL — which is what re-installing the file is
// for. The refusal says what a removal may name rather than 404ing about the reach.
func TestRemovingRefusesAReachIntoARule(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	if code, msg := installAt(t, ts.URL, "notes", `{name: watcher, condition: {subject: {path: [notes, *, *]}}, action: {patch: {seen: true}}}`); code != 201 {
		t.Fatalf("installing = %d: %s", code, msg)
	}
	code, body := deletePath(t, ts.URL, "notes.rules.watcher.action", false)
	if code != http.StatusBadRequest {
		t.Fatalf("a reach into a rule answered %d, want a refusal: %s", code, body)
	}
	if !strings.Contains(body, "one rule or one level") {
		t.Errorf("the refusal does not say what a removal may name: %s", body)
	}
	if code, _ := charterPath(t, ts.URL, "notes.rules.watcher"); code != http.StatusOK {
		t.Error("the refused removal took something anyway")
	}
}

// deletePath sends DELETE /charter with a path.
func deletePath(t *testing.T, url, path string, recurse bool) (int, string) {
	t.Helper()
	q := url + "/charter?path=" + path
	if recurse {
		q += "&recurse=true"
	}
	req, err := http.NewRequest(http.MethodDelete, q, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(api.PrincipalHeader, tester)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(body))
}

// A REMOVAL AT AN ADDRESS THAT HOLDS NOTHING is a question about a PLACE, and answering
// "nosuchpkg is neither a rule nor a level" describes something nobody asked. It is the split
// the read already makes, and the removal did not.
func TestRemovingAtAnAbsentAddressSaysSo(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	code, body := deletePath(t, ts.URL, "nosuchpkg", false)
	if code != http.StatusNotFound {
		t.Fatalf("answered %d, want a 404 about the address: %s", code, body)
	}
	if !strings.Contains(body, "no charter is installed at nosuchpkg") {
		t.Errorf("the refusal is about the wrong thing: %s", body)
	}
}

// A PLACE ANSWERS WITH ITS CHILDREN AND NOTHING ELSE. `verse-dev` exists because
// `verse-dev.drift` is under it, and every other key in the tree is a fact about a CHARTER —
// so a node with no charter carries none of them.
//
// It used to add `rules: []` and `strata: []` at every node, on the argument that one shape
// everywhere is easier to read. What a reader learned there was false: `rules: []` at a place
// reads as a charter with no rules, which is a state a package really can be in — its levels
// installed and its rules refused — and the only thing telling the two apart was the ABSENCE
// of `writes`.
func TestAPlaceAnswersWithItsChildrenAlone(t *testing.T) {
	_, url := pathFixture(t)
	code, body := charterPath(t, url, "verse-dev")
	if code != http.StatusOK {
		t.Fatalf("verse-dev answered %d: %s", code, body)
	}
	if !strings.Contains(body, "children:") || !strings.Contains(body, "drift") {
		t.Fatalf("a place did not answer with what is under it:\n%s", body)
	}
	for _, k := range []string{"rules:", "strata:", "writes:"} {
		if strings.HasPrefix(strings.TrimSpace(body), k) {
			t.Errorf("a place claims %s, which is a fact about a charter:\n%s", k, body)
		}
	}
	// …while the charter under it carries all of them.
	code, body = charterPath(t, url, "verse-dev.drift")
	if code != http.StatusOK {
		t.Fatalf("verse-dev.drift answered %d: %s", code, body)
	}
	for _, k := range []string{"writes:", "rules:", "strata:"} {
		if !strings.Contains(body, k) {
			t.Errorf("a charter does not carry %s:\n%s", k, body)
		}
	}
}
