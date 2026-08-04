package linear_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/source/linear"
)

const secret = "shhh"

func webhook(t *testing.T, signing string) (http.Handler, *[][]entity.Ref) {
	t.Helper()
	f := newFake()
	f.team("ENG", "Engineering")
	src := newSource(t, f, linear.Config{Teams: []string{"ENG"}})
	var hints [][]entity.Ref
	h := src.Webhook(signing, func(_ context.Context, refs ...entity.Ref) {
		hints = append(hints, refs)
	})
	return h, &hints
}

func deliver(t *testing.T, h http.Handler, body map[string]any, sign string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/linear/webhook", strings.NewReader(string(raw)))
	if sign != "" {
		r.Header.Set("Linear-Signature", linear.Sign(sign, raw))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func now() int64 { return time.Now().UnixMilli() }

// A delivery becomes ONE THING: which team went stale. Linear carries the team in more than
// one place depending on what changed, and all of them have to land on the same ref.
func TestLinearWebhookHintsTheTeamHoweverItIsCarried(t *testing.T) {
	want := linear.TeamRef("ENG")
	cases := []struct {
		name string
		body map[string]any
	}{
		{"an issue, by its nested team", map[string]any{
			"action": "update", "type": "Issue", "webhookTimestamp": now(),
			"data": map[string]any{"id": "uuid", "team": map[string]any{"key": "ENG"}},
		}},
		{"an issue, by its identifier's prefix", map[string]any{
			"action": "create", "type": "Issue", "webhookTimestamp": now(),
			"data": map[string]any{"identifier": "ENG-42"},
		}},
		{"a comment, by the issue it hangs off", map[string]any{
			"action": "create", "type": "Comment", "webhookTimestamp": now(),
			"data": map[string]any{"issue": map[string]any{"identifier": "ENG-42"}},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, hints := webhook(t, secret)
			w := deliver(t, h, c.body, secret)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if len(*hints) != 1 || len((*hints)[0]) != 1 || (*hints)[0][0] != want {
				t.Fatalf("hints %v, want one %s", *hints, want)
			}
		})
	}
}

// A delivery this source cannot place in a team it reflects asks for a full pass rather than
// nothing — including one about somebody else's team, where the full pass is over the teams
// THIS source has and so costs nothing wrong.
func TestLinearAnUnplaceableDeliveryAsksForAFullPass(t *testing.T) {
	for _, body := range []map[string]any{
		{"action": "update", "type": "Project", "webhookTimestamp": now(),
			"data": map[string]any{"id": "uuid"}},
		{"action": "update", "type": "Issue", "webhookTimestamp": now(),
			"data": map[string]any{"identifier": "OPS-1"}},
	} {
		h, hints := webhook(t, secret)
		w := deliver(t, h, body, secret)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d", w.Code)
		}
		if len(*hints) != 1 || len((*hints)[0]) != 0 {
			t.Fatalf("hints %v, want one with no refs", *hints)
		}
	}
}

// A forged or replayed delivery is refused and hints nothing.
func TestLinearWebhookRefusesWhatIsNotSigned(t *testing.T) {
	h, hints := webhook(t, secret)

	body := map[string]any{"action": "update", "type": "Issue", "webhookTimestamp": now(),
		"data": map[string]any{"identifier": "ENG-1"}}
	if w := deliver(t, h, body, "not-the-secret"); w.Code != http.StatusUnauthorized {
		t.Fatalf("a bad signature got %d", w.Code)
	}
	if w := deliver(t, h, body, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("an unsigned delivery got %d", w.Code)
	}
	// Correctly signed, but from an hour ago: the timestamp is inside the signed body, so a
	// replay of a genuine delivery is caught on age.
	old := map[string]any{"action": "update", "type": "Issue",
		"webhookTimestamp": time.Now().Add(-time.Hour).UnixMilli(),
		"data":             map[string]any{"identifier": "ENG-1"}}
	if w := deliver(t, h, old, secret); w.Code != http.StatusUnauthorized {
		t.Fatalf("a replayed delivery got %d", w.Code)
	}
	if len(*hints) != 0 {
		t.Fatalf("a refused delivery hinted %v", *hints)
	}
}

// With no secret there is nothing to verify against, and the endpoint refuses rather than
// trusting.
func TestLinearWebhookRefusesEverythingWithoutASecret(t *testing.T) {
	h, hints := webhook(t, "")
	w := deliver(t, h, map[string]any{"type": "Issue", "webhookTimestamp": now()}, secret)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", w.Code)
	}
	if len(*hints) != 0 {
		t.Fatal("it hinted anyway")
	}
}
