package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/client"
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
// AND A QUESTION, when the input carries `asks` (step 5; asks.go):
//
//	to:   pr.42
//	text: "Deploy this?"                 # optional: the question says itself without it
//	asks: verse.trigger.rule.deploy~3    # or a list of ids
//
// The comment says what each question asks and how to answer it: a comment on the pull request
// saying `/verse <option>` on a line of its own. Each pass reads the replies and answers one as
// its author, through the author's GitHub account binding, so the ballot is theirs; an author
// verse does not know is told so once. The comment is redrawn as the question settles. The
// status also carries `handled`, the replies already acted on.
//
// AND A REVIEW, when the input carries `review` (step 4):
//
//	to:     pr.42
//	review: {event: request-changes, body: "an objection stands: …"}   # or approve, or comment
//
// answered with `review` (its id), `event`, `url` and `reviewedAt`. A changed event or body posts a
// new review, which supersedes the io's earlier one, since GitHub counts a reviewer's latest; an
// unchanged one costs nothing. GitHub refuses a review from the pull request's own author, so an io
// that opened the pull request cannot review it: the refusal lands on the status.
//
// `comment` is findings that do not block. GitHub counts a reviewer's latest approval or request
// for changes, and a comment is neither, so a comment after either would leave it standing: a
// request for changes still holding the merge, an approval still counting toward it. The io
// dismisses the approvals and requests for changes it left standing before it comments, and what
// it said last is its verdict. If GitHub then refuses the comment, they stay dismissed and no
// comment replaces them: the pull request is left with no verdict of the io's, which blocks
// nothing, as the comment would not have.
//
// `sha` pins the review to the commit it is about (`review: {event: approve, body: …, sha: <head>}`),
// so a verdict reached on one head is not read as one on a head pushed since: GitHub shows it as on
// that commit, and the status carries `sha`, which a review without one removes. A new sha with
// the same verdict is a new review.
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
//
// An `open` input may ALSO carry `text` and `asks`, and then this keeps the comment on the pull
// request it opened as a `pr.<n>` input would, and reads the replies to it (asks.go): a question
// asked by opening the pull request it is about, in one input, for a charter that cannot know the
// number before the pull request exists. The comment's fields stand beside the pull request's on
// the status as `comment`, `commentUrl`, `commentPosted` and `commentAt`. It may carry `merge`,
// which merges the pull request this io opened at the head it was decided on, as a `merge` input
// does, with the same fields; and `closed: true`, which closes it unmerged, once, and never one
// it adopted; `closed` and `closedAt` say so, as a `close` input's do.
//
// AND COMMIT STATUSES, when an `open` input carries `commitStatus`, a map from GitHub's context
// (the name a status stands under on the page) to the status asked for under it:
//
//	commitStatus:
//	  verse:         {state: pending, description: "waiting for an answer", sha: <head>}
//	  "verse/check": {state: success, description: "installs cleanly"}
//
// each a commit status on the pull request's head (`sha`, or the head GitHub reports when it is
// left out), linking to the pull request: `state` is pending, success, failure or error. Each
// context is its own status, so two things said about one pull request stand side by side and
// neither overwrites the other. The status records `commitStatus.<context>`: `state`, `sha`,
// `at` and `posted`; the same state, description and sha again costs nothing, and a changed one
// replaces the status under that context. It takes `statuses: write` on the App. One GitHub
// refuses is `error` under its context, `refused` holding what was refused so it is not asked
// again, and does not fail the input.
//
// AND AN ISSUE, when `open` is asked with `to: issue`:
//
//	to:   issue
//	open: {title: "charters: the install of <tree> failed", body: "…"}
//
// opened once and kept in step, answered with `number, url, postedAt, updatedAt, posted` as a pull
// request is; `text`, `asks`, `commitStatus` (each naming its `sha`: an issue has no head) and
// `closed: true` then apply to it, and `closed: false` reopens it
// where this io closed it (`reopenedAt`): one standing issue a charter opens and closes as what it
// is about goes wrong and right. It is how a charter files what
// went wrong where the repository's people read, with no number to know before the issue exists.
type Sink struct {
	// Verse is the verse this io reads questions and bindings from, as itself. Nil reads none,
	// and an input carrying `asks` is refused for good.
	Verse *client.Client
	// As makes a request as a person this io relays, named with their GitHub account
	// (`github:<user id>`), which is how a `/verse` comment answers a question as its author
	// (asks.go). Nil answers nobody: a command is read and told it cannot count.
	As func(principal, account string) *client.Client

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
		return k.openWith(ctx, repo, in, spec)
	}
	if spec := entity.Get(in.Value, "merge"); spec != nil {
		return k.merge(ctx, repo, in, spec)
	}
	if kind, _, _ := strings.Cut(in.To, "."); kind == "pr" && (entity.Get(in.Value, "commitStatus") != nil ||
		(entity.Get(in.Value, "review") != nil && (entity.Get(in.Value, "text") != nil || entity.Get(in.Value, "asks") != nil))) {
		return k.keepPull(ctx, repo, in)
	}
	if spec := entity.Get(in.Value, "review"); spec != nil {
		return k.review(ctx, repo, in, spec)
	}
	if entity.Get(in.Value, "close") != nil {
		return k.close(ctx, repo, in)
	}
	text := strings.TrimSpace(entity.Str(in.Value, "text"))
	asks := verseio.Strs(entity.Get(in.Value, "asks"))
	if text == "" && len(asks) == 0 {
		return nil, verseio.Permanent(fmt.Errorf("an input carries {to, text}, and this one has no text"))
	}
	var asked []asked
	if len(asks) > 0 {
		var err error
		if text, asked, err = k.asksBody(ctx, text, asks); err != nil {
			return nil, err
		}
	}
	number, err := target(in.To)
	if err != nil {
		return nil, err
	}
	return k.comment(ctx, repo, number, text, asked, in.Status, commentKeys)
}

