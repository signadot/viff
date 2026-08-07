package linear

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/signadot/verse/source"
)

// The delivery half: a Linear webhook, received HERE.
//
// The same object as the Slack events endpoint and as the four-line `/bin/sh` git hook
// `Git.InstallHook` writes — a delivery the source owns, turned into which ref went stale,
// posted to verse. verse parses none of the three, which is what keeps "map a delivery to
// refs" from having a second implementation per source.
//
// A forged delivery costs a reconcile of a slice its sender does not get to choose the
// contents of. The signature check is here because Linear's contract asks for it and because
// an unverified public endpoint is somebody else's API budget to spend — not because the hint
// law depends on it.

const webhookMaxBody = 4 << 20

// webhookMaxSkew is how far a delivery's timestamp may be from now. Linear's own guidance says
// a minute; this is longer on purpose, because the failure it would otherwise produce is a
// pod with a drifting clock rejecting every genuine delivery — and by the law above, accepting
// a replayed one costs a reconcile.
const webhookMaxSkew = 5 * time.Minute

// Webhook returns the handler for a Linear webhook url.
//
// A delivery it cannot place in a team becomes a full resync rather than nothing: a hint that
// names too much costs a pass, and one that names nothing costs a missed change until the
// resync net catches it.
func (l *Linear) Webhook(secret string, hint source.Hinter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if secret == "" {
			// Refused rather than trusted, as in the Slack source: the hint law says a forged
			// delivery is cheap, but silently accepting unsigned ones would be a claim about
			// Linear's contract that this program is not entitled to make.
			http.Error(w, "no webhook secret configured", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, webhookMaxBody))
		if err != nil {
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}
		if err := verify(secret, r.Header.Get("Linear-Signature"), body); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		var d delivery
		if err := json.Unmarshal(body, &d); err != nil {
			http.Error(w, "undecodable body", http.StatusBadRequest)
			return
		}
		if err := d.fresh(time.Now()); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		if hint != nil {
			if key := l.team(d.teamKey()); key != "" {
				hint(r.Context(), l.TeamRef(key))
			} else {
				hint(r.Context()) // not placeable in a team this source reflects — a full pass
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

// team returns key if this source reflects that team, else "".
func (l *Linear) team(key string) string {
	for _, k := range l.teams {
		if k == key {
			return k
		}
	}
	return ""
}

// delivery is the part of a webhook body that says which slice went stale, and nothing else.
// Nothing here becomes an entity: reading a delivery's payload into the store is the mistake
// the hint law exists to prevent, so this type is deliberately incapable of it.
type delivery struct {
	Action    string `json:"action"`
	Type      string `json:"type"`
	Timestamp int64  `json:"webhookTimestamp"` // milliseconds
	Data      struct {
		Identifier string `json:"identifier"`
		Team       struct {
			Key string `json:"key"`
		} `json:"team"`
		Issue struct {
			Identifier string `json:"identifier"`
		} `json:"issue"`
	} `json:"data"`
}

// teamKey reads the team a delivery is about, from whichever of the three places this kind of
// payload carries it — the nested team (an Issue or a Cycle), the identifier's own prefix
// (`ENG-123`, which is a Linear identifier's shape and not a convention this source invented),
// or the issue a Comment hangs off. Empty when it is none of them.
func (d delivery) teamKey() string {
	if k := d.Data.Team.Key; k != "" {
		return k
	}
	if id := d.Data.Identifier; id != "" {
		if key, _, ok := strings.Cut(id, "-"); ok {
			return key
		}
	}
	if id := d.Data.Issue.Identifier; id != "" {
		if key, _, ok := strings.Cut(id, "-"); ok {
			return key
		}
	}
	return ""
}

// fresh bounds a delivery's age. Unlike Slack's, the timestamp is inside the signed body
// rather than in a header, so this is a check on decoded content and not part of the digest.
func (d delivery) fresh(now time.Time) error {
	if d.Timestamp == 0 {
		return nil // not every delivery carries one; the signature is the real check
	}
	age := now.Sub(time.UnixMilli(d.Timestamp))
	if age > webhookMaxSkew || age < -webhookMaxSkew {
		return fmt.Errorf("stale delivery (%s off)", age.Round(time.Second))
	}
	return nil
}

// verify checks a delivery's signature: hex HMAC-SHA256 of the raw body.
func verify(secret, signature string, body []byte) error {
	if signature == "" {
		return fmt.Errorf("unsigned delivery")
	}
	if !hmac.Equal([]byte(Sign(secret, body)), []byte(signature)) {
		// hmac.Equal, not ==: a signature check that leaks its own progress through timing is
		// the one bug this kind of code is famous for.
		return fmt.Errorf("bad signature")
	}
	return nil
}

// Sign spells a delivery's signature, which is what a test needs to send one and what nothing
// in production calls. It is here rather than in the test file because the handler and its
// sender must agree on what is signed, and two copies of that agreement is how a test comes to
// pass against a rule the handler does not apply.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
