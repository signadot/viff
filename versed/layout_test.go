package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A charter comes back in ONE shape, whatever punctuation it was written with.
//
// It used to come back as a hybrid, and the hybrid was legible only if you knew the Go
// types: `patch` and a match's `value` are held as opaque tony documents, so they returned
// bracketed exactly as authored, while `condition`, `spawn` and `gates` — decoded into
// structs, which have no layout to carry — returned in block form no matter what. So an
// author who bracketed everything got braces on two fields and blocks on the rest, and the
// selection looked arbitrary because the thing it tracked (which parts verse interprets) is
// invisible from outside.
//
// The fix is at ingest (codec.Read → StripBrackets), so this asserts the property a reader
// sees rather than the mechanism: post it braced, read it back, find no braces.
func TestAnInstalledCharterRendersInOneShape(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Everything bracketable, bracketed — including the two fields that used to keep it.
	charter := `{ name: braced,
	  condition: { subject: { system: demo, kind: pr, value: { stage: open } } },
	  action: { patch: { stage: reviewing }, spawn: { driver: scripted, prompt: "echo hi" } } }`
	resp, err := http.Post(ts.URL+"/charter", "application/tony", strings.NewReader(charter))
	if err != nil {
		t.Fatalf("post charter: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}

	got, err := http.Get(ts.URL + "/charter/braced")
	if err != nil {
		t.Fatalf("get charter: %v", err)
	}
	defer got.Body.Close()
	body, _ := io.ReadAll(got.Body)
	if strings.ContainsAny(string(body), "{}") {
		t.Errorf("the authored brackets survived install — a charter is a value, not a "+
			"memory of the file it arrived in:\n%s", body)
	}
	// It has to still BE the charter, not merely be brace-free.
	for _, want := range []string{"braced", "stage", "reviewing", "scripted"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the charter lost %q on the way through:\n%s", want, body)
		}
	}
}

// The same for a payload verse stores but never interprets: `entity put` with a JSON body
// is tony written in brackets, and the brackets are not part of the value.
func TestAStoredPayloadRendersInOneShape(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"ref":"demo:pr:1","payload":{"stage":"open","tags":["a","b"]}}`
	resp, err := http.Post(ts.URL+"/entity", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("put entity: %v", err)
	}
	resp.Body.Close()

	got, err := http.Get(ts.URL + "/entity?ref=demo:pr:1")
	if err != nil {
		t.Fatalf("get entity: %v", err)
	}
	defer got.Body.Close()
	out, _ := io.ReadAll(got.Body)
	if strings.ContainsAny(string(out), "{}[]") {
		t.Errorf("a JSON body's brackets reached the stored value:\n%s", out)
	}
	if !strings.Contains(string(out), "stage") || !strings.Contains(string(out), "open") {
		t.Errorf("the payload did not survive:\n%s", out)
	}
}
