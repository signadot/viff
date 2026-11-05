package github_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/io/github"
)

const hookSecret = "s3cret"

// post sends one delivery, signed unless a signature is given explicitly.
func post(t *testing.T, h http.Handler, event string, body map[string]any, sig ...string) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(raw))
	req.Header.Set("X-GitHub-Event", event)
	if len(sig) > 0 {
		req.Header.Set("X-Hub-Signature-256", sig[0])
	} else {
		req.Header.Set("X-Hub-Signature-256", github.Sign(hookSecret, raw))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Result()
}

// newPost and newRecorder are the two halves of a request a test sends by hand, shared with the
// captured-delivery replay so both send the same shape.
func newPost(path string, body []byte) *http.Request {
	return httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
}

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

// hinted collects what a delivery asked to be reconciled. A call with no refs is a full resync,
// which is a different answer from no call at all.
type hinted struct {
	calls [][]entity.Ref
}

func (h *hinted) hint(_ context.Context, refs ...entity.Ref) {
	h.calls = append(h.calls, refs)
}

func (h *hinted) only(t *testing.T) []entity.Ref {
	t.Helper()
	if len(h.calls) != 1 {
		t.Fatalf("want exactly one hint, got %d: %v", len(h.calls), h.calls)
	}
	return h.calls[0]
}

func webhook(t *testing.T, f *fakeGitHub, h *hinted, repos ...string) http.Handler {
	t.Helper()
	if len(repos) == 0 {
		repos = []string{"acme/app"}
	}
	src := newSource(t, f, github.Config{Repos: repos})
	return src.Webhook(hookSecret, h.hint)
}

// A delivery nobody signed, or signed wrongly, is refused — and a source with no secret refuses
// every delivery rather than trusting them. The hint law says a forged delivery is cheap; that
// is not a licence to accept unsigned ones on GitHub's behalf.
func TestAnUnsignedOrWronglySignedDeliveryIsRefused(t *testing.T) {
	f := newFake()
	f.repo("acme/app")
	h := &hinted{}
	wh := webhook(t, f, h)

	body := map[string]any{"repository": map[string]any{"full_name": "acme/app"}}
	if got := post(t, wh, "push", body, "").StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("an unsigned delivery = %d, want 401", got)
	}
	if got := post(t, wh, "push", body, "sha256=deadbeef").StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("a wrongly signed delivery = %d, want 401", got)
	}
	if len(h.calls) != 0 {
		t.Fatalf("a refused delivery hinted anyway: %v", h.calls)
	}

	// And with no secret configured, nothing is accepted at all.
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})
	none := src.Webhook("", h.hint)
	if got := post(t, none, "push", body).StatusCode; got != http.StatusServiceUnavailable {
		t.Fatalf("a source with no secret = %d, want 503", got)
	}
}

