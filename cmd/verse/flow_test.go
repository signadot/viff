package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
)

// flowServer is a verse that records the flow verbs it is sent and answers the listing with rows.
func flowServer(t *testing.T, rows []api.FlowRow) (got func() (string, api.FlowSelect, string), query func() string) {
	t.Helper()
	var path, as, q string
	var sel api.FlowSelect
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/flows":
			q = r.URL.RawQuery
			doc, err := codec.Marshal(rows, codec.Tony)
			if err != nil {
				t.Error(err)
			}
			w.Write(doc)
		case strings.HasPrefix(r.URL.Path, "/flow/"):
			raw, _ := io.ReadAll(r.Body)
			sel = api.FlowSelect{}
			if err := codec.Unmarshal(raw, &sel, codec.Tony); err != nil {
				t.Errorf("decoding %s: %v", r.URL.Path, err)
			}
			path, as = r.URL.Path, r.Header.Get(api.PrincipalHeader)
			w.Write([]byte(`{kind: task}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	testAddr = strings.TrimPrefix(ts.URL, "http://")
	return func() (string, api.FlowSelect, string) {
			p, s, a := path, sel, as
			path, sel, as = "", api.FlowSelect{}, ""
			return p, s, a
		}, func() string {
			return q
		}
}

// The verbs are addressed by the flags the listing takes, and those reach the wire as the
// selection they spell; the holder is whoever --as says.
func TestFlowVerbsSendTheirFlags(t *testing.T) {
	got, query := flowServer(t, nil)
	for _, c := range []struct {
		args []string
		path string
		want api.FlowSelect
	}{
		{[]string{"suspend", "--charter", "verse-dev.drift", "--subject", "*", "--why", "reading it"},
			"/flow/suspend", api.FlowSelect{Charter: "verse-dev.drift", Subject: "*", Why: "reading it"}},
		{[]string{"suspend", "--charter", "verse-dev", "-r", "--subject", `github.signadot.verse.pr."42"`, "--why", "a PR"},
			"/flow/suspend", api.FlowSelect{Charter: "verse-dev", Recursive: true, Subject: `github.signadot.verse.pr."42"`, Why: "a PR"}},
		{[]string{"suspend", "--task", "review@demo.pr.1"}, "/flow/suspend", api.FlowSelect{Task: "review@demo.pr.1"}},
		{[]string{"resume", "--charter", ":claim", "--subject", "git.verse.head"},
			"/flow/resume", api.FlowSelect{Charter: ":claim", Subject: "git.verse.head"}},
		{[]string{"cancel", "--task", "review@demo.pr.1"}, "/flow/cancel", api.FlowSelect{Task: "review@demo.pr.1"}},
		{[]string{"say", "--subject", "demo.pr.*", "look", "again"},
			"/flow/say", api.FlowSelect{Subject: "demo.pr.*", Text: "look again", By: "user:alice@example.com"}},
	} {
		args := append(append([]string{"flow"}, c.args...), "--as", "alice@example.com")
		if _, err := runCLI(t, args...); err != nil {
			t.Fatalf("verse %v: %v", args, err)
		}
		path, sel, as := got()
		if path != c.path || sel != c.want || as != "user:alice@example.com" {
			t.Fatalf("verse %v sent %s %+v as %q, want %s %+v as alice", args, path, sel, as, c.path, c.want)
		}
	}
	if _, err := runCLI(t, "flow", "--charter", "verse-dev.drift", "-r"); err != nil {
		t.Fatal(err)
	}
	q, err := url.ParseQuery(query())
	if err != nil || q.Get("charter") != "verse-dev.drift" || q.Get("recursive") != "true" || q.Has("subject") || q.Has("task") {
		t.Fatalf("the listing asked %q, want the flags as its query", query())
	}
}

// A verb that does not say what it acts on prints its usage and sends nothing: nothing is
// "every" by being left out.
func TestAFlowVerbNarrowsOrIsRefused(t *testing.T) {
	got, _ := flowServer(t, nil)
	for _, args := range [][]string{
		{"suspend"},
		{"suspend", "--charter", "verse-dev.drift", "--why", "no subject"},
		{"suspend", "--charter", "verse-dev.drift", "--subject", "*"}, // no --why
		{"resume", "--subject", "demo.pr.1"},
		{"say", "hello"},
		{"cancel", "--subject", "demo.pr.1"},
		{"suspend", "--task", "x", "--subject", "demo.pr.1"},
		{"resume", "demo.pr.1"},
		{"resume", "--args", "--task", "x"},
	} {
		_, err := runCLI(t, append([]string{"flow"}, args...)...)
		if !errors.Is(err, cli.ErrUsage) {
			t.Errorf("verse flow %v = %v, want a usage error", args, err)
		}
		if path, _, _ := got(); path != "" {
			t.Errorf("verse flow %v sent %s", args, path)
		}
	}
}

// `--args` prints each row as the flags that name it, and a line of it is read back by xargs as
// the words it was printed from — a subject holding quotes and a space included — so `verse flow
// --args | xargs -L1 verse flow resume` acts on what was listed.
func TestFlowArgsSurviveXargs(t *testing.T) {
	xargs, err := exec.LookPath("xargs")
	if err != nil {
		t.Skip("no xargs on this machine")
	}
	subject := `demo.pr."it's 42"`
	flowServer(t, []api.FlowRow{
		{Kind: api.FlowRowTask, Task: "review@demo.pr.1", Subject: "demo.pr.1"},
		{Kind: api.FlowRowSuspension, ID: "x", Charter: "verse-dev.drift:claim", Recursive: true, Subject: subject},
	})
	out, err := runCLI(t, "flow", "--args")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(xargs, "-L1", "printf", `%s|`)
	cmd.Stdin = strings.NewReader(out)
	read, err := cmd.Output()
	if err != nil {
		t.Fatalf("xargs over %q: %v", out, err)
	}
	want := "--task|review@demo.pr.1|" + "--charter|verse-dev.drift:claim|--recursive|--subject|" + subject + "|"
	if string(read) != want {
		t.Fatalf("xargs read\n%s\nas %q, want %q", out, read, want)
	}
}
