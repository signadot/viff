package github_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/signadot/verse/pkg/io/github"
)

// TestCapturedDeliveries replays real webhook payloads through the handler.
//
// It exists because the half of the webhook that can be wrong is whether `delivery`'s field
// paths match GitHub's actual JSON, and that half needs no public address: GitHub keeps recent
// deliveries per hook and hands them back through the API. The other half — whether GitHub can
// reach the endpoint — tests a tunnel and a hook configuration rather than this code.
//
// THE ASSERTION IS "FINER THAN THE REPOSITORY", and it is chosen because a misread field does
// not fail. hintFor falls back to the repository whenever it cannot read a number, so a payload
// whose shape this build guesses wrongly still hints, still converges, and still passes anything
// that only checked for a hint — while costing eight requests per delivery instead of one,
// forever. Degrading to the fallback is exactly what a wrong field path looks like, so that is
// what is refused.
//
// Empty is the ordinary state and skips. See testdata/deliveries/README.md for how to capture.
func TestCapturedDeliveries(t *testing.T) {
	files, err := filepath.Glob("testdata/deliveries/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Skip("no captured deliveries; see testdata/deliveries/README.md")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var captured struct {
				Event   string `json:"event"`
				Want    string `json:"verseWant"`
				Request struct {
					Payload json.RawMessage `json:"payload"`
				} `json:"request"`
			}
			if err := json.Unmarshal(raw, &captured); err != nil {
				t.Fatalf("not a delivery as `gh api` returns one: %v", err)
			}
			if captured.Event == "" || len(captured.Request.Payload) == 0 {
				t.Fatalf("no event or no request.payload — save the delivery verbatim, see README")
			}
			var payload struct {
				Repository struct {
					FullName string `json:"full_name"`
				} `json:"repository"`
			}
			if err := json.Unmarshal(captured.Request.Payload, &payload); err != nil {
				t.Fatalf("payload does not parse: %v", err)
			}
			repo := payload.Repository.FullName
			if repo == "" {
				t.Skip("no repository in the payload; this source has nothing to place it by")
			}

			f := newFake()
			f.repo(repo)
			h := &hinted{}
			src := newSource(t, f, github.Config{Repos: []string{repo}})
			wh := src.Webhook(hookSecret, h.hint)

			req := signedRequest(t, captured.Event, captured.Request.Payload)
			w := newRecorder()
			wh.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("a real delivery was refused: %d %s", w.Code, w.Body.String())
			}
			refs := h.only(t)
			if captured.Want != "" {
				if len(refs) != 1 || refs[0].String() != captured.Want {
					t.Fatalf("hinted %v, want %s", refs, captured.Want)
				}
				return
			}
			if !carriesANumber(captured.Event) {
				return
			}
			if len(refs) != 1 {
				t.Fatalf("a %s delivery hinted %v", captured.Event, refs)
			}
			if refs[0] == src.RepoRef(repo) {
				t.Fatalf("a %s delivery fell back to the repository (%s), which is what a field "+
					"path this build reads wrongly looks like", captured.Event, refs[0])
			}
			t.Logf("%s → %s", captured.Event, refs[0])
		})
	}
}

// carriesANumber reports whether this event names something finer than its repository, and so
// whether falling back to one is evidence of a misread.
func carriesANumber(event string) bool {
	switch event {
	case "issues", "issue_comment", "pull_request", "pull_request_review",
		"pull_request_review_comment", "discussion", "discussion_comment", "workflow_run":
		return true
	}
	return false
}

// signedRequest builds the request GitHub would have sent, from a captured payload.
func signedRequest(t *testing.T, event string, payload []byte) *http.Request {
	t.Helper()
	req := newPost("/github/webhook", payload)
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", github.Sign(hookSecret, payload))
	return req
}
