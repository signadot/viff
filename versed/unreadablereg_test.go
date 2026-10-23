package versed

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/resolver"
)

// A REGISTRATION THIS VERSE CAN'T READ is still there to see and to remove. One an older verse
// wrote in another shape used to come back as its raw payload; a listing that skipped it would
// leave an operator nothing but writing the store by hand. Each listing shows it as a row with
// its name and why, and the removal by name takes it out.
func TestARegistrationThisVerseCantReadIsListedAndRemoved(t *testing.T) {
	for _, c := range []struct {
		route string
		ref   entity.Ref
	}{
		{"/runners", runnerEntityRef("old")},
		{"/iods", verseio.Ref("old")},
		{"/resolvers", resolver.Ref("old")},
	} {
		t.Run(c.route, func(t *testing.T) {
			srv := testServer(t)
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()
			// A name that is a list: no registration's `name` decodes from one.
			if _, _, err := entity.As(srv.Store, "user:"+tester).Write(entity.Write{
				At: c.ref.At(), Value: entity.Obj{"name": []any{"old"}}.Payload(),
			}); err != nil {
				t.Fatal(err)
			}

			var rows []map[string]any
			resp, err := ts.Client().Do(mustReq(t, http.MethodGet, ts.URL+c.route))
			if err != nil {
				t.Fatal(err)
			}
			if err := codec.Decode(resp.Body, &rows, codec.Tony); err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if len(rows) != 1 || rows[0]["name"] != "old" || rows[0]["at"] != c.ref.String() || rows[0]["unreadable"] == nil {
				t.Fatalf("%s = %v, want the one registration, named, with why it can't be read", c.route, rows)
			}

			resp, err = ts.Client().Do(mustReq(t, http.MethodDelete, ts.URL+c.route+"/old"))
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode >= 300 {
				t.Fatalf("removing it = %s: %s, want it removed", resp.Status, readBody(t, resp))
			}
			resp.Body.Close()
			if _, ok, _ := srv.Store.Get(c.ref); ok {
				t.Fatalf("%s still stands after its removal", c.ref)
			}
		})
	}
}
