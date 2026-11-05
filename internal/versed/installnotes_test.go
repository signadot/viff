package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/codec"
)

// installFile posts a charter file as the tester and returns what the install answered, decoded.
func installFile(t *testing.T, url, file string) []map[string]any {
	t.Helper()
	resp := postAs(t, tester, url+"/charter", "application/tony", file)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s %s", resp.Status, raw)
	}
	var out struct {
		Installed []map[string]any `tony:"field=installed"`
	}
	if err := codec.Unmarshal(raw, &out, codec.Tony); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	return out.Installed
}

// count reads a number out of a decoded answer, whatever width it decoded to.
func count(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return -1
}

func text(v any) string {
	s, _ := v.(string)
	return s
}

func charterListing(t *testing.T, url string) string {
	t.Helper()
	resp := getAs(t, tester, url+"/charter")
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}

// A rule naming a stratum that is not installed is held, and the install SAYS so to whoever
// sent it: it is counted out of the rules installed, named with the stratum it waits for, and
// listed in the charter marked held. A misspelled `stratum:` is this, and it read "installed 1".
func TestAHeldRuleIsSaidToItsInstaller(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	got := installFile(t, ts.URL, `{
		name: waits,
		stratum: sdcl,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {patch: {stage: reviewed}},
	}`)
	if len(got) != 1 {
		t.Fatalf("answer = %v, want one package", got)
	}
	if n := count(got[0]["rules"]); n != 0 {
		t.Errorf("rules = %d, want 0: the one rule is held and can fire on nothing", n)
	}
	held, _ := got[0]["held"].([]any)
	if len(held) != 1 {
		t.Fatalf("held = %v, want the rule named", got[0]["held"])
	}
	if h, _ := held[0].(map[string]any); h["rule"] != "waits" || h["waits"] != "waiting for stratum sdcl" || h["arming"] != nil {
		t.Errorf("held = %v, want rule waits, waiting for stratum sdcl, and not arming", held[0])
	}
	if listing := charterListing(t, ts.URL); !strings.Contains(listing, "name: waits") ||
		!strings.Contains(listing, `held: "waiting for stratum sdcl"`) {
		t.Errorf("the charter does not list the held rule as held:\n%s", listing)
	}

	// The stratum arrives: the rule installs, and is listed as any other.
	got = installFile(t, ts.URL, `{name: sdcl, level: 0, match: "demo"}`)
	if got[0]["held"] != nil {
		t.Errorf("installing the stratum: held = %v", got[0]["held"])
	}
	if listing := charterListing(t, ts.URL); !strings.Contains(listing, "name: waits") ||
		strings.Contains(listing, "held:") {
		t.Errorf("the rule is still listed as held once its stratum is in:\n%s", listing)
	}
}

// A file that carries a rule and the stratum it names holds nothing: what the author is told is
// how things stand when the install ends.
func TestARuleWhoseStratumIsInTheFileIsNotHeld(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	got := installFile(t, ts.URL, `- {
  name: ordered,
  stratum: sdlc,
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
  action: {patch: {stage: reviewed}},
}
- {name: sdlc, level: 0, match: "demo"}
`)
	if got[0]["held"] != nil || count(got[0]["rules"]) != 1 || count(got[0]["strata"]) != 1 {
		t.Fatalf("answer = %v, want one rule, one stratum, none held", got)
	}
}

// An advisory is said to whoever sent the file, with the rule it is about: a gate whose refusal
// leaves the subject where it was is legal, and is installed, and re-asks its question.
func TestAnAdvisoryIsSaidToItsInstaller(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	got := installFile(t, ts.URL, `{
		name: asks-again,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		gates: [{name: approval, mode: ask}],
		action: {patch: {stage: reviewed}},
	}`)
	if n := count(got[0]["rules"]); n != 1 {
		t.Errorf("rules = %d, want 1: an advised rule is installed", n)
	}
	notes, _ := got[0]["notes"].([]any)
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want the advisory", got[0]["notes"])
	}
	n, _ := notes[0].(map[string]any)
	if n["rule"] != "asks-again" || !strings.Contains(text(n["note"]), "onDenied") {
		t.Errorf("note = %v, want the rule and what to write", n)
	}

	// A rule with nothing to say says nothing.
	got = installFile(t, ts.URL, `{
		name: plain,
		condition: {subject: {path: "demo.task.*", value: {priority: high}}},
		action: {patch: {priority: escalated}},
	}`)
	if got[0]["notes"] != nil || got[0]["held"] != nil {
		t.Errorf("a plain rule: %v", got[0])
	}
}
