package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
)

func putAs(t *testing.T, as, url, at string, payload entity.Payload) *http.Response {
	t.Helper()
	put, err := codec.Marshal(api.PutEntity{At: at, Payload: payload}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return postAs(t, as, url+"/entity", "application/tony", string(put))
}

func statusOf(t *testing.T, ts *httptest.Server) api.Status {
	t.Helper()
	resp, err := http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	var st api.Status
	decodeResp(t, resp, &st)
	return st
}

// A write the check refuses is a 403 carrying the check's own words, lands nowhere, and is
// counted on /status.
func TestARefusedWriteIsA403AndCounted(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := putAs(t, "alice@example.com", ts.URL, "demo.pr.1", entity.Obj{"stage": "open"}.Payload())
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("alice's put: %s", resp.Status)
	}
	if st := statusOf(t, ts); st.Perms == nil || len(st.Perms.Refused) != 0 {
		t.Fatalf("before any refusal, perms = %+v", st.Perms)
	}

	resp = putAs(t, "bob@example.com", ts.URL, "demo.pr.1", entity.Obj{"stage": "merged"}.Payload())
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bob's put: %s %s", resp.Status, body)
	}
	if !strings.Contains(string(body), "bob@example.com holds read at") || // read: the open verse's
		!strings.Contains(string(body), "alice@example.com at") {
		t.Fatalf("the refusal does not say who or what decided: %s", body)
	}
	e, ok, err := srv.Store.Get(entity.RefAt("demo", "pr", "1"))
	if err != nil || !ok || entity.Str(e.Payload, "stage") != "open" {
		t.Fatalf("the refused write landed: stage = %q (ok=%v err=%v)", entity.Str(e.Payload, "stage"), ok, err)
	}
	if st := statusOf(t, ts); st.Perms.Refused[string(entity.ReadWrite)] != 1 {
		t.Fatalf("perms = %+v", st.Perms)
	}
	// alice, the owner, still writes.
	resp = putAs(t, "alice@example.com", ts.URL, "demo.pr.1", entity.Obj{"stage": "merged"}.Payload())
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("alice's put: %s", resp.Status)
	}
}

// Bare `verse perms` is the records of every node the requester may read, and no other: who
// owns what they can see, and what it holds out. The mirror is the system's, so this is the
// one listing a person has of it — and a record whose node they may not read is not theirs to
// know exists.
func TestTheRecordsOneMayReadAreListed(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	walter := entity.As(srv.Store, "user:walter@example.com")
	space, notes, private := entity.RefAt("spaces", "walter"), entity.RefAt("spaces", "walter", "notes"), entity.RefAt("spaces", "walter", "private")
	if resp := putAs(t, "walter@example.com", ts.URL, "spaces.walter.notes", entity.Obj{"todo": "milk"}.Payload()); resp.StatusCode/100 != 2 {
		t.Fatalf("notes: %s", resp.Status)
	}
	if err := entity.Claim(walter, space, "user:walter@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetChildGrants(walter, space, entity.Levels{}); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(walter, space, nil); err != nil {
		t.Fatal(err)
	}
	if resp := putAs(t, "walter@example.com", ts.URL, "spaces.walter.private", entity.Obj{"todo": "mine"}.Payload()); resp.StatusCode/100 != 2 {
		t.Fatalf("private: %s", resp.Status)
	}

	listed := func(as string) map[string]api.PermsRecord {
		t.Helper()
		resp := getAs(t, as, ts.URL+"/perms/records")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s lists: %s", as, resp.Status)
		}
		var recs []api.PermsRecord
		decodeResp(t, resp, &recs)
		out := map[string]api.PermsRecord{}
		for i, r := range recs {
			if i > 0 && recs[i-1].At > r.At {
				t.Fatalf("the listing is not in address order: %s before %s", recs[i-1].At, r.At)
			}
			out[r.At] = r
		}
		return out
	}
	mine := listed("walter@example.com")
	// Two records, where ownership changed: `spaces`, which his first put brought into being —
	// a creation takes the place it made with it — and `spaces.walter`, which he took to close.
	top := entity.RefAt("spaces")
	for _, at := range []string{top.String(), space.String()} {
		if _, ok := mine[at]; !ok {
			t.Errorf("walter's listing lacks %s: %v", at, mine)
		}
	}
	// Neither node beneath has a record of its own: each is owned through the place above it,
	// and a record is written only where ownership changes. Not listed because not there.
	for _, at := range []string{notes.String(), private.String()} {
		if _, ok := mine[at]; ok {
			t.Errorf("a node owned through the place above it has a record: %v", mine[at])
		}
	}
	if mine[space.String()].Owner != "user:walter@example.com" {
		t.Errorf("the place's record reads %+v", mine[space.String()])
	}
	// Ana reads the top, which was created under the open default, and nothing of the space
	// walter closed — the notes inside it included, since they are his through the space and
	// closing it closed them.
	theirs := listed("ana@example.com")
	if _, ok := theirs[top.String()]; !ok {
		t.Errorf("ana's listing lacks the place created under the open default: %v", theirs)
	}
	for _, at := range []string{space.String(), notes.String(), private.String()} {
		if _, ok := theirs[at]; ok {
			t.Errorf("ana's listing carries %s, which she may not read", at)
		}
	}
}