// statusKeys names the fields a comment's facts stand under on a status: a `pr.<n>` input's own,
// or beside a pull request's on an `open` input that also comments.
type statusKeys struct{ url, posted, postedAt, updatedAt string }

var (
	commentKeys = statusKeys{url: "url", posted: "posted", postedAt: "postedAt", updatedAt: "updatedAt"}
	besideKeys  = statusKeys{url: "commentUrl", posted: "commentPosted", postedAt: "commentAt", updatedAt: "commentAt"}
)

// comment keeps the comment carrying text on #number and, where it asks questions, reads the
// replies that answer them. Nil fields means nothing needed doing.
func (k *Sink) comment(ctx context.Context, repo string, number int, text string, asked []asked, status entity.Payload, keys statusKeys) (entity.Obj, error) {
	fields, err := k.say(ctx, repo, number, text, status, keys)
	if err != nil || len(asked) == 0 {
		return fields, err
	}
	// The comment stands and says what is asked; now the replies, which answer. A reply is
	// judged against the edit the comment SHOWED when it was made, which the status keeps from
	// the pass that last drew it, and not the edit this pass just drew: a question re-posed in
	// between was never seen by the author.
	if fields != nil {
		fields["edits"] = editsOf(asked)
	}
	id, ok := commentID(status)
	if c, _ := fields["comment"].(string); c != "" {
		id, _ = strconv.ParseInt(c, 10, 64)
		ok = true
	}
	if !ok {
		return fields, nil
	}
	more, err := k.answerReplies(ctx, repo, number, id, shownEdits(asked, status), status)
	if err != nil && fields != nil {
		// The comment was made or changed: that is recorded now, with the error beside it, and
		// not lost to the error. Recorded nowhere, the comment's id would be gone and the next
		// pass would say it again.
		fields["error"] = err.Error()
		return fields, nil
	}
	if err != nil {
		return nil, err
	}
	if fields == nil {
		return more, nil
	}
	for k, v := range more {
		fields[k] = v
	}
	return fields, nil
}

// say keeps one comment on #number carrying text, and answers the fields to record under keys.
// Nil fields means the comment already carries the text.
func (k *Sink) say(ctx context.Context, repo string, number int, text string, status entity.Payload, keys statusKeys) (entity.Obj, error) {
	fp := fingerprint(text)
	now := k.now().UTC().Format(time.RFC3339)
	if id, ok := commentID(status); ok {
		if entity.Str(status, keys.posted) == fp {
			return nil, nil // in step: the comment carries this text
		}
		err := k.g.api.updateComment(ctx, repo, id, text)
		if err == nil {
			return entity.Obj{keys.updatedAt: now, keys.posted: fp}, nil
		}
		// GONE: somebody deleted the comment. The input still asks for one saying this, so it is
		// said again, rather than an edit of what is not there refused for good
		// (git-issue ne9fce9dh12ks1q4qsn0).
		if !NotFound(err) {
			return nil, err
		}
	}
	c, err := k.g.api.createComment(ctx, repo, number, text)
	if err != nil {
		return nil, err
	}
	return entity.Obj{
		"comment": strconv.FormatInt(c.ID, 10), keys.url: c.HTMLURL,
		keys.postedAt: now, keys.updatedAt: now, keys.posted: fp,
	}, nil
}

