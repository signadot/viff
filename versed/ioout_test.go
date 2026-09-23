package versed

import (
	stdio "io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
)

// An io that acts says so on its registration, and the install stands its place up: the askers may
// ask, the io owns what it answers, and everyone may read both. What an operator used to type as
// four `verse perms` lines is written from the record, where it cannot be typed wrong.

// installIO registers one io with an out half, as installer, and answers the response.
func installIO(t *testing.T, ts *httptest.Server, as string, body map[string]any) *http.Response {
	t.Helper()
	b, err := codec.Marshal(body, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return postAs(t, as, ts.URL+"/io?probe=false", "application/tony", string(b))
}

func TestAnIoWithAnOutHalfHasItsGrantsWritten(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const installer = "user:ana@example.com"
	resp := installIO(t, ts, installer, map[string]any{
		"name": "workspace", "root": "slack", "url": "http://slack-io:9801",
		"owns": []any{map[string]any{"at": "slack.Acme"}},
		"out": map[string]any{
			"places":     []any{"slack.Acme.out"},
			"as":         "agent:slack-io",
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
	status := entity.RefAt("slack", "Acme", "out", "U1", "hello"+verseio.StatusSuffix)
	asker, io, stranger := "user:bo@example.com", "agent:slack-io", "user:cara@example.com"

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
		{"no places", map[string]any{"as": "agent:slack-io"}, "out.places is required"},
		{"nobody to be", map[string]any{"places": []any{"slack.Acme.out"}}, "out.as is required"},
		{"a principal the grammar refuses", map[string]any{
			"places": []any{"slack.Acme.out"}, "as": "slack-io"}, "out.as"},
		{"a place outside the claim", map[string]any{
			"places": []any{"elsewhere.out"}, "as": "agent:slack-io"}, "outside what this io claims"},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := installIO(t, ts, "user:ana@example.com", map[string]any{
				"name": "workspace-" + strings.ReplaceAll(c.name, " ", "-"),
				"root": "slack", "url": "http://slack-io:9801",
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
		"name": "cluster", "root": "git", "url": "http://git-io:9801",
		"owns": []any{map[string]any{"at": "git"}},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install = %s", resp.Status)
	}
	e, ok, err := srv.Store.Get(verseio.Ref("cluster"))
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
		"name": "workspace", "root": "slack", "url": "http://slack-io:9801",
		"owns": []any{map[string]any{"at": "slack.Acme"}},
		"out": map[string]any{
			"places": []any{"slack.Acme.out"}, "as": "agent:slack-io",
			"requesters": []any{"user:bo@example.com"},
		},
	})
	resp.Body.Close()

	input := entity.RefAt("slack", "Acme", "out", "U1", "hello")
	if err := write(srv.Store, "user:bo@example.com", input, entity.Obj{"text": "say this"}); err != nil {
		t.Fatalf("an asker could not ask: %v", err)
	}

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/io/workspace?forget=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Verse-Principal", "user:ana@example.com")
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
