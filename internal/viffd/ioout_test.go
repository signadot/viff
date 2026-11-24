package viffd

import (
	"fmt"
	stdio "io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	viffio "go.viff.dev/viff/pkg/io"
)

// An io that acts says so on its registration, and the install stands its place up: the askers may
// ask, the io owns what it answers, and everyone may read both. What an operator used to type as
// four `viff perms` lines is written from the record, where it cannot be typed wrong.

// installIO registers one io with an out half, as installer, and answers the response.
func installIO(t *testing.T, ts *httptest.Server, as string, body map[string]any) *http.Response {
	t.Helper()
	b, err := codec.Marshal(body, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return postAs(t, as, ts.URL+"/iods?probe=false", "application/tony", string(b))
}

func TestAnIoWithAnOutHalfHasItsGrantsWritten(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const installer = "user:ana@example.com"
	resp := installIO(t, ts, installer, map[string]any{
		"name": "workspace", "root": "slack", "url": "http://slack-iod:9801",
		"owns": []any{map[string]any{"at": "slack.Acme"}},
		"out": map[string]any{
			"places":     []any{"slack.Acme.out"},
			"as":         "agent:slack-iod",
			"requesters": []any{"user:bo@example.com"},
		},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install = %s", resp.Status)
	}

	// What the grants ARE is the spelling; what they DO is the contract, so the test does what
	// the four parties would do.
	input := entity.RefAt("slack", "Acme", "out", "U1", "hello")
	status := entity.RefAt("slack", "Acme", "out", "U1", "hello"+viffio.StatusSuffix)
	asker, io, stranger := "user:bo@example.com", "agent:slack-iod", "user:cara@example.com"

	if err := write(srv.Store, asker, input, entity.Obj{"text": "say this"}); err != nil {
		t.Errorf("an asker could not ask: %v", err)
	}
	if _, ok, err := entity.As(srv.Store, io).Get(input); err != nil || !ok {
		t.Errorf("the io could not read what was asked: ok %v, %v", ok, err)
	}
	if err := write(srv.Store, io, status, entity.Obj{"ts": "1.1"}); err != nil {
		t.Errorf("the io could not answer: %v", err)
	}
	// The status is the io's alone: the asker's grant from above stops at the owner record the
	// io's own write left on it.
	if err := write(srv.Store, asker, status, entity.Obj{"ts": "forged"}); err == nil {
		t.Error("an asker rewrote the io's status")
	}
	// And everyone may read it, which is what the child default holds out.
	if _, ok, err := entity.As(srv.Store, stranger).Get(status); err != nil || !ok {
		t.Errorf("a reader could not read the status: ok %v, %v", ok, err)
	}
}

// write is one write as somebody, for a test that asks what a grant DOES.
func write(store entity.Store, as string, at entity.Ref, o entity.Obj) error {
	_, _, err := entity.As(store, as).Write(entity.Write{At: at.At(), Value: o.Payload()})
	return err
}

// The registration is checked where the operator is present, not discovered where it is used.
func TestAnOutHalfIsCheckedAtInstall(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, c := range []struct {
		name string
		out  map[string]any
		want string
	}{
		{"no places", map[string]any{"as": "agent:slack-iod"}, "out.places is required"},
		{"nobody to be", map[string]any{"places": []any{"slack.Acme.out"}}, "out.as is required"},
		{"a principal the grammar refuses", map[string]any{
			"places": []any{"slack.Acme.out"}, "as": "slack-iod"}, "out.as"},
		{"a place outside the claim", map[string]any{
			"places": []any{"elsewhere.out"}, "as": "agent:slack-iod"}, "outside what this io claims"},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := installIO(t, ts, "user:ana@example.com", map[string]any{
				"name": "workspace-" + strings.ReplaceAll(c.name, " ", "-"),
				"root": "slack", "url": "http://slack-iod:9801",
				"owns": []any{map[string]any{"at": "slack.Acme"}},
				"out":  c.out,
			})
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("install = %s, want 400", resp.Status)
			}
			if said := read(t, resp); !strings.Contains(said, c.want) {
				t.Errorf("refusal %q, want it to say %q", said, c.want)
			}
		})
	}
}