// openWith opens the pull request an `open` input asks for and, where the input also carries
// `text` or `asks`, keeps the comment on it and reads its replies; where it carries `closed:
// true`, closes it. The fields of each stand together on the one status.
func (k *Sink) openWith(ctx context.Context, repo string, in verseio.Input, spec entity.Payload) (entity.Obj, error) {
	fields, err := k.open(ctx, repo, in, spec)
	if err != nil {
		return nil, err
	}
	number, known := prNumber(in.Status)
	if n, ok := fields["number"].(int); ok {
		number, known = n, true
	}
	if !known {
		return fields, nil
	}
	merge := func(more entity.Obj) {
		if len(more) == 0 {
			return
		}
		if fields == nil {
			fields = entity.Obj{}
		}
		for k, v := range more {
			fields[k] = v
		}
	}
	text := strings.TrimSpace(entity.Str(in.Value, "text"))
	asks := verseio.Strs(entity.Get(in.Value, "asks"))
	if text != "" || len(asks) > 0 {
		var asked []asked
		if len(asks) > 0 {
			if text, asked, err = k.asksBody(ctx, text, asks); err != nil {
				return fields, err
			}
		}
		more, err := k.comment(ctx, repo, number, text, asked, in.Status, besideKeys)
		if err != nil {
			if fields == nil {
				return nil, err
			}
			fields["error"] = err.Error()
			return fields, nil
		}
		merge(more)
	}
	if spec := entity.Get(in.Value, "merge"); spec != nil {
		if in.To == issueTarget {
			return fields, verseio.Permanent(fmt.Errorf("an issue is not merged: `merge` is a pull request's"))
		}
		more, err := k.mergeAt(ctx, repo, number, spec, in.Status)
		if err != nil {
			if fields == nil {
				return nil, err
			}
			fields["error"] = err.Error()
			return fields, nil
		}
		merge(more)
	}
	if spec := entity.Get(in.Value, "commitStatus"); spec != nil {
		// BESIDE, NEVER INSTEAD: a status GitHub refuses (an App not yet allowed `statuses:
		// write`) is said on the input's status and does not fail the input, which would stop
		// the rest of it — the replies to its question above all — being asked again. An issue
		// has no head, so a status asked beside one names its commit (pull 0: no head to read).
		pull := number
		if in.To == issueTarget {
			pull = 0
		}
		merge(k.commitStatuses(ctx, repo, pull, spec, in.Status, fields))
	}
	if closed := ir.Uncomment(entity.Get(in.Value, "closed")); closed != nil && closed.Type == ir.BoolType && closed.Bool &&
		entity.Get(in.Status, "closed") == nil && entity.Get(in.Status, "adopted") == nil {
		// CLOSED, ONCE, AND ONLY WHAT THIS IO OPENED: a person's pull request it adopted is
		// theirs to close.
		it, err := k.g.api.readIssue(ctx, repo, number)
		if err != nil {
			return fields, err
		}
		out := entity.Obj{"closed": true}
		if it.State == "closed" {
			out["already"] = true
		} else if it, err = k.g.api.closeIssue(ctx, repo, number); err != nil {
			return fields, err
		}
		if it.ClosedAt != "" {
			out["closedAt"] = it.ClosedAt
		} else {
			out["closedAt"] = k.now().UTC().Format(time.RFC3339)
		}
		merge(out)
	} else if in.To == issueTarget && closed != nil && closed.Type == ir.BoolType && !closed.Bool &&
		entity.Get(in.Status, "closed") != nil {
		// REOPENED, an issue this io filed and closed, where it is asked for open again: one
		// standing issue a charter opens while something is wrong and closes when it is right,
		// across as many rounds as it takes, rather than a new issue each round.
		if _, err := k.g.api.setIssueState(ctx, repo, number, "open"); err != nil {
			return fields, err
		}
		merge(entity.Obj{
			"closed": entity.Removed(), "closedAt": entity.Removed(), "already": entity.Removed(),
			"reopenedAt": k.now().UTC().Format(time.RFC3339),
		})
	}
	return fields, nil
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

// commitStatuses keeps one commit status per context on the pull request's head, as
// `commitStatus` asks: a map from GitHub's context (the name a status stands under on the page,
// `verse`, `verse/check`) to `{state, description, sha}`. Each context is its own status, so two
// rules saying two things (what verse decided, what the charter check found) write two keys and
// neither overwrites the other. What each did is recorded under the same context in the input's
// status, `commitStatus.<context>`.
func (k *Sink) commitStatuses(ctx context.Context, repo string, number int, spec, status entity.Payload, fields entity.Obj) entity.Obj {
	out := entity.Obj{}
	names := verseio.Fields(spec)
	if len(names) == 0 || ir.Uncomment(spec).Type != ir.ObjectType {
		return entity.Obj{"commitStatusError": "`commitStatus` maps a context (`verse`) to {state, description, sha}"}
	}
	sort.Strings(names)
	was := entity.Get(status, "commitStatus")
	// Each status links to the pull request: this pass's url where it opened it, the last pass's
	// otherwise.
	target := entity.Str(status, "url")
	if u, ok := fields["url"].(string); ok && u != "" {
		target = u
	}
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			continue
		}
		if more := k.commitStatus(ctx, repo, number, name, entity.Get(spec, name), entity.Get(was, name), target); more != nil {
			out[name] = more
		}
	}
	if len(out) == 0 {
		return nil
	}
	return entity.Obj{"commitStatus": out}
}

