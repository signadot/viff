package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
)

// The out half: a comment on an issue or a pull request, kept in step with what the verse asks
// for. It is the Linear io's out half pointed at GitHub, and it is the first of what the review
// environment needs from GitHub (git-issue jdvbwvjah12krsstqsn0; the steps are on the branch's
// issue, 934azhtjh12kr7mtqsn0).
//
// Somebody writes an input under a repository's out place, `github.<org>.<name>.out.<name>`, or
// a package writes one in an outbox it declares for that place:
//
//	to:   pr.42              # or issue.42: GitHub numbers both in one sequence
//	text: "the trial found nothing new"
//
// and this answers at `<name>~status`:
//
//	comment:   the comment's id, which is what an update needs
//	url:       where a reader finds it
//	postedAt / updatedAt
//	posted:    the fingerprint of the text the comment now carries
//
// A comment that already carries this text is left alone, and a changed text is an update of the
// same comment rather than a second one.
//
// AND A REVIEW, when the input carries `review` (step 4):
//
//	to:     pr.42
//	review: {event: request-changes, body: "an objection stands: …"}   # or approve
//
// answered with `review` (its id), `event`, `url` and `reviewedAt`. A changed event or body posts a
// new review, which supersedes the io's earlier one, since GitHub counts a reviewer's latest; an
// unchanged one costs nothing. GitHub refuses a review from the pull request's own author, so an io
// that opened the pull request cannot review it: the refusal lands on the status.
//
// AND A CLOSE, when the input carries `close`:
//
//	to:    pr.42      # or issue.42
//	close: {}
//
// answered with `closed: true`, `closedAt`, and `closedBy`, the account GitHub says closed it. A
// pull request is closed unmerged. One already closed is recorded as it is, with `already: true`:
// that is how a close made around verse shows on the status. `to` says which kind it closes, and a
// number that is the other kind is refused, since closing the wrong thing is not undone by a
// status.
//
// AND A MERGE AT A SHA, when the input carries `merge` (step 3):
//
//	to:    pr.42
//	merge: {sha: <head>, method: merge}   # method is merge, squash or rebase; merge by default
//
// merged only while the pull request's head is still that sha, and answered with `merged: true`,
// the merge commit as `commit`, and `mergedAt`. A pull request already merged is recorded as it is,
// with no `commit` of this io's: that is how a merge made around verse shows on the status.
//
// AND A PULL REQUEST FOR A BRANCH, when the input carries `open` (step 2):
//
//	to:   branch.verse/repo-charter-review-x     # everything after the first dot is the branch
//	open: {base: main, title: "…", body: "…"}  # base defaults to the repository's default branch
//
// answered with `number, url, postedAt, updatedAt, posted`, and `adopted: true` where a person had
// already opened one from that branch. A pull request this io opened follows the input's title
// and body; one it ADOPTED keeps its author's, since what a person wrote on their own pull request
// is not this io's to rewrite.
type Sink struct {
	g   *GitHub
	now func() time.Time
}

// Sink is this source's out half, over every repository it reflects.
func (g *GitHub) Sink() *Sink { return &Sink{g: g, now: time.Now} }

// OutPlace is the segment a repository's out place ends in.
const OutPlace = "out"

// OutRefs are the places inputs are written under: one per repository, inside the repository's
// own claim and outside every cover the reflection carries (`repo`, `issue.*`, `pr.*`, `run.*`,
// `comment.*`), so no pass tombstones what somebody asked for.
func (g *GitHub) OutRefs() []entity.Ref {
	out := make([]entity.Ref, 0, len(g.repos))
	for _, r := range g.repos {
		out = append(out, entity.RefAt(g.place(r, OutPlace)...))
	}
	return out
}

// repoOf is the repository whose out place an input was asked at — an outbox's input arrives
// with the out place it sends to — or "" for a place this source does not serve.
func (g *GitHub) repoOf(place entity.Ref) string {
	for _, r := range g.repos {
		if entity.RefAt(g.place(r, OutPlace)...).String() == place.String() {
			return r
		}
	}
	return ""
}

// target reads an input's `to`: `pr.<n>` or `issue.<n>`.
func target(to string) (int, error) {
	kind, num, ok := strings.Cut(to, ".")
	if kind == "branch" {
		return 0, verseio.Permanent(fmt.Errorf("`to` %q names a branch, which takes `open`; a comment goes on pr.<number> or issue.<number>", to))
	}
	if !ok || (kind != "pr" && kind != "issue") {
		return 0, verseio.Permanent(fmt.Errorf("`to` is pr.<number> or issue.<number>, not %q", to))
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return 0, verseio.Permanent(fmt.Errorf("`to` %q: %q is not a pull request or issue number", to, num))
	}
	return n, nil
}

