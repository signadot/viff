package slack_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/io/slack"
)

const secret = "shhh"

// deliver posts a signed event.
func deliver(t *testing.T, h http.Handler, body any, at time.Time) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	ts, sig := slack.Sign(secret, at, raw)
	r := httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(string(raw)))
	r.Header.Set("X-Slack-Request-Timestamp", ts)
	r.Header.Set("X-Slack-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// eventsHandler builds a source and its events handler, recording every hint.
func eventsHandler(t *testing.T, signing string) (http.Handler, *[][]entity.Ref) {
	t.Helper()
	f := newFake()
	src := newSource(t, f, slack.Config{})
	var hints [][]entity.Ref
	h := src.Events(signing, func(_ context.Context, refs ...entity.Ref) {
		hints = append(hints, refs)
	})
	return h, &hints
}

// A delivery becomes ONE THING: which channel went stale. Slack spells the channel three
// different ways depending on the event, and all three have to land on the same ref.
func TestSlackEventsHintTheChannelHoweverItIsSpelled(t *testing.T) {
	want := slack.ChannelRef("Acme", "C1")
	cases := []struct {
		name string
		body map[string]any
	}{
		{"a message, where channel is a string", map[string]any{
			"type":  "event_callback",
			"event": map[string]any{"type": "message", "channel": "C1", "text": "hi"},
		}},
		{"channel_created, where channel is an object", map[string]any{
			"type": "event_callback",
			"event": map[string]any{"type": "channel_created",
				"channel": map[string]any{"id": "C1", "name": "incidents"}},
		}},
		{"reaction_added, where it is under item", map[string]any{
			"type": "event_callback",
			"event": map[string]any{"type": "reaction_added",
				"item": map[string]any{"type": "message", "channel": "C1", "ts": "1.2"}},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, hints := eventsHandler(t, secret)
			w := deliver(t, h, c.body, time.Now())
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if len(*hints) != 1 || len((*hints)[0]) != 1 || (*hints)[0][0] != want {
				t.Fatalf("hints %v, want one %s", *hints, want)
			}
		})
	}
}

// A delivery that names no channel is a full resync, not a dropped one: a hint that names too
// much costs a pass, and one that names nothing costs a missed change.
func TestSlackAnEventWithNoChannelAsksForAFullPass(t *testing.T) {
	h, hints := eventsHandler(t, secret)
	w := deliver(t, h, map[string]any{
		"type":  "event_callback",
		"event": map[string]any{"type": "team_join", "user": map[string]any{"id": "U9"}},
	}, time.Now())
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if len(*hints) != 1 || len((*hints)[0]) != 0 {
		t.Fatalf("hints %v, want one with no refs", *hints)
	}
}

// The handshake Slack performs when the Request URL is saved — and it is signed like
// everything else, so it lands after the check rather than in front of it.
func TestSlackEventsAnswerTheUrlVerificationChallenge(t *testing.T) {
	h, hints := eventsHandler(t, secret)
	w := deliver(t, h, map[string]any{
		"type": "url_verification", "challenge": "abc123",
	}, time.Now())
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "abc123" {
		t.Fatalf("status %d body %q", w.Code, w.Body)
	}
	if len(*hints) != 0 {
		t.Fatal("the handshake hinted")
	}
}

// A forged or replayed delivery is refused, and — this is the part that matters — it hints
// nothing. The hint law says a forgery would only cost a reconcile; this is the cheaper
// answer, and it is what Slack's contract asks for.
func TestSlackEventsRefuseWhatIsNotSigned(t *testing.T) {
	h, hints := eventsHandler(t, secret)

	// Wrong secret.
	raw := []byte(`{"type":"event_callback","event":{"type":"message","channel":"C1"}}`)
	ts, sig := slack.Sign("not-the-secret", time.Now(), raw)
	r := httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(string(raw)))
	r.Header.Set("X-Slack-Request-Timestamp", ts)
	r.Header.Set("X-Slack-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a bad signature got %d", w.Code)
	}

	// A genuine delivery from an hour ago: signed correctly, refused on age.
	w = deliver(t, h, map[string]any{
		"type":  "event_callback",
		"event": map[string]any{"type": "message", "channel": "C1"},
	}, time.Now().Add(-time.Hour))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a replayed delivery got %d", w.Code)
	}

	// Unsigned entirely.
	r = httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(string(raw)))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an unsigned delivery got %d", w.Code)
	}

	if len(*hints) != 0 {
		t.Fatalf("a refused delivery hinted %v", *hints)
	}
}

// With no signing secret there is nothing to verify against, and the endpoint refuses rather
// than trusting: accepting unsigned deliveries would be a claim about Slack's contract this
// program is not entitled to make.
func TestSlackEventsRefuseEverythingWithoutASigningSecret(t *testing.T) {
	h, hints := eventsHandler(t, "")
	w := deliver(t, h, map[string]any{"type": "url_verification", "challenge": "x"}, time.Now())
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", w.Code)
	}
	if len(*hints) != 0 {
		t.Fatal("it hinted anyway")
	}
}
