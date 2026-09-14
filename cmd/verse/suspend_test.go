package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
)

// The words a suspension is made with: a charter's address alone is the charter when one is
// installed there and a subject when none is, `<charter>:<rule>` names a rule, a pair names both,
// and baseline's rule is `:<rule>`. Each reaches the route as the three parts it means, and the
// holder is whoever --as says.
func TestSuspendReadsItsWords(t *testing.T) {
	var got api.Suspend
	var as string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/charter":
			if r.URL.Query().Get("scope") != "" {
				t.Errorf("a package was looked for in scope %q; packages are baseline's", r.URL.Query().Get("scope"))
			}
			if r.URL.Query().Get("at") == "verse-dev.drift" {
				w.Write([]byte(`{writes: "verse-dev.drift.*"}`))
				return
			}
			http.NotFound(w, r)
		case "/suspend", "/resume":
			raw, _ := io.ReadAll(r.Body)
			got = api.Suspend{}
			if err := codec.Unmarshal(raw, &got, codec.Tony); err != nil {
				t.Errorf("decoding %s: %v", r.URL.Path, err)
			}
			as = r.Header.Get(api.PrincipalHeader)
			w.Write([]byte(`{id: x, rule: "*", subject: "*", held: []}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	testAddr = strings.TrimPrefix(ts.URL, "http://")

	for _, c := range []struct {
		args []string
		want api.Suspend
	}{
		{[]string{"verse-dev.drift"}, api.Suspend{Charter: "verse-dev.drift"}},
		{[]string{"demo.pr.1"}, api.Suspend{Charter: "*", Subject: "demo.pr.1"}},
		{[]string{"verse-dev.drift:claim"}, api.Suspend{Charter: "verse-dev.drift", Rule: "claim"}},
		{[]string{"verse-dev.drift:claim", "git.verse.head"}, api.Suspend{Charter: "verse-dev.drift", Rule: "claim", Subject: "git.verse.head"}},
		{[]string{":claim", "git.verse.head"}, api.Suspend{Rule: "claim", Subject: "git.verse.head"}},
		{[]string{"*", "git.verse.*"}, api.Suspend{Charter: "*", Subject: "git.verse.*"}},
	} {
		args := append([]string{"suspend"}, c.args...)
		args = append(args, "--why", "reading it", "--as", "alice@example.com")
		if _, err := runCLI(t, args...); err != nil {
			t.Fatalf("verse %v: %v", args, err)
		}
		c.want.Why = "reading it"
		if got != c.want || as != "user:alice@example.com" {
			t.Fatalf("verse %v sent %+v as %q, want %+v as alice", args, got, as, c.want)
		}
		if _, err := runCLI(t, append([]string{"resume"}, c.args...)...); err != nil {
			t.Fatalf("verse resume %v: %v", c.args, err)
		}
		c.want.Why = ""
		if got != c.want {
			t.Fatalf("verse resume %v sent %+v, want %+v", c.args, got, c.want)
		}
	}
	t.Setenv("VERSE_SCOPE", "me")
	if _, err := runCLI(t, "suspend", "verse-dev.drift", "--why", "from a scope"); err != nil {
		t.Fatalf("verse --scope me suspend verse-dev.drift: %v", err)
	}
	if got.Charter != "verse-dev.drift" {
		t.Fatalf("from a scope, a package's address sent %+v, want it read as the charter", got)
	}
	t.Setenv("VERSE_SCOPE", "")
	if _, err := runCLI(t, "suspend", "verse-dev.drift"); err == nil || !strings.Contains(err.Error(), "--why") {
		t.Fatalf("suspend with no --why: %v", err)
	}
}