// Deliver does what one input asks, and says GitHub's refusal of it as permanent (a 4xx other than
// a timeout or a rate limit), so it is not asked again until the input changes (verseio.Permanent).
func (k *Sink) Deliver(ctx context.Context, in verseio.Input) (entity.Obj, error) {
	fields, err := k.deliver(ctx, in)
	var he *httpError
	if err != nil && !verseio.IsPermanent(err) && asHTTPError(err, &he) && he.refusal() {
		err = verseio.Permanent(err)
	}
	return fields, err
}

// deliver comments on one issue or pull request, or updates the comment it already left there.
func (k *Sink) deliver(ctx context.Context, in verseio.Input) (entity.Obj, error) {
	repo := k.g.repoOf(in.Place)
	if repo == "" {
		return nil, verseio.Permanent(fmt.Errorf("%s is no repository's out place this io serves", in.Place))
	}
	if spec := entity.Get(in.Value, "open"); spec != nil {
		return k.open(ctx, repo, in, spec)
	}
	if spec := entity.Get(in.Value, "merge"); spec != nil {
		return k.merge(ctx, repo, in, spec)
	}
	if spec := entity.Get(in.Value, "review"); spec != nil {
		return k.review(ctx, repo, in, spec)
	}
	if entity.Get(in.Value, "close") != nil {
		return k.close(ctx, repo, in)
	}
	text := strings.TrimSpace(entity.Str(in.Value, "text"))
	if text == "" {
		return nil, verseio.Permanent(fmt.Errorf("an input carries {to, text}, and this one has no text"))
	}
	fp := fingerprint(text)
	now := k.now().UTC().Format(time.RFC3339)
	if id, ok := commentID(in.Status); ok {
		if entity.Str(in.Status, "posted") == fp {
			return nil, nil // in step: the comment carries this text
		}
		if err := k.g.api.updateComment(ctx, repo, id, text); err != nil {
			return nil, err
		}
		return entity.Obj{"updatedAt": now, "posted": fp}, nil
	}
	number, err := target(in.To)
	if err != nil {
		return nil, err
	}
	c, err := k.g.api.createComment(ctx, repo, number, text)
	if err != nil {
		return nil, err
	}
	return entity.Obj{
		"comment": strconv.FormatInt(c.ID, 10), "url": c.HTMLURL,
		"postedAt": now, "updatedAt": now, "posted": fp,
	}, nil
}

// commentID is the comment a status says this input made, if it made one.
func commentID(status entity.Payload) (int64, bool) {
	s := entity.Str(status, "comment")
	if s == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil
}

// fingerprint is a short, stable digest of what a comment should say, so a changed text is an
// update and an unchanged one is nothing at all.
func fingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

// open keeps one pull request for a branch: adopted where a person opened one, opened otherwise,
// and, for one this io opened, its title and body in step with the input.
func (k *Sink) open(ctx context.Context, repo string, in verseio.Input, spec entity.Payload) (entity.Obj, error) {
	kind, branch, _ := strings.Cut(in.To, ".")
	if kind != "branch" || branch == "" {
		return nil, verseio.Permanent(fmt.Errorf("`open` asks for a pull request from a branch: `to` is branch.<name>, not %q", in.To))
	}
	title := strings.TrimSpace(entity.Str(spec, "title"))
	if title == "" {
		return nil, verseio.Permanent(fmt.Errorf("`open` carries a title"))
	}
	body := entity.Str(spec, "body")
	fp := fingerprint(title + "\x00" + body)
	now := k.now().UTC().Format(time.RFC3339)
	if number, ok := prNumber(in.Status); ok {
		if entity.Get(in.Status, "adopted") != nil || entity.Str(in.Status, "posted") == fp {
			return nil, nil // in step, or a person's pull request whose words are theirs
		}
		if err := k.g.api.updatePull(ctx, repo, number, title, body); err != nil {
			return nil, err
		}
		return entity.Obj{"updatedAt": now, "posted": fp}, nil
	}
	if p, ok, err := k.g.api.openPullFor(ctx, repo, branch); err != nil {
		return nil, err
	} else if ok {
		return entity.Obj{"number": p.Number, "url": p.HTMLURL, "adopted": true, "postedAt": now}, nil
	}
	base := entity.Str(spec, "base")
	if base == "" {
		r, _, err := k.g.api.readRepo(ctx, repo)
		if err != nil {
			return nil, err
		}
		base = r.DefaultBranch
	}
	p, err := k.g.api.createPull(ctx, repo, branch, base, title, body)
	if err != nil {
		return nil, err
	}
	return entity.Obj{
		"number": p.Number, "url": p.HTMLURL, "postedAt": now, "updatedAt": now, "posted": fp,
	}, nil
}

