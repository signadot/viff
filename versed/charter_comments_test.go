package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A charter installed over HTTP comes back with the comments it was written with — the
// operator's own path, `verse c -a -f rule.tony` then `verse charter show`.
//
// It is worth a test at this level because the loss was invisible from either end: the
// document arrives whole (the wire is tony and the parser is asked for comments), the rule
// carries them through its decode, and what dropped them was the WRITE — `!raw` landing on the
// comment wrapper rather than on the value it wraps, which kept the head comments and ate the
// line ones. Each layer's own tests passed.
func TestATriggerInstalledOverHTTPKeepsItsComments(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	addRule(t, ts, "# test comments in a trigger\n"+
		"action:\n"+
		"  patch:\n"+
		"    commented: true # the flag it sets\n"+
		"condition:\n"+
		"  subject:\n"+
		"    system: comment\n"+
		"name: comment # this is a comment\n")

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/charter/comment", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/tony")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("trigger show: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		"# test comments in a trigger", // above the rule
		"# the flag it sets",           // inside the patch, beside a value
		"# this is a comment",          // beside a field of the rule itself
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the installed rule came back without %q:\n%s", want, body)
		}
	}
}