// commitStatus keeps one context's commit status on the pull request's head: `state` (pending,
// success, failure or error), `description`, and `sha`, the head it is about, which defaults to
// the pull request's; beside an issue (number 0) there is no head, and `sha` is required. A status GitHub already has, by state, description and sha, is not posted
// again. `target` is the pull request's url, which the status links to; `status` is what was
// recorded for this context.
//
// It answers fields and never an error: what went wrong is `error`, beside the rest under the
// context. A refusal (a state GitHub has no word for, a 4xx such as a missing permission) is not
// asked again while the same status is asked for, `refused` being its fingerprint; anything else
// is asked again on the next pass.
func (k *Sink) commitStatus(ctx context.Context, repo string, number int, context string, spec, status entity.Payload, target string) entity.Obj {
	failed := func(err error, refusedOn string) entity.Obj {
		if entity.Str(status, "error") == err.Error() && entity.Str(status, "refused") == refusedOn {
			return nil // said already
		}
		out := entity.Obj{"error": err.Error(), "refused": entity.Removed()}
		if refusedOn != "" {
			out["refused"] = refusedOn
		}
		return out
	}
	state := strings.TrimSpace(entity.Str(spec, "state"))
	description := strings.TrimSpace(entity.Str(spec, "description"))
	sha := strings.TrimSpace(entity.Str(spec, "sha"))
	switch state {
	case "pending", "success", "failure", "error":
	default:
		return failed(fmt.Errorf("`commitStatus.%s.state` is pending, success, failure or error, not %q", context, state),
			fingerprint(state+"\x00"+description+"\x00"+sha))
	}
	if sha == "" && number == 0 {
		return failed(fmt.Errorf("`commitStatus.%s` names no sha, and an issue has no head to put it on", context),
			fingerprint(state+"\x00"+description+"\x00"+sha))
	}
	if sha == "" {
		p, err := k.g.api.readPull(ctx, repo, number)
		if err != nil {
			return failed(err, "")
		}
		if p.Head == nil || p.Head.SHA == "" {
			return failed(fmt.Errorf("pull request #%d names no head to put the status on", number), "")
		}
		sha = p.Head.SHA
	}
	fp := fingerprint(state + "\x00" + description + "\x00" + sha)
	if entity.Str(status, "posted") == fp || entity.Str(status, "refused") == fp {
		return nil // GitHub has this status on this head, or refused it as asked
	}
	if err := k.g.api.createStatus(ctx, repo, sha, state, context, description, target); err != nil {
		var he *httpError
		if asHTTPError(err, &he) && he.refusal() {
			return failed(err, fp)
		}
		return failed(err, "")
	}
	out := entity.Obj{"state": state, "sha": sha, "posted": fp, "at": k.now().UTC().Format(time.RFC3339)}
	if entity.Get(status, "error") != nil || entity.Get(status, "refused") != nil {
		out["error"], out["refused"] = entity.Removed(), entity.Removed()
	}
	return out
}

