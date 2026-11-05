package slack

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	verseio "github.com/signadot/verse/pkg/io"
)

// The delivery half: Slack's Events API, received HERE.
//
// This is the git hook in another costume, and the symmetry is the design. verse does not
// parse deliveries — a source owns its own, because the source is what deployed the hook or
// holds the signing secret — so what crosses to verse is the same thing in both cases: which
// ref went stale, as a query parameter, and nothing else. `Git.InstallHook` writes a four-line
// `/bin/sh` script that curls it; this reads a signed JSON envelope and calls verseio.PostHint.
// Neither one tells verse anything it could act on wrongly.
//
// Which is why a forged delivery is not interesting: it costs a reconcile of a slice the
// sender does not get to choose the contents of. The signature check below is here because
// Slack's own contract asks for it and because an unverified public endpoint is somebody
// else's rate limit to spend, not because the hint law depends on it.

// eventsMaxBody caps a delivery. Slack's envelopes are small; the cap is against a body
// nobody sent on purpose.
const eventsMaxBody = 1 << 20

// eventsMaxSkew is how far a delivery's timestamp may be from now. Slack's own documented
// replay window.
const eventsMaxSkew = 5 * time.Minute

// Events returns the handler for Slack's Events API request url.
//
// It answers the challenge Slack sends when the url is configured, verifies every delivery
// against the signing secret, and turns what it can into a targeted hint — a delivery it
// cannot place in a channel becomes a full resync rather than nothing, because a hint that
// names too much costs a pass and a hint that names nothing costs a missed change.
//
// The hint is sent BEFORE the 200, synchronously. Slack wants an answer within three seconds
// and verse is a service away, so the round trip is not the thing to optimise — and if it ever
// were, Slack's retry would deliver the event again, which by the law above is harmless.
func (s *Slack) Events(signingSecret string, hint verseio.Hinter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if signingSecret == "" {
			// Refused rather than trusted. The hint law says a forged delivery is cheap, but a
			// source that silently accepted unsigned ones would be a claim about Slack's
			// contract that this program is not entitled to make.
			http.Error(w, "no signing secret configured", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, eventsMaxBody))
		if err != nil {
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}
		if err := verify(signingSecret, r.Header.Get("X-Slack-Request-Timestamp"),
			r.Header.Get("X-Slack-Signature"), body, time.Now()); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		var d delivery
		if err := json.Unmarshal(body, &d); err != nil {
			http.Error(w, "undecodable body", http.StatusBadRequest)
			return
		}
		// The handshake Slack performs when the url is saved. It is signed like everything
		// else, so it lands after the check rather than in front of it.
		if d.Type == "url_verification" {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, d.Challenge)
			return
		}
		s.hintFor(r.Context(), d, hint)
		w.WriteHeader(http.StatusOK)
	})
}

// hintFor sends the hint a delivery calls for: the channel it names, or a full resync when it
// names none. A delivery arrives by the request url, signed, or over Socket Mode, on a connection
// the app token opened (socket.go), and says the same thing either way.
func (s *Slack) hintFor(ctx context.Context, d delivery, hint verseio.Hinter) {
	if hint == nil {
		return
	}
	if id := d.channel(); id != "" {
		hint(ctx, s.ChannelRef(id))
	} else {
		hint(ctx) // no channel in it — a full resync
	}
}

// delivery is the part of an event envelope that says which slice went stale, and nothing
// else. Nothing in here becomes an entity: reading a payload into the store is the mistake
// the hint law exists to prevent, so this type is deliberately incapable of it.
type delivery struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Event     struct {
		Type string `json:"type"`
		// A string for a message, an OBJECT for channel_created — Slack spells the same field
		// both ways, so it is read raw and resolved below.
		Channel json.RawMessage `json:"channel"`
		Item    struct {
			Channel string `json:"channel"`
		} `json:"item"`
	} `json:"event"`
}

// channel resolves the channel id an event is about, or "" when it is not about one.
func (d delivery) channel() string {
	raw := d.Event.Channel
	if len(raw) > 0 {
		if raw[0] == '"' {
			var s string
			if json.Unmarshal(raw, &s) == nil && s != "" {
				return s
			}
		}
		var obj struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &obj) == nil && obj.ID != "" {
			return obj.ID
		}
	}
	// reaction_added / reaction_removed / star_added carry it under `item`.
	return d.Event.Item.Channel
}

// verify checks a delivery's signature and its age, which are one check: the timestamp is
// signed with the body, so a replay of a genuine delivery is caught by the age and a forged
// one by the digest.
func verify(secret, timestamp, signature string, body []byte, now time.Time) error {
	if timestamp == "" || signature == "" {
		return fmt.Errorf("unsigned delivery")
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("bad timestamp %q", timestamp)
	}
	if age := now.Sub(time.Unix(ts, 0)); age > eventsMaxSkew || age < -eventsMaxSkew {
		return fmt.Errorf("stale delivery (%s off)", age.Round(time.Second))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "v0:%s:", timestamp)
	mac.Write(body)
	want := "v0=" + hex.EncodeToString(mac.Sum(nil))
	// hmac.Equal, not ==: a signature check that leaks its own progress through timing is the
	// one bug this kind of code is famous for.
	if !hmac.Equal([]byte(want), []byte(signature)) {
		return fmt.Errorf("bad signature")
	}
	return nil
}

// Sign spells a delivery's signature headers, which is what a test needs to send one and what
// nothing in production calls. It is here rather than in the test file because the events
// handler and its sender must agree on the base string, and two copies of that agreement is
// how a test comes to pass against a rule the handler does not actually apply.
func Sign(secret string, at time.Time, body []byte) (timestamp, signature string) {
	timestamp = strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "v0:%s:", timestamp)
	mac.Write(body)
	return timestamp, "v0=" + hex.EncodeToString(mac.Sum(nil))
}
