package io_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.viff.dev/viff/pkg/client"
	"go.viff.dev/viff/pkg/entity"
	viffio "go.viff.dev/viff/pkg/io"
)

// AN OBSOLETE SWEEP settles, as the io, the pending questions under its prefix that are about
// nothing kept, posed by its instant, and offering `obsolete`; it leaves the kept, the late, the
// foreign and the ones that offer no such option alone, and a sweep for an instant already swept
// asks nothing.
func TestTheGitOutHalfSettlesQuestionsNothingProposes(t *testing.T) {
	const prefix = "repo-charters:propose@repo-charters.r.candidate."
	var mu sync.Mutex
	var answered []string
	lists := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/asks" && r.URL.Query().Get("waiting") == "me":
			lists++
			w.Header().Set("Content-Type", "application/tony")
			io.WriteString(w, `- {id: "`+prefix+`kept~1", posedAt: "2026-10-04T20:00:00Z", options: [{id: proceed}, {id: deny}, {id: obsolete}]}
- {id: "`+prefix+`gone~1", posedAt: "2026-10-04T20:00:00Z", options: [{id: proceed}, {id: deny}, {id: obsolete}]}
- {id: "`+prefix+`late~1", posedAt: "2026-10-04T21:00:05Z", options: [{id: proceed}, {id: deny}, {id: obsolete}]}
- {id: "`+prefix+`plain~1", posedAt: "2026-10-04T20:00:00Z", options: [{id: proceed}, {id: deny}]}
- {id: "other:ask@x~1", posedAt: "2026-10-04T20:00:00Z", options: [{id: obsolete}]}
`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/answer"):
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), "obsolete") || !strings.Contains(string(b), "agent:git-iod") {
				t.Errorf("an answer that is not `obsolete` by the io: %s", b)
			}
			answered = append(answered, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/asks/"), "/answer"))
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer fake.Close()

	g := viffio.NewGit()
	g.AddRepo("r", t.TempDir())
	sink := g.Sink()
	sink.Viff = client.New(strings.TrimPrefix(fake.URL, "http://"), "agent:git-iod")
	sink.As = "agent:git-iod"
	in := viffio.Input{Place: g.OutRef("r"), To: "obsolete", Value: entity.Obj{
		"to": "obsolete", "prefix": prefix, "before": "2026-10-04T21:00:00Z",
		"keep": entity.Obj{"state": []any{"kept"}, "pulls": []any{""}},
	}.Payload()}
	st, err := sink.Deliver(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(answered) != 1 || !strings.Contains(answered[0], "gone") {
		t.Fatalf("answered %v, want only the question about gone", answered)
	}
	if got := st["obsoleted"]; got == nil || !strings.Contains(strings.Join(got.([]string), ","), "gone~1") {
		t.Errorf("status %v, want gone~1 obsoleted", st)
	}
	in.Status = st.Payload()
	if _, err := sink.Deliver(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if lists != 1 {
		t.Errorf("a sweep for an instant already swept listed the questions again: %d listings", lists)
	}
}
