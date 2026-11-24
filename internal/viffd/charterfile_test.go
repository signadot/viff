package viffd

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// A CHARTER FILE IS ONE FIELDED DOCUMENT: where it goes, what it reads, and its levels,
// schedules and rules, each under its own key — the node `viff c` prints for one address.
func TestAFieldedCharterInstallsAtItsAddress(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	file := `at: notes
reads: [git]
strata:
- {name: seen, level: 0, match: {path: "notes"}}
rules:
- name: note-the-head
  stratum: seen
  condition: {when: {path: "git.head.*"}, subject: {path: "notes.seen.head"}}
  action: {patch: {seen: true}}`
	if code, msg := postCharterFile(t, ts.URL, file); code != 201 {
		t.Fatalf("install = %d: %s", code, msg)
	}
	eng, err := srv.packageEngine("notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := eng.Rule("note-the-head"); !ok {
		t.Error("the rule is not installed at notes")
	}
	if _, ok := eng.Stratum("seen"); !ok {
		t.Error("the level is not installed at notes")
	}
	if got := eng.Reads(); len(got) != 1 {
		t.Errorf("reads = %v, want [git]", got)
	}

	// AN `at:` WITH NO `reads:` DECLARES a package that reads nothing outside itself: the file
	// is the whole charter, and an absent `reads:` means nothing rather than "as it was". So
	// one standing rule that reads git refuses it…
	if code, msg := postCharterFile(t, ts.URL, `{at: notes}`); code != 400 {
		t.Errorf("narrowing under a rule that reads git = %d (%s), want 400", code, msg)
	}
	// …and a package with no such rule reads nothing afterwards.
	if code, msg := postCharterFile(t, ts.URL, `{at: quiet, reads: [git]}`); code != 201 {
		t.Fatalf("declaring = %d: %s", code, msg)
	}
	if code, msg := postCharterFile(t, ts.URL, `{at: quiet}`); code != 201 {
		t.Fatalf("re-declaring = %d: %s", code, msg)
	}
	quiet, err := srv.packageEngine("quiet")
	if err != nil {
		t.Fatal(err)
	}
	if got := quiet.Reads(); len(got) != 0 {
		t.Errorf("reads after a charter with none = %v, want nothing", got)
	}
}

// The refusals a fielded charter can meet, each naming what to change.
func TestAFieldedCharterIsRefusedWhenMalformed(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	rule := `{name: r, condition: {subject: {path: "notes.x.*"}}, action: {patch: {seen: true}}}`
	for _, c := range []struct {
		name, at, file, says string
	}{
		{"an unknown key", "", `{at: notes, rulez: []}`, `"rulez" is none of them`},
		{"reads at baseline", "", `{reads: [git]}`, "belongs to a package"},
		{"a rule among the levels", "", `{at: notes, strata: [` + rule + `]}`, "holds levels"},
		{"a charter inside a list", "notes", `[` + rule + `, {at: notes, rules: []}]`, "a document of its own"},
		{"an at: the path disagrees with", "other", `{at: notes, rules: [` + rule + `]}`, "drop the path"},
		{"two charters at one address", "", "{at: notes}\n---\n{at: notes}", "two charters at \"notes\""},
		{"nothing", "", "", "no charter in the request"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, msg := 0, ""
			if c.at != "" {
				code, msg = installAt(t, ts.URL, c.at, c.file)
			} else {
				code, msg = postCharterFile(t, ts.URL, c.file)
			}
			if code != 400 {
				t.Fatalf("install = %d (%s), want 400", code, msg)
			}
			if !strings.Contains(msg, c.says) {
				t.Errorf("refusal %q does not say %q", msg, c.says)
			}
		})
	}
	if got := srv.packagesSeen(); len(got) != 0 {
		t.Errorf("a refused install left packages standing: %v", got)
	}
}

// THE SHORTHAND STANDS: a rule, a level, or a list of them installs at the path, beside what
// is there, and declares nothing.
func TestTheShorthandInstallsAtThePath(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, msg := postCharterFile(t, ts.URL, `{at: notes, reads: [git]}`); code != 201 {
		t.Fatalf("declaring = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", `[
		{name: seen, level: 0, match: {path: "notes"}},
		{name: r, stratum: seen, condition: {subject: {path: "notes.x.*"}}, action: {patch: {seen: true}}}]`); code != 201 {
		t.Fatalf("shorthand = %d: %s", code, msg)
	}
	eng, err := srv.packageEngine("notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := eng.Rule("r"); !ok {
		t.Error("the shorthand's rule is not installed")
	}
	if got := eng.Reads(); len(got) != 1 {
		t.Errorf("the shorthand changed what the package reads: %v", got)
	}
}
