package source

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/signadot/verse/entity"
)

// The hint, from the source's side.
//
// verse does not parse deliveries: a source owns its own — it deployed the git hook, it holds
// the Slack signing secret — and tells verse which refs went stale. That is the whole of the
// outbound direction, and it is deliberately the smallest thing that could work: a POST with
// the refs as query parameters and no body at all.
//
// This is here rather than in each source because there are now three senders (a `/bin/sh`
// hook running curl, a Slack events endpoint, a Linear webhook endpoint) and one route. A
// second spelling of it is how the git hook and a Go source come to disagree about what a hint
// is — and the shell one-liner is the version that cannot be refactored, so it is the one this
// has to match.

// Hinter is what a source's delivery endpoint calls once it has decided which refs went stale
// — PostHint, in every deployment, and something a test can watch in a test.
//
// It returns nothing, and that is the shape of the thing rather than laziness: the endpoint
// calling it is holding a webhook request from Slack or Linear that must be answered in
// seconds, and there is no failure it could usefully act on. A hint that does not arrive costs
// latency, and the resync net is what pays it.
type Hinter func(ctx context.Context, refs ...entity.Ref)

// HintPath is the route one registration's hints go to.
//
// The registration's NAME is in it, which is what makes a hint addressable at all: a source
// program does not know what an operator called it, so the url is configuration handed to the
// source (a flag, an env var, a line in a git hook) rather than something it can derive.
func HintPath(name string) string {
	return "/sources/" + name + "/hint"
}

// HintURL joins a verse base url to that route: HintURL("http://verse:9797", "acme").
func HintURL(base, name string) string {
	return strings.TrimRight(base, "/") + HintPath(name)
}

// Hints posts to hintURL and logs what it could not deliver — the Hinter every deployed source
// wires into its delivery endpoint, and the only place a source knows verse's address at all.
func Hints(hintURL string, logf func(string, ...any)) Hinter {
	return func(ctx context.Context, refs ...entity.Ref) {
		if err := PostHint(ctx, hintURL, refs...); err != nil && logf != nil {
			logf("%v", err)
		}
	}
}

// hintTimeout bounds a hint delivery. Short, and much shorter than a reflect: a hint is a
// courtesy — the periodic resync is what makes a missed one land eventually — so a source
// blocking its own event loop on a verse that is rolling out would be paying a real cost for
// an optimisation.
const hintTimeout = 10 * time.Second

// PostHint tells verse that the slice each ref names went stale. No refs is a full resync.
//
// It carries NOTHING but the refs, and that is the law rather than a minimal first version: a
// hint never writes entities, it schedules a reconcile that reads external truth — so a
// delivery that is duplicated, reordered or forged costs at most a pass over a slice its
// sender does not get to choose the contents of. That is what lets the route stay
// unauthenticated in a wide-open verse, and it is why there is no body to sign here even
// though the delivery this hint came FROM was signed.
//
// A failure is returned rather than retried. The caller is holding a webhook request it must
// answer quickly, and the resync net is the retry.
func PostHint(ctx context.Context, hintURL string, refs ...entity.Ref) error {
	q := url.Values{}
	for _, ref := range refs {
		q.Add("at", ref.String())
	}
	target := hintURL
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return fmt.Errorf("hint %s: %w", hintURL, err)
	}
	cl := &http.Client{Timeout: hintTimeout}
	resp, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("hint %s: %w", hintURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= http.StatusBadRequest {
		// Said out loud, because the two ways this fails are both operator mistakes that look
		// like nothing from the outside: a 404 is a registration name that does not match the
		// url this source was configured with, and a 503 is a registration whose reflector is
		// not running.
		return fmt.Errorf("hint %s: %s: %s", hintURL, resp.Status,
			strings.TrimSpace(string(bytes.TrimSpace(body))))
	}
	return nil
}