// An io that only reflects says nothing about an out half, and nothing is seeded for it.
func TestAnIoThatOnlyReflectsSeedsNothing(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := installIO(t, ts, "user:ana@example.com", map[string]any{
		"name": "cluster", "root": "git", "url": "http://git-iod:9801",
		"owns": []any{map[string]any{"at": "git"}},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install = %s", resp.Status)
	}
	e, ok, err := srv.Store.Get(viffio.Ref("cluster"))
	if err != nil || !ok {
		t.Fatalf("the registration: ok %v, %v", ok, err)
	}
	if entity.Get(e.Payload, "out") != nil {
		t.Errorf("registration carries an out half it was not given: %v", entity.ToObj(e.Payload))
	}
}

// read is what the server said, for a refusal a test asserts on.
func read(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := stdio.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The grants go with the registration that described them: left standing, they would hold a place
// open to askers for an io that no longer exists.
func TestRemovingAnIoTakesItsOutGrantsBack(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := installIO(t, ts, "user:ana@example.com", map[string]any{
		"name": "workspace", "root": "slack", "url": "http://slack-iod:9801",
		"owns": []any{map[string]any{"at": "slack.Acme"}},
		"out": map[string]any{
			"places": []any{"slack.Acme.out"}, "as": "agent:slack-iod",
			"requesters": []any{"user:bo@example.com"},
		},
	})
	resp.Body.Close()

	input := entity.RefAt("slack", "Acme", "out", "U1", "hello")
	if err := write(srv.Store, "user:bo@example.com", input, entity.Obj{"text": "say this"}); err != nil {
		t.Fatalf("an asker could not ask: %v", err)
	}

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/iods/workspace?forget=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Viff-Principal", "user:ana@example.com")
	gone, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	gone.Body.Close()
	if gone.StatusCode != http.StatusOK {
		t.Fatalf("remove = %s", gone.Status)
	}
	// A --forget says the io is over, so what it claimed is tombstoned — the out place is inside
	// that claim, so the requests go with it, which is the honest outcome for requests nothing
	// will ever act on.
	if _, ok, _ := entity.As(srv.Store, "system:default").Get(input); ok {
		t.Error("what was asked of a forgotten io still stands")
	}
	// And the grants went with the registration that described them, rather than standing as a
	// door held open for an io that no longer exists. Asked of the record rather than of a write,
	// because the place itself is gone: with nothing standing there, what a write may do is the
	// root's default to decide and not this removal's.
	rec, ok, err := srv.Store.Get(entity.PermsRef(entity.RefAt("slack", "Acme", "out")))
	if err != nil {
		t.Fatalf("reading what the place holds out: %v", err)
	}
	if ok {
		if lines := entity.Get(rec.Payload, "grants"); lines != nil && len(lines.Values) > 0 {
			t.Errorf("the io's grants still stand: %v", entity.ToObj(rec.Payload))
		}
		if entity.Get(rec.Payload, "child-grants") != nil {
			t.Errorf("what the place held out still stands: %v", entity.ToObj(rec.Payload))
		}
	}
}

// Viff dials a reflection and knows how it is doing; it never dials an out half, so the out half
// tells it. Without that, a listing shows one half of an io and calls it the io.
//
// WHAT THE PASS DID IS SCRATCH'S, not the document's: when it last passed and what it did is an
// opinion about now, so it costs no commit, and the record keeps only the out half's config and
// the failure that stopped a pass — written when the failure begins and when it ends, and not
// for a pass that repeats either.
func TestAnOutHalfReportsItsPasses(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := installIO(t, ts, "user:ana@example.com", map[string]any{
		"name": "workspace", "root": "slack", "url": "http://slack-iod:9801",
		"owns": []any{map[string]any{"at": "slack.Acme"}},
		"out":  map[string]any{"places": []any{"slack.Acme.out"}, "as": "agent:slack-iod"},
	})
	resp.Body.Close()
	// The reflection the install started dials an iod that is not there and records that failure on
	// the record once; the heads below are measured after it, so what they measure is the report.
	waitEntity(t, srv, viffio.Ref("workspace"), func(p entity.Payload) bool { return entity.Get(p, "lastError") != nil })
	scratch, ok := entity.ScratchOf(srv.Store)
	if !ok {
		t.Fatal("the test store has no scratch")
	}
	report := func() map[string]any {
		t.Helper()
		n, err := scratch.ScratchRead(outReportRef(viffio.Ref("workspace")))
		if err != nil {
			t.Fatal(err)
		}
		return entity.ToObj(n)
	}
	// Every head below is taken once what could commit has: the reflection's failure, waited for
	// above, is the install's last word on the document, and the install itself noted its
	// principal before answering; after that, only a pass route writes, and it has written by
	// the time it answers.
	head := func() int64 { return srv.Store.(interface{ Head() int64 }).Head() }

	pass := postTony(t, ts.URL+"/iods/workspace/pass", viffio.OutPass{Delivered: 3})
	pass.Body.Close()
	if pass.StatusCode != http.StatusNoContent {
		t.Fatalf("reporting a pass = %s", pass.Status)
	}
	if rep := report(); rep["lastPass"] == nil || fmt.Sprint(rep["delivered"]) != "3" {
		t.Fatalf("the report in scratch reads %v, want when it last passed and what it did", rep)
	}
	// And the listing shows the report beside the record, one row for both halves.
	listing := getAs(t, "user:ana@example.com", ts.URL+"/iods")
	if body := readBody(t, listing); !strings.Contains(body, "lastPass:") || !strings.Contains(body, "delivered: 3") {
		t.Errorf("the listing does not show what the last pass did: %s", body)
	}
	before := head()
	more := postTony(t, ts.URL+"/iods/workspace/pass", viffio.OutPass{Delivered: 2})
	more.Body.Close()
	if fmt.Sprint(report()["delivered"]) != "2" {
		t.Errorf("the report in scratch did not follow the pass: %v", report())
	}
	if h := head(); h != before {
		t.Errorf("a pass that worked committed to the document (head %d → %d); the report is scratch's", before, h)
	}
	rec, _, _ := srv.Store.Get(viffio.Ref("workspace"))
	if out := entity.Get(rec.Payload, "out"); entity.Get(out, "lastPass") != nil || entity.Get(out, "delivered") != nil {
		t.Errorf("the record carries the report: %v", entity.ToObj(out))
	}

	// A failure carries WHEN IT STARTED, kept across repeats: an error that has been the same
	// since breakfast reads as "now" otherwise. The repeat writes nothing.
	first := postTony(t, ts.URL+"/iods/workspace/pass", viffio.OutPass{Failed: 1, Error: "slack refused"})
	first.Body.Close()
	e, _ := viffio.Find(srv.Store, "workspace")
	if e.Out == nil || e.Out.LastError == nil || e.Out.LastError.When == nil {
		t.Fatalf("out half reads %+v, want the failure and when it began", e.Out)
	}
	began := *e.Out.LastError.When
	if fmt.Sprint(report()["failed"]) != "1" {
		t.Errorf("the report in scratch does not say the pass failed: %v", report())
	}

	settled := head()
	again := postTony(t, ts.URL+"/iods/workspace/pass", viffio.OutPass{Failed: 1, Error: "slack refused"})
	again.Body.Close()
	e, _ = viffio.Find(srv.Store, "workspace")
	if when := *e.Out.LastError.When; when != began {
		t.Errorf("the same failure began again at %s, was %s", when, began)
	}
	if h := head(); h != settled {
		t.Errorf("the same failure again committed to the document (head %d → %d)", settled, h)
	}

	// And a pass that worked ends it, once.
	okPass := postTony(t, ts.URL+"/iods/workspace/pass", viffio.OutPass{Delivered: 1})
	okPass.Body.Close()
	e, _ = viffio.Find(srv.Store, "workspace")
	if e.Out.LastError != nil {
		t.Errorf("the failure outlived the pass that ended it: %+v", e.Out.LastError)
	}
	ended := head()
	okAgain := postTony(t, ts.URL+"/iods/workspace/pass", viffio.OutPass{Delivered: 1})
	okAgain.Body.Close()
	if h := head(); h != ended {
		t.Errorf("a pass that worked after one that worked committed to the document (head %d → %d)", ended, h)
	}

	// The report goes with the registration: an io removed and installed again under the name
	// has passed nothing, and is not listed with what its predecessor last did.
	gone := deleteAs(t, "user:ana@example.com", ts.URL+"/iods/workspace?forget=true")
	gone.Body.Close()
	if gone.StatusCode != http.StatusOK {
		t.Fatalf("removing the io = %s", gone.Status)
	}
	if rep := report(); len(rep) != 0 {
		t.Errorf("the report outlived its registration: %v", rep)
	}
	resp = installIO(t, ts, "user:ana@example.com", map[string]any{
		"name": "workspace", "root": "slack", "url": "http://slack-iod:9801",
		"owns": []any{map[string]any{"at": "slack.Acme"}},
		"out":  map[string]any{"places": []any{"slack.Acme.out"}, "as": "agent:slack-iod"},
	})
	resp.Body.Close()
	if rep := report(); len(rep) != 0 {
		t.Errorf("a registration installed anew carries a report: %v", rep)
	}
}

// A pass reported for an io nobody installed is a 404 rather than a registration conjured out of
// a timestamp.
func TestAPassForNoSuchIoIsRefused(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := postTony(t, ts.URL+"/iods/nobody/pass", viffio.OutPass{Delivered: 1})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("a pass for an io that is not installed = %s, want 404", resp.Status)
	}
	if _, ok := viffio.Find(srv.Store, "nobody"); ok {
		t.Error("a registration was created by a pass report")
	}
}

// THE OPERATOR'S PATH: an out half named on the command line, with no claim in the request.
//
// `viff iod add slack <url> --root slack --out slack.Signadot.out` says nothing about what the
// io owns — the claim comes from PROBING it. So the check that an out place is inside the claim
// cannot be asked of the request, and asking it there refused every out half an operator typed,
// including the one a deployment's repopulate script types. The other tests here pass `owns` explicitly and so
// never went through this door.
func TestAnOutHalfIsCheckedAgainstTheClaimTheProbeRead(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	face := sourceFace(t, "viff") // claims git.viff, and says so when asked
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := postAsTony(t, "user:ana@example.com", ts.URL+"/iods", map[string]any{
		"root": "git", "name": "laptop", "url": face.URL,
		"out": map[string]any{"places": []any{"git.viff.out"}, "as": "agent:git-iod"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install = %s: %s", resp.Status, read(t, resp))
	}
	// And the claim it was checked against is the probe's, which is also what makes the
	// refusal below meaningful rather than vacuous.
	e, ok := viffio.Find(srv.Store, "laptop")
	if !ok || len(e.Owns) != 1 || e.Owns[0].At != "git.viff" {
		t.Fatalf("registration owns %+v, want what the probe read", e.Owns)
	}

	// …and a place outside that claim is still refused, now for the right reason.
	bad := postAsTony(t, "user:ana@example.com", ts.URL+"/iods", map[string]any{
		"root": "git", "name": "laptop2", "url": face.URL,
		"out": map[string]any{"places": []any{"elsewhere.out"}, "as": "agent:git-iod"},
	})
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("a place outside the probed claim = %s, want 400", bad.Status)
	}
	if said := read(t, bad); !strings.Contains(said, "outside what this io claims (git.viff)") {
		t.Errorf("refusal %q, want the probed claim named", said)
	}
}

// postAsTony is postTony as somebody: an install names who makes it, and these two want an
// owner for the places they take.
func postAsTony(t *testing.T, as, url string, body map[string]any) *http.Response {
	t.Helper()
	b, err := codec.Marshal(body, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return postAs(t, as, url, "application/tony", string(b))
}

// A SCOPED IOD'S REPORT IS ITS SCOPE'S. Scratch has no scopes of its own, so a scoped iod of the
// same name as a baseline one would otherwise report over baseline's counts, and clear them at its
// install; and the scope's report would outlive the scope, since nothing in a scope's data names
// it. Each stands in its own scratch, and the scope's goes with the scope.
func TestAScopedIodsReportIsItsScopes(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	reg := map[string]any{
		"name": "workspace", "root": "slack", "url": "http://slack-iod:9801",
		"owns": []any{map[string]any{"at": "slack.Acme"}},
		"out":  map[string]any{"places": []any{"slack.Acme.out"}, "as": "agent:slack-iod"},
	}
	resp := installIO(t, ts, "user:ana@example.com", reg)
	resp.Body.Close()
	pass := postTony(t, ts.URL+"/iods/workspace/pass", viffio.OutPass{Delivered: 3})
	pass.Body.Close()
	if pass.StatusCode != http.StatusNoContent {
		t.Fatalf("reporting a pass = %s", pass.Status)
	}
	// The same iod, by name and by claim, in a scope: shadowing baseline is what a scope is for.
	b, err := codec.Marshal(reg, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	scopedInstall := postAs(t, "user:ana@example.com", ts.URL+"/iods?probe=false&scope=sandbox", "application/tony", string(b))
	if scopedInstall.StatusCode != http.StatusCreated {
		t.Fatalf("a scoped install over a baseline iod of the name = %s: %s", scopedInstall.Status, readBody(t, scopedInstall))
	}
	scopedInstall.Body.Close()
	delivered := func(store entity.Store) string {
		t.Helper()
		sc, ok := entity.ScratchOf(store)
		if !ok {
			t.Fatal("the store has no scratch")
		}
		n, err := sc.ScratchRead(outReportRef(viffio.Ref("workspace")))
		if err != nil {
			t.Fatal(err)
		}
		if n == nil {
			return ""
		}
		return fmt.Sprint(entity.ToObj(n)["delivered"])
	}
	if got := delivered(srv.Store); got != "3" {
		t.Fatalf("the scoped install cleared baseline's report: delivered %q, want 3", got)
	}
	scopedPass := postTony(t, ts.URL+"/iods/workspace/pass?scope=sandbox", viffio.OutPass{Delivered: 7})
	scopedPass.Body.Close()
	if scopedPass.StatusCode != http.StatusNoContent {
		t.Fatalf("reporting a scoped pass = %s", scopedPass.Status)
	}
	if got := delivered(srv.Store.Scope("sandbox")); got != "7" {
		t.Errorf("the scope's report reads delivered %q, want 7", got)
	}
	if got := delivered(srv.Store); got != "3" {
		t.Errorf("a scoped pass reported over baseline's: delivered %q, want 3", got)
	}
	gone := postTony(t, ts.URL+"/scope/delete?scope=sandbox", nil)
	gone.Body.Close()
	if gone.StatusCode != http.StatusNoContent {
		t.Fatalf("deleting the scope = %s", gone.Status)
	}
	if got := delivered(srv.Store.Scope("sandbox")); got != "" {
		t.Errorf("the scope's report outlived the scope: delivered %q", got)
	}
	if got := delivered(srv.Store); got != "3" {
		t.Errorf("deleting the scope took baseline's report: delivered %q, want 3", got)
	}
}
