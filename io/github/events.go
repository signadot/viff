package github

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

	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
)

// The delivery half: a GitHub webhook, received HERE.
//
// The same object as the Slack events endpoint, the Linear webhook, and the four-line `/bin/sh`
// git hook `Git.InstallHook` writes — a delivery the source owns, turned into which ref went
// stale, posted to verse. verse parses none of the four, which is what keeps "map a delivery to
// refs" from having a second implementation per source, on the far side of a network where a
// divergence is least recoverable.
//
// A forged delivery costs a reconcile of a slice its sender does not get to choose the contents
// of. The signature check is here because GitHub's contract asks for it, and because an
// unverified public endpoint is somebody else's API budget to spend — not because the hint law
// depends on it.

const webhookMaxBody = 8 << 20

// Webhook returns the handler for a GitHub webhook url.
func (g *GitHub) Webhook(secret string, hint verseio.Hinter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if secret == "" {
			// Refused rather than trusted, as in the Slack and Linear sources: the hint law says
			// a forged delivery is cheap, but silently accepting unsigned ones would be a claim
			// about GitHub's contract that this program is not entitled to make.
			http.Error(w, "no webhook secret configured", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, webhookMaxBody))
		if err != nil {
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}
		if err := verify(secret, r.Header.Get("X-Hub-Signature-256"), body); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		event := r.Header.Get("X-GitHub-Event")
		if event == "ping" {
			// What GitHub sends when the hook is created. Answered, and nothing is stale.
			w.WriteHeader(http.StatusOK)
			return
		}
		var d delivery
		if err := json.Unmarshal(body, &d); err != nil {
			http.Error(w, "undecodable body", http.StatusBadRequest)
			return
		}
		if hint != nil {
			g.hintFor(r, event, d, hint)
		}
		w.WriteHeader(http.StatusOK)
	})
}

// hintFor turns one delivery into the reconcile it asks for.
//
// THE HINT NAMES THE THING, NOT ITS CONTAINER, and this is the first source where that pays.
// git's targeted pass is a whole repository because git is content-addressed and a finer target
// costs the same; Linear's is a whole team because that is what one GraphQL query reads. GitHub
// answers a single issue in one request, so a delivery about #42 costs one call rather than the
// eight a repository pass costs — which matters most for `workflow_run`, the highest-frequency
// event there is: a push through three statuses of two workflows is six deliveries, and at a
// repository each that is a budget nobody has.
//
// A delivery this source cannot place asks for a FULL resync rather than nothing, because a
// hint that names too much costs a pass while one that names nothing costs a missed change until
// the resync net catches it. The exception is a delivery about a repository this source
// provably does not reflect: that is not an unplaceable delivery, it is somebody else's, and a
// full pass would be a misconfigured hook making this source re-read everything it owns forever.
func (g *GitHub) hintFor(r *http.Request, event string, d delivery, hint verseio.Hinter) {
	repo := d.Repository.FullName
	if repo != "" && !g.reflects(repo) {
		return
	}
	if repo == "" {
		hint(r.Context()) // nothing to place it by
		return
	}
	switch event {
	case "issue_comment", "pull_request_review_comment":
		// The comment is the new thing, so it is what the hint names — one request against the
		// nine a repository pass costs, and against the item pass that would refresh a title
		// nobody changed. The item it is on is unaffected by a comment arriving: its own
		// payload carries no count, deliberately.
		if id := d.Comment.ID; id > 0 {
			if n := d.Issue.Number; n > 0 {
				hint(r.Context(), g.CommentRef(repo, n, id))
				return
			}
			if n := d.PullRequest.Number; n > 0 {
				hint(r.Context(), g.CommentRef(repo, n, id))
				return
			}
		}
	case "issues":
		// An `issues` delivery is about the item itself. (An `issue_comment` fires for a pull
		// request's conversation too — on GitHub a PR IS an issue for commenting — which is why
		// the comment arm above reads the number from whichever of the two the payload carried.)
		if n := d.Issue.Number; n > 0 {
			kind := "issue"
			if d.Issue.PullRequest != nil {
				kind = "pr"
			}
			hint(r.Context(), g.ItemRef(kind, repo, n))
			return
		}
	case "pull_request", "pull_request_review":
		if n := d.PullRequest.Number; n > 0 {
			hint(r.Context(), g.ItemRef("pr", repo, n))
			return
		}
	case "discussion", "discussion_comment":
		if n := d.Discussion.Number; n > 0 {
			hint(r.Context(), g.ItemRef("discussion", repo, n))
			return
		}
	case "workflow_run":
		if id := d.WorkflowRun.ID; id > 0 {
			// The run, and with it the pull request whose head it ran against: a targeted run
			// pass carries the check state onto that PR, so "CI went red" reaches a charter
			// without a repository pass. See reflectRun.
			hint(r.Context(), g.RunRef(repo, id))
			return
		}
	case "repository", "push", "check_suite", "check_run":
		// About the repository itself, or about CI in a shape this source reads through runs
		// rather than directly. Either way the repository is the finest thing that is stale.
		hint(r.Context(), g.RepoRef(repo))
		return
	}
	// A known repository and an event this build does not read finely: a repository pass is
	// right and cheap enough, and cannot be wrong.
	hint(r.Context(), g.RepoRef(repo))
}

// reflects reports whether this source is responsible for a repository.
func (g *GitHub) reflects(repo string) bool {
	for _, r := range g.repos {
		if r == repo {
			return true
		}
	}
	return false
}