// The hint names the thing rather than its container, which is what makes a delivery cost one
// request instead of eight.
func TestADeliveryHintsTheThingItIsAbout(t *testing.T) {
	f := newFake()
	f.repo("acme/app")
	repo := map[string]any{"full_name": "acme/app"}

	for _, tc := range []struct {
		name  string
		event string
		body  map[string]any
		want  string
	}{
		{"an issue", "issues", map[string]any{
			"repository": repo, "issue": map[string]any{"number": 42},
		}, `github.acme.app.issue."42"`},
		{"a pull request", "pull_request", map[string]any{
			"repository": repo, "pull_request": map[string]any{"number": 7},
		}, `github.acme.app.pr."7"`},
		{"a review", "pull_request_review", map[string]any{
			"repository": repo, "pull_request": map[string]any{"number": 7},
		}, `github.acme.app.pr."7"`},
		{"a discussion", "discussion", map[string]any{
			"repository": repo, "discussion": map[string]any{"number": 3},
		}, `github.acme.app.discussion."3"`},
		{"a workflow run", "workflow_run", map[string]any{
			"repository": repo, "workflow_run": map[string]any{"id": 991},
		}, `github.acme.app.run."991"`},
		{"a push", "push", map[string]any{"repository": repo}, "github.acme.app"},
		// A comment delivery names the COMMENT, which is the new thing — one request against
		// the nine a repository pass costs, and against an item pass that would refresh a title
		// nobody changed. It arrives as an issue_comment for a pull request's conversation too,
		// since on GitHub a PR IS an issue for commenting, and the number is read from
		// whichever of the two the payload carried.
		{"a comment on a pull request", "issue_comment", map[string]any{
			"repository": repo, "comment": map[string]any{"id": 900},
			"issue": map[string]any{"number": 7, "pull_request": map[string]any{"url": "x"}},
		}, `github.acme.app.comment."7"."900"`},
		{"a comment on an issue", "issue_comment", map[string]any{
			"repository": repo, "comment": map[string]any{"id": 901},
			"issue": map[string]any{"number": 42},
		}, `github.acme.app.comment."42"."901"`},
		{"a review comment on a diff", "pull_request_review_comment", map[string]any{
			"repository": repo, "comment": map[string]any{"id": 902},
			"pull_request": map[string]any{"number": 7},
		}, `github.acme.app.comment."7"."902"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &hinted{}
			wh := webhook(t, f, h)
			if got := post(t, wh, tc.event, tc.body).StatusCode; got != http.StatusOK {
				t.Fatalf("status %d", got)
			}
			refs := h.only(t)
			if len(refs) != 1 || refs[0].String() != tc.want {
				t.Fatalf("hinted %v, want %s", refs, tc.want)
			}
		})
	}
}

// An event this build does not read finely still hints its repository — a hint that names too
// much costs a pass, and one that names nothing costs a missed change until the resync catches
// it. A delivery with no repository at all cannot be placed, so it asks for everything.
func TestAnUnplaceableDeliveryAsksForAPass(t *testing.T) {
	f := newFake()
	f.repo("acme/app")

	h := &hinted{}
	post(t, webhook(t, f, h), "release", map[string]any{
		"repository": map[string]any{"full_name": "acme/app"}})
	refs := h.only(t)
	if len(refs) != 1 || refs[0].String() != "github.acme.app" {
		t.Fatalf("an unread event hinted %v, want the repository", refs)
	}

	h2 := &hinted{}
	post(t, webhook(t, f, h2), "meta", map[string]any{})
	if refs := h2.only(t); len(refs) != 0 {
		t.Fatalf("a delivery naming no repository hinted %v, want a full resync", refs)
	}
}

// A delivery about a repository this source does not reflect is not an unplaceable delivery, it
// is somebody else's — and a full pass for each would be a misconfigured hook making this source
// re-read everything it owns forever.
func TestADeliveryForAnotherRepositoryIsIgnored(t *testing.T) {
	f := newFake()
	f.repo("acme/app")
	h := &hinted{}
	post(t, webhook(t, f, h), "push", map[string]any{
		"repository": map[string]any{"full_name": "other/thing"}})
	if len(h.calls) != 0 {
		t.Fatalf("another repository's delivery hinted %v", h.calls)
	}
}

// NO DELIVERY EVER WRITES AN ENTITY. A hint schedules a reconcile that reads GitHub; the payload
// is never the source of what is stored. It is asserted by what the handler CAN do rather than
// by what it does: a delivery carrying a title and a state hints the same ref as one carrying
// only a number, because nothing in it is read.
func TestADeliveryCarriesNothingIntoTheStore(t *testing.T) {
	f := newFake()
	f.repo("acme/app").issue(42, "the real title", "open", "2026-01-01T00:00:00Z")
	h := &hinted{}
	wh := webhook(t, f, h)

	post(t, wh, "issues", map[string]any{
		"repository": map[string]any{"full_name": "acme/app"},
		"issue": map[string]any{
			"number": 42, "title": "A FORGED TITLE", "state": "closed", "body": "nonsense",
		},
	})
	refs := h.only(t)
	if len(refs) != 1 || refs[0].String() != `github.acme.app.issue."42"` {
		t.Fatalf("hinted %v", refs)
	}
	// And the reconcile that hint asks for reads GitHub, which still says what it said.
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})
	rn, err := src.Reflect(context.Background(), refs[0])
	if err != nil {
		t.Fatal(err)
	}
	got := payloads(rn)[`github.acme.app.issue."42"`]
	if got["title"] != "the real title" || got["state"] != "open" {
		t.Fatalf("a delivery's payload reached the store: %v", got)
	}
}

// A targeted run pass covers exactly that run — the mass-deletion trap one kind over — and
// carries the state it reached onto the pull request whose head it ran against, which is what
// makes a workflow_run delivery worth targeting rather than escalating to a repository pass.
func TestATargetedRunPassCoversTheRunAndLandsOnItsPullRequest(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.pr(7, "a change", "open", "2026-01-01T00:00:00Z", "cafe1234")
	r.run(1, "ci", "completed", "success", "cafe1234")
	r.run(2, "ci", "completed", "failure", "cafe1234")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})

	rn, err := src.Reflect(context.Background(), src.RunRef("acme/app", 2))
	if err != nil {
		t.Fatal(err)
	}
	got := payloads(rn)
	if got[`github.acme.app.run."2"`] == nil {
		t.Fatalf("the run is missing: %v", got)
	}
	if got[`github.acme.app.run."1"`] != nil {
		t.Fatal("a targeted run pass answered for another run")
	}
	if c := got[`github.acme.app.pr."7"`]["checks"]; c != "failure" {
		t.Fatalf("the run's state did not reach its pull request: %v", got[`github.acme.app.pr."7"`])
	}
	// The covers name the run and that pull request, and nothing else: a cover over the run
	// window would tombstone every other run in the repository.
	other := entity.Entity{
		Ref:     entity.RefAt("github", "acme", "app", "run", "1"),
		Payload: entity.Obj{"repo": "acme/app", "id": int64(1)}.Payload(),
	}
	for _, c := range rn.Covers {
		if c.MatchEntity(other) {
			t.Fatalf("a pass about run 2 covers run 1: %v", entity.ToObj(c.Predicate))
		}
	}
}