// prNumber is the pull request a status says this input opened or adopted.
func prNumber(status entity.Payload) (int, bool) {
	n := ir.Uncomment(entity.Get(status, "number"))
	if n == nil || n.Int64 == nil {
		return 0, false
	}
	return int(*n.Int64), true
}

// merge merges a pull request at the sha it was asked for, once.
func (k *Sink) merge(ctx context.Context, repo string, in verseio.Input, spec entity.Payload) (entity.Obj, error) {
	if entity.Get(in.Status, "merged") != nil {
		return nil, nil // merged, by this io or before it was asked: nothing is left to do
	}
	number, err := target(in.To)
	if err != nil {
		return nil, err
	}
	sha := entity.Str(spec, "sha")
	if sha == "" {
		return nil, verseio.Permanent(fmt.Errorf("`merge` carries the sha it merges at, so a head that moved since " +
			"the decision is not merged"))
	}
	p, err := k.g.api.readPull(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	now := k.now().UTC().Format(time.RFC3339)
	if p.Merged {
		// Merged before this io was asked to: said as it is, with no commit of this io's.
		out := entity.Obj{"merged": true, "mergedAt": p.MergedAt}
		if p.Head != nil {
			out["head"] = p.Head.SHA
		}
		return out, nil
	}
	if p.Head == nil || p.Head.SHA != sha {
		head := ""
		if p.Head != nil {
			head = p.Head.SHA
		}
		return nil, verseio.Permanent(fmt.Errorf("pr.%d's head is %s, and the merge was decided at %s: a head that "+
			"moved is a different change, so it is not merged", number, head, sha))
	}
	m, err := k.g.api.mergePull(ctx, repo, number, sha, entity.Str(spec, "method"))
	if err != nil {
		return nil, err
	}
	return entity.Obj{"merged": true, "commit": m.SHA, "head": sha, "mergedAt": now}, nil
}

// reviewEvents maps the input's words to GitHub's events.
var reviewEvents = map[string]string{"approve": "APPROVE", "request-changes": "REQUEST_CHANGES"}

// review posts a review on a pull request, and a new one when what it should say changes.
func (k *Sink) review(ctx context.Context, repo string, in verseio.Input, spec entity.Payload) (entity.Obj, error) {
	number, err := target(in.To)
	if err != nil {
		return nil, err
	}
	word := entity.Str(spec, "event")
	event, ok := reviewEvents[word]
	if !ok {
		return nil, verseio.Permanent(fmt.Errorf("a review's `event` is approve or request-changes, not %q", word))
	}
	body := entity.Str(spec, "body")
	if event == "REQUEST_CHANGES" && strings.TrimSpace(body) == "" {
		return nil, verseio.Permanent(fmt.Errorf("a review requesting changes carries a body: GitHub refuses one without"))
	}
	fp := fingerprint(word + "\x00" + body)
	if entity.Str(in.Status, "posted") == fp {
		return nil, nil // in step: the io's latest review says this
	}
	r, err := k.g.api.createReview(ctx, repo, number, event, body)
	if err != nil {
		return nil, err
	}
	return entity.Obj{
		"review": strconv.FormatInt(r.ID, 10), "event": word, "url": r.HTMLURL,
		"reviewedAt": k.now().UTC().Format(time.RFC3339), "posted": fp,
	}, nil
}

// close closes an issue or a pull request, once.
func (k *Sink) close(ctx context.Context, repo string, in verseio.Input) (entity.Obj, error) {
	if entity.Get(in.Status, "closed") != nil {
		return nil, nil // closed, by this io or before it was asked: nothing is left to do
	}
	number, err := target(in.To)
	if err != nil {
		return nil, err
	}
	it, err := k.g.api.readIssue(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	asked, _, _ := strings.Cut(in.To, ".")
	if is := map[bool]string{true: "pr", false: "issue"}[it.isPullRequest()]; is != asked {
		return nil, verseio.Permanent(fmt.Errorf("`to` is %s, and #%d is %s.%d: nothing is closed that was not named",
			in.To, number, is, number))
	}
	out := entity.Obj{"closed": true}
	if it.State == "closed" {
		out["already"] = true // closed before this io was asked to: said as it is
	} else if it, err = k.g.api.closeIssue(ctx, repo, number); err != nil {
		return nil, err
	}
	if it.ClosedAt != "" {
		out["closedAt"] = it.ClosedAt
	} else {
		out["closedAt"] = k.now().UTC().Format(time.RFC3339)
	}
	if it.ClosedBy != nil && it.ClosedBy.Login != "" {
		out["closedBy"] = it.ClosedBy.Login
	}
	return out, nil
}