// issueTarget is the `to` of an input that opens an issue: there is no number before it exists.
const issueTarget = "issue"

// open keeps one pull request for a branch: adopted where a person opened one, opened otherwise,
// and, for one this io opened, its title and body in step with the input. With `to: issue` it
// keeps one issue instead (openIssue).
func (k *Sink) open(ctx context.Context, repo string, in verseio.Input, spec entity.Payload) (entity.Obj, error) {
	if in.To == issueTarget {
		return k.openIssue(ctx, repo, in, spec)
	}
	kind, branch, _ := strings.Cut(in.To, ".")
	if kind != "branch" || branch == "" {
		return nil, verseio.Permanent(fmt.Errorf("`open` asks for a pull request from a branch, or an issue: `to` is branch.<name> or issue, not %q", in.To))
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
		// THE IO'S OWN, found again: it opened this one and the status that said so was lost
		// (git-issue ne9fce9dh12ks1q4qsn0). Recorded as opened, its words as GitHub has them, so a
		// title or body that differs is put in step — not frozen as a person's.
		self, err := k.g.api.self(ctx)
		if err != nil {
			return nil, err
		}
		if p.User != nil && strings.EqualFold(p.User.Login, self) {
			return entity.Obj{
				"number": p.Number, "url": p.HTMLURL, "postedAt": now, "updatedAt": now,
				"posted": fingerprint(p.Title + "\x00" + p.Body),
			}, nil
		}
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
		// A HEAD GITHUB DOES NOT HAVE YET is not a refusal of the pull request: the branch is
		// another io's to push (the git io's, for a candidate), and it arrives on its own pass.
		// Said as an ordinary error, so the next pass asks again, rather than GitHub's 422 which
		// Deliver would record as a refusal for good.
		var he *httpError
		if asHTTPError(err, &he) && he.status == 422 &&
			strings.Contains(he.body, `"field":"head"`) {
			return nil, fmt.Errorf("branch %s is not on GitHub yet, so the pull request waits for it", branch)
		}
		return nil, err
	}
	return entity.Obj{
		"number": p.Number, "url": p.HTMLURL, "postedAt": now, "updatedAt": now, "posted": fp,
	}, nil
}