// delivery is the part of a webhook body that says which slice went stale, and nothing else.
//
// Nothing here becomes an entity: reading a delivery's payload into the store is the mistake the
// hint law exists to prevent, so this type is deliberately incapable of it — no titles, no
// states, no bodies. What a reconcile writes comes from a read of GitHub, always.
type delivery struct {
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Issue struct {
		Number int `json:"number"`
		// Present when the "issue" is really a pull request, which is how an issue_comment on a
		// PR arrives. The same distinction step 1's listing filter makes.
		PullRequest *struct{} `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		ID int64 `json:"id"`
	} `json:"comment"`
	PullRequest struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Discussion struct {
		Number int `json:"number"`
	} `json:"discussion"`
	WorkflowRun struct {
		ID int64 `json:"id"`
	} `json:"workflow_run"`
}

// verify checks a delivery's signature: `sha256=` and the hex HMAC-SHA256 of the raw body.
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

// Sign spells a delivery's signature, which is what a test needs to send one and what nothing in
// production calls. It is here rather than in the test file because the handler and its sender
// must agree on what is signed, and two copies of that agreement is how a test comes to pass
// against a rule the handler does not apply.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// targetComment reads a ref as one comment: `<system>.<org>.<name>.comment.<n>.<id>`.
func (g *GitHub) targetComment(ref entity.Ref) (repo string, number int, id int64, ok bool) {
	repo, rest, under := g.under(ref)
	if !under || len(rest) != 3 || rest[0] != "comment" {
		return "", 0, 0, false
	}
	n, err1 := strconv.Atoi(rest[1])
	i, err2 := strconv.ParseInt(rest[2], 10, 64)
	if err1 != nil || err2 != nil || n <= 0 || i <= 0 {
		return "", 0, 0, false
	}
	return repo, n, i, true
}

// reflectComment answers for ONE comment, and covers exactly it.
//
// It asks both endpoints, because a delivery says which kind of comment it was and this ref does
// not — and inventing a `kind` segment in the address to carry that would put GitHub's plumbing
// in an id a charter reads. Two requests, of which the second only happens when the first says
// no; a comment that is neither is covered and absent, which tombstones one that was deleted.
func (g *GitHub) reflectComment(ctx context.Context, repo string, number int, id int64) (verseio.Reflection, error) {
	rn := verseio.Reflection{Covers: []entity.Pattern{
		g.cover(repo, "comment", strconv.Itoa(number), strconv.FormatInt(id, 10)),
	}}
	for _, where := range []struct{ path, kind string }{
		{"issues", "conversation"}, {"pulls", "review"},
	} {
		var c comment
		err := g.api.get(ctx, "/repos/"+repo+"/"+where.path+"/comments/"+strconv.FormatInt(id, 10), &c)
		switch {
		case err == nil:
			rn.Add(g.CommentRef(repo, number, c.ID), commentPayload(repo, c, where.kind))
			return rn, nil
		case NotFound(err):
			continue
		default:
			return verseio.Reflection{}, err
		}
	}
	return rn, nil // gone from both: covered and absent, which tombstones it
}

// ── the targeted run pass a workflow_run delivery asks for ───────────────────

// targetRun reads a ref as one workflow run.
func (g *GitHub) targetRun(ref entity.Ref) (repo string, id int64, ok bool) {
	repo, rest, under := g.under(ref)
	if !under || len(rest) != 2 || rest[0] != "run" {
		return "", 0, false
	}
	n, err := strconv.ParseInt(rest[1], 10, 64)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return repo, n, true
}

// reflectRun answers for ONE workflow run, and covers exactly it.
//
// The cover is the run's own address, for step 1's reason one kind over: a targeted pass that
// declared the window's cover while carrying one run would tombstone every other run in the
// repository.
//
// IT ALSO CARRIES THE CHECK STATE ONTO THE PULL REQUEST the run ran for, which is what makes
// this delivery worth targeting rather than escalating to a repository pass. A run finishing is
// how "CI went red" reaches a charter, and the charter reads that off the PR. Two calls — the
// run, and the pull requests it names — against the eight a repository pass costs.
func (g *GitHub) reflectRun(ctx context.Context, repo string, id int64) (verseio.Reflection, error) {
	rn := verseio.Reflection{Covers: []entity.Pattern{
		g.cover(repo, "run", strconv.FormatInt(id, 10)),
	}}
	var r workflowRun
	if err := g.api.get(ctx, "/repos/"+repo+"/actions/runs/"+strconv.FormatInt(id, 10), &r); err != nil {
		if NotFound(err) {
			return rn, nil // gone: covered and absent, which tombstones it
		}
		return verseio.Reflection{}, err
	}
	rn.Add(g.RunRef(repo, r.ID), runPayload(repo, r))

	// The pull requests this run's head belongs to, so the state it just reached lands where a
	// charter reads it. Covered per pull request, exactly as a targeted item pass is: this
	// answer is complete for those and says nothing about any other.
	if r.HeadSHA == "" {
		return rn, nil
	}
	prs, err := g.api.pullsForSHA(ctx, repo, r.HeadSHA)
	if err != nil {
		// The run is reflected either way; the PR's check field catches up on the next
		// repository pass. Not worth failing a delivery over.
		g.log("github: %s: could not attach run %d to its pull requests: %v", repo, id, err)
		return rn, nil
	}
	state := runState(r)
	for _, p := range prs {
		payload := pullPayload(repo, p)
		payload["checks"] = state
		g.withTrees(ctx, repo, p, payload)
		rn.Covers = append(rn.Covers, g.cover(repo, "pr", strconv.Itoa(p.Number)))
		rn.Add(g.ItemRef("pr", repo, p.Number), payload)
	}
	return rn, nil
}