// openIssue keeps one issue for an input: opened once, and its title and body in step with the
// input from then on. It is how a charter files what went wrong where the repository's people
// read, with no number to know before the issue exists; `text`, `asks` and `closed` then apply
// to it as to a pull request this io opened.
func (k *Sink) openIssue(ctx context.Context, repo string, in verseio.Input, spec entity.Payload) (entity.Obj, error) {
	title := strings.TrimSpace(entity.Str(spec, "title"))
	if title == "" {
		return nil, verseio.Permanent(fmt.Errorf("`open` carries a title"))
	}
	body := entity.Str(spec, "body")
	fp := fingerprint(title + "\x00" + body)
	now := k.now().UTC().Format(time.RFC3339)
	if number, ok := prNumber(in.Status); ok {
		if entity.Str(in.Status, "posted") == fp {
			return nil, nil
		}
		if err := k.g.api.updateIssue(ctx, repo, number, title, body); err != nil {
			return nil, err
		}
		return entity.Obj{"updatedAt": now, "posted": fp}, nil
	}
	i, err := k.g.api.createIssue(ctx, repo, title, body)
	if err != nil {
		return nil, err
	}
	return entity.Obj{
		"number": i.Number, "url": i.HTMLURL, "postedAt": now, "updatedAt": now, "posted": fp,
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
	number, err := target(in.To)
	if err != nil {
		return nil, err
	}
	return k.mergeAt(ctx, repo, number, spec, in.Status)
}

// mergeAt merges pull request #number at the head spec names, once; status is what the last pass
// recorded for whatever input asked.
func (k *Sink) mergeAt(ctx context.Context, repo string, number int, spec, status entity.Payload) (entity.Obj, error) {
	if entity.Get(status, "merged") != nil {
		return nil, nil // merged, by this io or before it was asked: nothing is left to do
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

// keepPull keeps several things on one pull request in one input, for a pull request somebody
// else opened: the question's comment (`text`, `asks`; its facts as `comment`, `commentUrl`,
// `commentPosted`, `commentAt`), verse's review (`review`; reviewBeside), and its commit statuses
// (`commitStatus`; commitStatuses). It is how the repo-charters reconciler asks its question on a
// pull request to main, beside its answer and its check as statuses (git-issue dzxn44gw).
//
// The comment's failure is the input's, retried; a review or a status GitHub refuses is said
// beside the rest and does not stop the comment, whose replies are where the answer comes from.
func (k *Sink) keepPull(ctx context.Context, repo string, in verseio.Input) (entity.Obj, error) {
	number, err := target(in.To)
	if err != nil {
		return nil, err
	}
	fields := entity.Obj{}
	merge := func(more entity.Obj) {
		for k, v := range more {
			fields[k] = v
		}
	}
	text := strings.TrimSpace(entity.Str(in.Value, "text"))
	asks := verseio.Strs(entity.Get(in.Value, "asks"))
	if text != "" || len(asks) > 0 {
		var asked []asked
		if len(asks) > 0 {
			if text, asked, err = k.asksBody(ctx, text, asks); err != nil {
				return nil, err
			}
		}
		more, err := k.comment(ctx, repo, number, text, asked, in.Status, besideKeys)
		if err != nil {
			return nil, err
		}
		merge(more)
	}
	if spec := entity.Get(in.Value, "review"); spec != nil {
		merge(k.reviewBeside(ctx, repo, number, spec, in.Status))
	}
	if spec := entity.Get(in.Value, "commitStatus"); spec != nil {
		merge(k.commitStatuses(ctx, repo, number, spec, in.Status, nil))
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return fields, nil
}

// reviewBeside keeps verse's review on a pull request as `review` asks, its facts beside the rest
// of the input's status: `reviewEvent`, `review` (its id), `reviewSha` (the commit it names, when
// it names one), `reviewPosted` (the fingerprint of what it said) and `reviewedAt`.
// `{event: request-changes, body}` blocks a merge where main's protection requires reviews, and
// `comment` lifts that block as it comments; `{event: dismiss, body}` dismisses every standing request for
// changes this io left on the pull request, `body` being the reason GitHub shows, and answers
// `dismissed`, how many. The same review asked again costs nothing. What GitHub refuses is
// `reviewError`, `reviewRefused` holding what was refused so it is not asked again; anything else
// is asked again on the next pass.
func (k *Sink) reviewBeside(ctx context.Context, repo string, number int, spec, status entity.Payload) entity.Obj {
	ask := readReview(spec)
	if entity.Str(status, "reviewPosted") == ask.fp || entity.Str(status, "reviewRefused") == ask.fp {
		return nil
	}
	failed := func(err error) entity.Obj {
		out := entity.Obj{"reviewError": err.Error(), "reviewRefused": entity.Removed()}
		var he *httpError
		if verseio.IsPermanent(err) || (asHTTPError(err, &he) && he.refusal()) {
			out["reviewRefused"] = ask.fp
		}
		if entity.Str(status, "reviewError") == err.Error() {
			return nil
		}
		return out
	}
	now := k.now().UTC().Format(time.RFC3339)
	if ask.word == "dismiss" {
		n, err := k.dismissOwn(ctx, repo, number, ask.body, "CHANGES_REQUESTED")
		if err != nil {
			return failed(err)
		}
		return entity.Obj{"reviewEvent": ask.word, "reviewPosted": ask.fp, "reviewedAt": now, "dismissed": n,
			"review": entity.Removed(), "reviewSha": entity.Removed(),
			"reviewError": entity.Removed(), "reviewRefused": entity.Removed()}
	}
	r, err := k.post(ctx, repo, number, ask)
	if err != nil {
		return failed(err)
	}
	return entity.Obj{"review": strconv.FormatInt(r.ID, 10), "reviewEvent": ask.word, "reviewSha": orRemoved(ask.sha),
		"reviewPosted": ask.fp, "reviewedAt": now, "reviewError": entity.Removed(), "reviewRefused": entity.Removed()}
}

// orRemoved is a status field that says s, or is removed when s is empty, since a status is merged
// into and a field left out would keep what an earlier pass wrote.
func orRemoved(s string) any {
	if s == "" {
		return entity.Removed()
	}
	return s
}

// reviewAsk is what a `review` asks for: the input's word for the event, the body, the commit it
// is about, and the fingerprint the status keeps of all three.
type reviewAsk struct{ word, body, sha, fp string }

// readReview reads a `review`. The sha joins the fingerprint only when one is named, so a review
// asked without one keeps the fingerprint it was posted under.
func readReview(spec entity.Payload) reviewAsk {
	a := reviewAsk{word: entity.Str(spec, "event"), body: entity.Str(spec, "body"), sha: entity.Str(spec, "sha")}
	said := a.word + "\x00" + a.body
	if a.sha != "" {
		said += "\x00" + a.sha
	}
	a.fp = fingerprint(said)
	return a
}

// post posts the review asked for, a dismissal aside. A comment is preceded by dismissing the
// approvals and requests for changes this io left standing, since a comment supersedes neither.
func (k *Sink) post(ctx context.Context, repo string, number int, ask reviewAsk) (review, error) {
	event, ok := reviewEvents[ask.word]
	if !ok {
		return review{}, verseio.Permanent(fmt.Errorf("a review's `event` is approve, request-changes, comment or dismiss, not %q", ask.word))
	}
	if event != "APPROVE" && strings.TrimSpace(ask.body) == "" {
		return review{}, verseio.Permanent(fmt.Errorf("a review that %s carries a body: GitHub refuses one without", map[string]string{
			"REQUEST_CHANGES": "requests changes", "COMMENT": "comments"}[event]))
	}
	if event == "COMMENT" {
		if _, err := k.dismissOwn(ctx, repo, number, "verse: superseded by a later review that comments",
			"APPROVED", "CHANGES_REQUESTED"); err != nil {
			return review{}, err
		}
	}
	return k.g.api.createReview(ctx, repo, number, event, ask.body, ask.sha)
}

// dismissOwn dismisses every review in one of states that this io left standing on a pull request,
// and answers how many: all of them, not only the latest, so no earlier one is left to count.
func (k *Sink) dismissOwn(ctx context.Context, repo string, number int, message string, states ...string) (int, error) {
	if strings.TrimSpace(message) == "" {
		return 0, verseio.Permanent(fmt.Errorf("a dismissal carries a body, the reason GitHub shows"))
	}
	self, err := k.g.api.self(ctx)
	if err != nil {
		return 0, err
	}
	reviews, err := k.g.api.listReviews(ctx, repo, number)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range reviews {
		if !slices.Contains(states, r.State) || r.User == nil || !strings.EqualFold(r.User.Login, self) {
			continue
		}
		if err := k.g.api.dismissReview(ctx, repo, number, r.ID, message); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// reviewEvents maps the input's words to GitHub's events.
var reviewEvents = map[string]string{"approve": "APPROVE", "request-changes": "REQUEST_CHANGES", "comment": "COMMENT"}

// review posts a review on a pull request, and a new one when what it should say changes.
func (k *Sink) review(ctx context.Context, repo string, in verseio.Input, spec entity.Payload) (entity.Obj, error) {
	number, err := target(in.To)
	if err != nil {
		return nil, err
	}
	ask := readReview(spec)
	if entity.Str(in.Status, "posted") == ask.fp {
		return nil, nil // in step: the io's latest review says this
	}
	if ask.word == "dismiss" {
		n, err := k.dismissOwn(ctx, repo, number, ask.body, "CHANGES_REQUESTED")
		if err != nil {
			return nil, err
		}
		return entity.Obj{"event": ask.word, "dismissed": n, "reviewedAt": k.now().UTC().Format(time.RFC3339), "posted": ask.fp,
			"review": entity.Removed(), "url": entity.Removed(), "sha": entity.Removed()}, nil
	}
	r, err := k.post(ctx, repo, number, ask)
	if err != nil {
		return nil, err
	}
	return entity.Obj{
		"review": strconv.FormatInt(r.ID, 10), "event": ask.word, "url": r.HTMLURL, "sha": orRemoved(ask.sha),
		"reviewedAt": k.now().UTC().Format(time.RFC3339), "posted": ask.fp,
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
