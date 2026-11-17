package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GitHub's REST API, as much of it as a reflection needs.
//
// encoding/json rather than codec, and this file is on scripts/check-json.sh's allowlist for
// the reason source/linear/api.go is: it is somebody else's wire format, and re-rendering it
// through codec would buy nothing but a second place for it to be wrong.

// APIBase is api.github.com. Overridable for GitHub Enterprise Server, and for a test server.
const APIBase = "https://api.github.com"

// pageSize is what every listing asks for. 100 is GitHub's maximum, and the number of requests
// per pass is the binding constraint here (see the note's budget arithmetic), so there is no
// reason to ask for fewer.
const pageSize = 100

type api struct {
	cred credential // nil sends no Authorization, which only a test server accepts
	base string
	cl   *http.Client

	mu     sync.Mutex
	cached map[string]cachedResponse
	login  string // who the credential is, once asked (self)
}

// cachedResponse is one endpoint's last successful answer, kept for the conditional request
// that asks whether it is still current.
//
// THE BODY, not the decoded value, because this cache is keyed by url and shared by every
// caller of get: one that decoded into a type would have to know which. Re-decoding a 304's
// body costs a parse and saves a round trip, which is the trade in the right direction.
type cachedResponse struct {
	etag string
	body []byte
	link string // the Link header, which a listing paged by cursor reads its next page from
}

func newAPI(token string, app *AppAuth, base string, cl *http.Client) (*api, error) {
	if base == "" {
		base = APIBase
	}
	if cl == nil {
		cl = &http.Client{Timeout: 60 * time.Second}
	}
	a := &api{base: strings.TrimRight(base, "/"), cl: cl, cached: map[string]cachedResponse{}}
	switch {
	case app != nil:
		inst, err := newInstallation(*app, a.base, cl)
		if err != nil {
			return nil, err
		}
		a.cred = inst
	case token != "":
		a.cred = bearer(token)
	}
	return a, nil
}

// authorize sets the request's Authorization, minting an installation token first where the one
// held is about to expire.
func (a *api) authorize(req *http.Request) error {
	if a.cred == nil {
		return nil
	}
	v, err := a.cred.authorization(req.Context())
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", v)
	return nil
}

// get issues one conditional GET and decodes the answer into out.
//
// CONDITIONAL FROM THE FIRST COMMIT, rather than as a later optimisation, because at this
// connector's scale it is not one. A pass over one repository is six requests, so a
// once-a-minute resync spends about 360 an hour on it — and a personal token has 5,000. GitHub
// documents a 304 as not counting against the primary rate limit, so a repository where nothing
// happened costs nothing, which is what makes more than a dozen of them possible at all. It is
// also a transport concern, and cheaper to build in than to retrofit around a client that never
// had a cache.
//
// A 304 answers from the last body this url returned. The cache holds only what a 200 put
// there, so it can be lost without being wrong: losing it costs one full read, never a wrong
// answer. That is the rule the whole design applies to a source's state — a source may keep
// what costs work to lose, never what costs correctness.
//
// A FAILURE IS NEVER CACHED, and never answers from the cache either. An error here has to
// reach the reflector as an error: an empty answer carrying covers is a legitimate instruction
// to tombstone everything under them, so a source that turned a bad minute into "the repository
// has no issues" would delete a slice.
func (a *api) get(ctx context.Context, path string, out any) error {
	return a.read(ctx, path, out, true)
}

// getOnce is a GET of what is read once and not again — one pull request or issue, read for an
// act on it — and so is neither cached nor conditional: a body kept for every number the out half
// ever acts on is memory spent on reads that never repeat (git-issue ne9fce9dh12ks1q4qsn0).
func (a *api) getOnce(ctx context.Context, path string, out any) error {
	return a.read(ctx, path, out, false)
}

// read is get, and getOnce where keep is false.
func (a *api) read(ctx context.Context, path string, out any, keep bool) error {
	_, err := a.readPage(ctx, path, out, keep)
	return err
}

// readPage is read, also answering the response's Link header: what a listing paged by cursor
// reads its next page from. A 304 answers the Link the cached body came with.
func (a *api) readPage(ctx context.Context, path string, out any, keep bool) (string, error) {
	target := a.base + path
	var prev cachedResponse
	hadPrev := false
	if keep {
		a.mu.Lock()
		prev, hadPrev = a.cached[target]
		a.mu.Unlock()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("github: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if err := a.authorize(req); err != nil {
		return "", err
	}
	if hadPrev && prev.etag != "" {
		req.Header.Set("If-None-Match", prev.etag)
	}

	resp, err := a.do(req, nil)
	if err != nil {
		return "", fmt.Errorf("github: GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified && hadPrev {
		return prev.link, json.Unmarshal(prev.body, out)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", fmt.Errorf("github: GET %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		// Said with the status and the body, because the two failures an operator actually
		// hits are told apart by them: a 401 is the token, a 403 with a rate-limit body is the
		// budget, and "GET failed" tells them neither.
		return "", &httpError{status: resp.StatusCode, path: path, body: string(trimBody(body)),
			limited: rateLimited(resp, body)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return "", fmt.Errorf("github: GET %s: %w", path, err)
	}
	link := resp.Header.Get("Link")
	if etag := resp.Header.Get("ETag"); etag != "" && keep {
		a.mu.Lock()
		a.cached[target] = cachedResponse{etag: etag, body: body, link: link}
		a.mu.Unlock()
	}
	return link, nil
}

// do sends req, and sends it once more with a token minted afresh where GitHub refused the one it
// carried (401) and the credential is one that mints. The io replaces a token before the expiry
// GitHub gave, by its own clock; a clock running slow, or a token revoked, is a 401 that is the
// io's to fix, not GitHub refusing the request, and a refusal is recorded as permanent
// (git-issue ne9fce9dh12ks1q4qsn0). A 401 that a fresh token gets too is believed.
func (a *api) do(req *http.Request, body []byte) (*http.Response, error) {
	resp, err := a.cl.Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	r, ok := a.cred.(renewable)
	if !ok {
		return resp, nil
	}
	r.invalidate(req.Header.Get("Authorization"))
	again := req.Clone(req.Context())
	if body != nil {
		again.Body = io.NopCloser(bytes.NewReader(body))
	}
	if err := a.authorize(again); err != nil {
		return resp, nil // the 401 stands, and says more than a failure to mint would
	}
	resp.Body.Close()
	return a.cl.Do(again)
}

// maxBody bounds one response. A listing of 100 issues with bodies is comfortably inside it;
// anything past it is a surface this source does not understand.
const maxBody = 32 << 20

func trimBody(b []byte) []byte {
	if len(b) > 512 {
		return b[:512]
	}
	return b
}

// httpError is a non-200 from GitHub, carrying enough to tell the ordinary failures apart.
type httpError struct {
	status int
	method string // "" is GET, which is every read
	path   string
	body   string
	// limited is a refusal that is GitHub's rate limit, primary or secondary, and not a refusal of
	// the request: it fixes itself, and asking again later is right.
	limited bool
}

// refusal is whether GitHub refused the request itself, which asking the same way again would not
// change: a 4xx other than a timeout or a rate limit (git-issue na9fce9dh12krdydqsn0).
func (e *httpError) refusal() bool {
	return e.status >= 400 && e.status < 500 && e.status != http.StatusRequestTimeout &&
		e.status != http.StatusTooManyRequests && !e.limited
}

// rateLimited is whether an answer is GitHub's rate limit: a 429, or a 403 that says so —
// `x-ratelimit-remaining: 0` for the primary limit, `retry-after` or its own words for the
// secondary one.
func rateLimited(resp *http.Response, body []byte) bool {
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return true
	case http.StatusForbidden:
		return resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != "" ||
			bytes.Contains(bytes.ToLower(body), []byte("rate limit"))
	}
	return false
}

func (e *httpError) Error() string {
	method := e.method
	if method == "" {
		method = http.MethodGet
	}
	return fmt.Sprintf("github: %s %s: %s: %s", method, e.path, http.StatusText(e.status), e.body)
}

// send issues one write — a POST, PATCH or PUT with a JSON body — and decodes the answer into out
// when it is not nil.
//
// NOT CONDITIONAL AND NOT CACHED, which is the difference from get: a write asks GitHub to do
// something, so an earlier answer cannot stand in for it, and its body is not what any read would
// return. A failure says the method, the status and GitHub's own words, as a read's does.
func (a *api) send(ctx context.Context, method, path string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	if err := a.authorize(req); err != nil {
		return err
	}
	resp, err := a.do(req, raw)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &httpError{status: resp.StatusCode, method: method, path: path, body: string(trimBody(answer)),
			limited: rateLimited(resp, answer)}
	}
	if out == nil || len(answer) == 0 {
		return nil
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	return nil
}

// postedComment is what GitHub answers about a comment it made or changed.
type postedComment struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
}

// createComment comments on an issue or a pull request, which GitHub numbers in one sequence and
// comments on through one route.
func (a *api) createComment(ctx context.Context, repo string, number int, text string) (postedComment, error) {
	var c postedComment
	err := a.send(ctx, http.MethodPost, "/repos/"+repo+"/issues/"+strconv.Itoa(number)+"/comments",
		map[string]string{"body": text}, &c)
	return c, err
}

// openPullFor is the open pull request whose head is branch, in repo's own organization, if there
// is one.
func (a *api) openPullFor(ctx context.Context, repo, branch string) (pull, bool, error) {
	org, _, _ := strings.Cut(repo, "/")
	var ps []pull
	if err := a.getOnce(ctx, "/repos/"+repo+"/pulls?state=open&head="+url.QueryEscape(org+":"+branch), &ps); err != nil {
		return pull{}, false, err
	}
	if len(ps) == 0 {
		return pull{}, false, nil
	}
	return ps[0], true, nil
}

// createPull opens a pull request from head into base.
func (a *api) createPull(ctx context.Context, repo, head, base, title, body string) (pull, error) {
	var p pull
	err := a.send(ctx, http.MethodPost, "/repos/"+repo+"/pulls",
		map[string]string{"head": head, "base": base, "title": title, "body": body}, &p)
	return p, err
}

// updatePull changes the title and body of a pull request this io opened.
func (a *api) updatePull(ctx context.Context, repo string, number int, title, body string) error {
	return a.send(ctx, http.MethodPatch, "/repos/"+repo+"/pulls/"+strconv.Itoa(number),
		map[string]string{"title": title, "body": body}, nil)
}

// readPull reads one pull request, its head and whether it is merged with it.
func (a *api) readPull(ctx context.Context, repo string, number int) (pull, error) {
	var p pull
	err := a.getOnce(ctx, "/repos/"+repo+"/pulls/"+strconv.Itoa(number), &p)
	return p, err
}

// merged is what GitHub answers about a merge it made.
type merged struct {
	SHA    string `json:"sha"`
	Merged bool   `json:"merged"`
}

// mergePull merges a pull request, but only while its head is sha: GitHub refuses the merge (409)
// when the head moved, which is what makes "merge what was decided" exact.
func (a *api) mergePull(ctx context.Context, repo string, number int, sha, method string) (merged, error) {
	var m merged
	body := map[string]string{"sha": sha}
	if method != "" {
		body["merge_method"] = method
	}
	err := a.send(ctx, http.MethodPut, "/repos/"+repo+"/pulls/"+strconv.Itoa(number)+"/merge", body, &m)
	return m, err
}

// createStatus puts a commit status on sha: `state` is pending, success, failure or error, and
// `context` names whose status it is, so a later one under the same context replaces it on the
// pull request's page. It takes the App's `statuses: write`.
func (a *api) createStatus(ctx context.Context, repo, sha, state, context, description, target string) error {
	body := map[string]string{"state": state, "context": context}
	if description != "" {
		body["description"] = description
	}
	if target != "" {
		body["target_url"] = target
	}
	return a.send(ctx, http.MethodPost, "/repos/"+repo+"/statuses/"+sha, body, nil)
}

// createIssue opens an issue.
func (a *api) createIssue(ctx context.Context, repo, title, body string) (issue, error) {
	var i issue
	err := a.send(ctx, http.MethodPost, "/repos/"+repo+"/issues",
		map[string]string{"title": title, "body": body}, &i)
	return i, err
}

// setIssueState opens or closes an issue: `open` reopens one closed.
func (a *api) setIssueState(ctx context.Context, repo string, number int, state string) (issue, error) {
	var i issue
	err := a.send(ctx, http.MethodPatch, "/repos/"+repo+"/issues/"+strconv.Itoa(number),
		map[string]string{"state": state}, &i)
	return i, err
}

// updateIssue changes the title and body of an issue this io opened.
func (a *api) updateIssue(ctx context.Context, repo string, number int, title, body string) error {
	return a.send(ctx, http.MethodPatch, "/repos/"+repo+"/issues/"+strconv.Itoa(number),
		map[string]string{"title": title, "body": body}, nil)
}

// review is what GitHub answers about a review it recorded.
type review struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	User    *user  `json:"user"`
}

// listReviews is every review of a pull request, oldest first.
func (a *api) listReviews(ctx context.Context, repo string, number int) ([]review, error) {
	var out []review
	for page := 1; ; page++ {
		var batch []review
		if err := a.getOnce(ctx, "/repos/"+repo+"/pulls/"+strconv.Itoa(number)+"/reviews?per_page=100&page="+strconv.Itoa(page), &batch); err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(batch) < 100 {
			return out, nil
		}
	}
}

// dismissReview dismisses one review, saying why.
func (a *api) dismissReview(ctx context.Context, repo string, number int, id int64, message string) error {
	return a.send(ctx, http.MethodPut, "/repos/"+repo+"/pulls/"+strconv.Itoa(number)+"/reviews/"+
		strconv.FormatInt(id, 10)+"/dismissals", map[string]string{"message": message, "event": "DISMISS"}, nil)
}

// createReview posts a review on a pull request: APPROVE, REQUEST_CHANGES or COMMENT, with a body,
// at the commit sha names, or at the pull request's head when it names none. A later review by the
// same reviewer supersedes an earlier one, which is how a review is changed.
func (a *api) createReview(ctx context.Context, repo string, number int, event, body, sha string) (review, error) {
	in := map[string]string{"event": event, "body": body}
	if sha != "" {
		in["commit_id"] = sha
	}
	var r review
	err := a.send(ctx, http.MethodPost, "/repos/"+repo+"/pulls/"+strconv.Itoa(number)+"/reviews", in, &r)
	return r, err
}

// self is the login the credential acts as: the app's bot (`<slug>[bot]`) for an installation,
// and the token's owner otherwise. Asked once, when first needed.
func (a *api) self(ctx context.Context) (string, error) {
	a.mu.Lock()
	login := a.login
	a.mu.Unlock()
	if login != "" {
		return login, nil
	}
	if inst, ok := a.cred.(*installation); ok {
		slug, err := inst.slug(ctx)
		if err != nil {
			return "", err
		}
		login = slug + "[bot]"
	} else {
		var u user
		if err := a.getOnce(ctx, "/user", &u); err != nil {
			return "", err
		}
		login = u.Login
	}
	a.mu.Lock()
	a.login = login
	a.mu.Unlock()
	return login, nil
}

// readIssue reads one issue or pull request through the issues endpoint, which answers both and
// says which with `pull_request`, and carries who closed it.
func (a *api) readIssue(ctx context.Context, repo string, number int) (issue, error) {
	var i issue
	err := a.getOnce(ctx, "/repos/"+repo+"/issues/"+strconv.Itoa(number), &i)
	return i, err
}

// closeIssue closes an issue or a pull request: a pull request is an issue to this endpoint, and
// closing one leaves it unmerged.
func (a *api) closeIssue(ctx context.Context, repo string, number int) (issue, error) {
	var i issue
	err := a.send(ctx, http.MethodPatch, "/repos/"+repo+"/issues/"+strconv.Itoa(number),
		map[string]string{"state": "closed"}, &i)
	return i, err
}

// updateComment changes the text of a comment this io made.
func (a *api) updateComment(ctx context.Context, repo string, id int64, text string) error {
	return a.send(ctx, http.MethodPatch, "/repos/"+repo+"/issues/comments/"+strconv.FormatInt(id, 10),
		map[string]string{"body": text}, nil)
}

// NotFound reports whether an error is GitHub answering that something is not there — which is
// a fact about the repository rather than a failure to read it.
func NotFound(err error) bool {
	var he *httpError
	if ok := asHTTPError(err, &he); !ok {
		return false
	}
	return he.status == http.StatusNotFound
}

func asHTTPError(err error, target **httpError) bool {
	for err != nil {
		if he, ok := err.(*httpError); ok {
			*target = he
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// ── what a reflection reads ──────────────────────────────────────────────────

// repository is `GET /repos/{owner}/{repo}`, as much of it as the entity carries.
type repository struct {
	FullName      string                 `json:"full_name"`
	Name          string                 `json:"name"`
	Owner         struct{ Login string } `json:"owner"`
	Private       bool                   `json:"private"`
	Archived      bool                   `json:"archived"`
	Fork          bool                   `json:"fork"`
	DefaultBranch string                 `json:"default_branch"`
	// HasDiscussions is what stops this connector paying for discussions on a repository that
	// does not have them. It rides the repository read, which is conditional and free on a warm
	// pass, where the GraphQL query answering the same question is neither.
	HasDiscussions bool     `json:"has_discussions"`
	Description    string   `json:"description"`
	Homepage       string   `json:"homepage"`
	Topics         []string `json:"topics"`
	CloneURL       string   `json:"clone_url"`
	SSHURL         string   `json:"ssh_url"`
	HTMLURL        string   `json:"html_url"`
}

// gitRef is `GET /repos/{o}/{r}/git/ref/heads/{branch}` — the one git-shaped fact this source
// carries, and the reason it is here rather than left to a git source is in the note: it costs
// one un-paginated call, where taking it from git costs a deployed mirror.
type gitRef struct {
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

// issue is an item from the issues listing. GitHub returns pull requests here too — see
// isPullRequest, and github.go's filter, which is the whole reason this field exists.
type issue struct {
	Number      int     `json:"number"`
	Title       string  `json:"title"`
	State       string  `json:"state"`
	StateReason string  `json:"state_reason"`
	Body        string  `json:"body"`
	HTMLURL     string  `json:"html_url"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	ClosedAt    string  `json:"closed_at"`
	ClosedBy    *user   `json:"closed_by"`
	Comments    int     `json:"comments"`
	User        *user   `json:"user"`
	Assignees   []user  `json:"assignees"`
	Labels      []label `json:"labels"`
	Milestone   *struct {
		Title string `json:"title"`
	} `json:"milestone"`
	PullRequest *struct{} `json:"pull_request"`
}

func (i issue) isPullRequest() bool { return i.PullRequest != nil }

// pull is an item from the pulls listing. It carries what the issues listing does not — the
// base and head of the proposal — which is why pull requests are read from their own endpoint
// rather than filtered out of the issues one and kept.
type pull struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Draft    bool   `json:"draft"`
	Merged   bool   `json:"merged"`
	MergedAt string `json:"merged_at"`
	// Comments is on the single-pull READ and not on the listing, which is the whole reason
	// pullPayload takes it as an argument rather than reading it here: two endpoints answering
	// with different field sets for one entity is how a payload comes to differ between a full
	// pass and a targeted one, and an entity that differs between two passes is rewritten by
	// whichever ran last, forever.
	Comments  int     `json:"comments"`
	Body      string  `json:"body"`
	HTMLURL   string  `json:"html_url"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	ClosedAt  string  `json:"closed_at"`
	User      *user   `json:"user"`
	Assignees []user  `json:"assignees"`
	Labels    []label `json:"labels"`
	Base      *branch `json:"base"`
	Head      *branch `json:"head"`
	// RequestedReviewers and RequestedTeams are whom a review is waiting on. Every endpoint a
	// pull is read from answers both — the listing, the single read and a commit's pulls — so,
	// unlike Comments, they cannot make one pass's payload differ from another's.
	RequestedReviewers []user `json:"requested_reviewers"`
	RequestedTeams     []team `json:"requested_teams"`
}

// team is a team a review was requested from. Slug is its identity at the forge, unqualified:
// `platform`, not `acme/platform` — the second half of an `@acme/platform` mention.
type team struct {
	Slug string `json:"slug"`
}

type branch struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type user struct {
	Login string `json:"login"`
	// ID is GitHub's own number for the account, which a rename does not change: what an identity
	// binding is keyed on (git-issue 9b4azhtjh12kszwyqsn0), where the login is only a name.
	ID int64 `json:"id"`
}

type label struct {
	Name string `json:"name"`
}

// workflowRun is one item from `GET /repos/{o}/{r}/actions/runs`.
type workflowRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HeadBranch string `json:"head_branch"`
	HeadSHA    string `json:"head_sha"`
	RunNumber  int    `json:"run_number"`
	RunAttempt int    `json:"run_attempt"`
	HTMLURL    string `json:"html_url"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	Actor      *user  `json:"actor"`
}

// ── the reads ────────────────────────────────────────────────────────────────

// readRepo reads the repository and the head of its default branch.
//
// Two calls rather than one: `GET /repos/{o}/{r}` names the default branch and does not say
// where it points, so the sha is a second, un-paginated request. It is worth the call — for a
// repository nobody watches at ref level it is the whole of the git-shaped fact anyone wants.
func (a *api) readRepo(ctx context.Context, repo string) (repository, string, error) {
	var r repository
	if err := a.get(ctx, "/repos/"+repo, &r); err != nil {
		return repository{}, "", err
	}
	sha := ""
	if r.DefaultBranch != "" {
		var ref gitRef
		err := a.get(ctx, "/repos/"+repo+"/git/ref/heads/"+url.PathEscape(r.DefaultBranch), &ref)
		switch {
		case err == nil:
			sha = ref.Object.SHA
		case NotFound(err):
			// An unborn default branch: the repository exists and the branch does not point
			// anywhere yet. A fact about the repository, not a failure to read it, so the
			// entity is written without a sha rather than the pass failing.
		default:
			return repository{}, "", err
		}
	}
	return r, sha, nil
}

// The two listing shapes, and the difference is the whole of what a bad answer costs.
//
// A LIVE read is covered as complete, so it must be exhaustive or fail: a partial list under a
// complete cover tombstones what it merely failed to read, which is the one mistake in this
// connector that destroys a slice. refuseOver is therefore an error and never a truncation.
//
// A SETTLED read is covered as a window, so stopping at n is what it means rather than a
// shortfall. It sorts by `updated` descending, which makes it a window and a catch-up at once:
// an issue that closed since the last pass is by definition among the most recently updated
// closed ones, so it arrives carrying its new state without anything having to remember when
// the last pass was. That is a cursor this source does not have to keep, and so cannot lose.

// listIssues reads the issues listing. Exactly one of refuseOver and window is meaningful; see
// above.
func (a *api) listIssues(ctx context.Context, repo, state, sort string, refuseOver, window int) ([]issue, error) {
	var out []issue
	for page := 1; ; page++ {
		var batch []issue
		q := listQuery(state, sort, page, window, len(out))
		if err := a.get(ctx, "/repos/"+repo+"/issues?"+q, &batch); err != nil {
			return nil, err
		}
		out = append(out, batch...)
		done, err := listDone(len(batch), len(out), refuseOver, window, repo, "open issues")
		if done || err != nil {
			return out, err
		}
	}
}

// listPulls is listIssues for the pulls endpoint, which carries the head and base an issue
// listing does not.
func (a *api) listPulls(ctx context.Context, repo, state, sort string, refuseOver, window int) ([]pull, error) {
	var out []pull
	for page := 1; ; page++ {
		var batch []pull
		q := listQuery(state, sort, page, window, len(out))
		if err := a.get(ctx, "/repos/"+repo+"/pulls?"+q, &batch); err != nil {
			return nil, err
		}
		out = append(out, batch...)
		done, err := listDone(len(batch), len(out), refuseOver, window, repo, "open pull requests")
		if done || err != nil {
			return out, err
		}
	}
}

// listQuery spells one page. A window asks for no more than it wants; an exhaustive read asks
// for GitHub's maximum every time, since the request count is what this connector is short of.
func listQuery(state, sort string, page, window, have int) string {
	per := pageSize
	if window > 0 && window-have < per {
		per = window - have
	}
	if per < 1 {
		per = 1
	}
	v := url.Values{
		"per_page": {fmt.Sprint(per)},
		"page":     {fmt.Sprint(page)},
	}
	if state != "" {
		v.Set("state", state)
	}
	if sort != "" {
		v.Set("sort", sort)
		v.Set("direction", "desc")
	}
	return v.Encode()
}

// listDone says whether a listing has finished, and refuses one bigger than the caller can
// cover.
func listDone(batch, total, refuseOver, window int, repo, what string) (bool, error) {
	if refuseOver > 0 && total > refuseOver {
		return true, fmt.Errorf("github: %s has more than %d %s, and this pass covers them as "+
			"complete — a partial list under a complete cover tombstones what it did not read. "+
			"Raise -max-open, or narrow what this source reflects", repo, refuseOver, what)
	}
	if window > 0 && total >= window {
		return true, nil // the window is full, which is what a window means
	}
	return batch < pageSize, nil
}

// listRuns reads the newest workflow runs, which is the whole of what this source keeps of
// Actions. GitHub returns them newest-created first and offers no ordering of its own, so the
// window is "the last n created" and nothing else.
func (a *api) listRuns(ctx context.Context, repo string, window int) ([]workflowRun, error) {
	var out []workflowRun
	for page := 1; ; page++ {
		var batch struct {
			Runs []workflowRun `json:"workflow_runs"`
		}
		q := listQuery("", "", page, window, len(out))
		if err := a.get(ctx, "/repos/"+repo+"/actions/runs?"+q, &batch); err != nil {
			return nil, err
		}
		out = append(out, batch.Runs...)
		done, err := listDone(len(batch.Runs), len(out), 0, window, repo, "workflow runs")
		if done || err != nil {
			return out, err
		}
	}
}

// ── discussions, which are GraphQL only ──────────────────────────────────────
//
// Repository Discussions has no REST API, so this is a second protocol in one connector. That
// is the cost, and it is why discussions are their own step: everything above is a conditional
// GET with an ETag, and none of it applies here. A GraphQL POST is not cacheable, so discussions
// are the one surface that spends a request on every pass whether anything changed or not.

// discussion is one node of the discussions connection.
type discussion struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	URL        string `json:"url"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
	ClosedAt   string `json:"closedAt"`
	Closed     bool   `json:"closed"`
	IsAnswered bool   `json:"isAnswered"`
	Category   *struct {
		Name string `json:"name"`
	} `json:"category"`
	Author *struct {
		Login string `json:"login"`
	} `json:"author"`
	Comments struct {
		TotalCount int `json:"totalCount"`
	} `json:"comments"`
}

const discussionsQuery = `query($owner: String!, $name: String!, $first: Int!, $after: String, $states: [DiscussionState!]) {
  repository(owner: $owner, name: $name) {
    hasDiscussionsEnabled
    discussions(first: $first, after: $after, states: $states,
                orderBy: {field: UPDATED_AT, direction: DESC}) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number title body url createdAt updatedAt closedAt closed isAnswered
        category { name }
        author { login }
        comments { totalCount }
      }
    }
  }
}`

// errDiscussionsOff is a repository with the Discussions feature turned off.
//
// A FACT ABOUT THE REPOSITORY, not a failure to read it, and the two must not be confused: the
// connection answers with an empty list either way, so a source that took the list at face value
// would tombstone every discussion a repository had the day somebody switched the feature off —
// or, worse, on a repository that never had it, would leave the cover claiming completeness for
// a set it never looked at. hasDiscussionsEnabled is what tells them apart.
var errDiscussionsOff = fmt.Errorf("github: discussions are not enabled on this repository")

// listDiscussions reads one state's discussions. refuseOver and window mean what they do for the
// REST listings: the live set must be exhaustive or fail, the settled set is a window.
func (a *api) listDiscussions(ctx context.Context, repo, state string, refuseOver, window int) ([]discussion, error) {
	owner, name, _ := strings.Cut(repo, "/")
	var out []discussion
	after := ""
	for {
		first := pageSize
		if window > 0 && window-len(out) < first {
			first = window - len(out)
		}
		if first < 1 {
			first = 1
		}
		vars := map[string]any{
			"owner": owner, "name": name, "first": first,
			"states": []string{state},
		}
		if after != "" {
			vars["after"] = after
		}
		var resp struct {
			Repository struct {
				HasDiscussionsEnabled bool `json:"hasDiscussionsEnabled"`
				Discussions           struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []discussion `json:"nodes"`
				} `json:"discussions"`
			} `json:"repository"`
		}
		if err := a.graphql(ctx, discussionsQuery, vars, &resp); err != nil {
			return nil, err
		}
		if !resp.Repository.HasDiscussionsEnabled {
			return nil, errDiscussionsOff
		}
		out = append(out, resp.Repository.Discussions.Nodes...)
		if refuseOver > 0 && len(out) > refuseOver {
			return nil, fmt.Errorf("github: %s has more than %d open discussions, and this pass "+
				"covers them as complete — a partial list under a complete cover tombstones what "+
				"it did not read. Raise -max-open, or narrow what this source reflects",
				repo, refuseOver)
		}
		if window > 0 && len(out) >= window {
			return out, nil
		}
		if !resp.Repository.Discussions.PageInfo.HasNextPage {
			return out, nil
		}
		after = resp.Repository.Discussions.PageInfo.EndCursor
	}
}

// graphql runs one query and decodes `data` into out.
//
// THE TRAP THIS EXISTS TO AVOID is GraphQL's own, and source/linear/api.go names it first: a
// request that failed answers HTTP 200 with `{"data": null, "errors": [...]}`. A client that
// checked the status and decoded the rest would read a revoked token as a repository with no
// discussions in it — and an empty answer under a cover is a legitimate instruction to tombstone
// everything beneath it. So `errors` is checked here, in one place, and nothing else can forget.
func (a *api) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.graphqlURL(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := a.authorize(req); err != nil {
		return err
	}
	resp, err := a.do(req, body)
	if err != nil {
		return fmt.Errorf("github: graphql: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("github: graphql: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return &httpError{status: resp.StatusCode, path: "/graphql", body: string(trimBody(raw)),
			limited: rateLimited(resp, raw)}
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("github: graphql: %w", err)
	}
	if len(envelope.Errors) > 0 {
		msgs := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("github: graphql: %s", strings.Join(msgs, "; "))
	}
	return json.Unmarshal(envelope.Data, out)
}

// graphqlURL derives the GraphQL endpoint from the REST base, which is where the two differ on
// GitHub Enterprise Server: `/api/v3` for REST and `/api/graphql` beside it, against a single
// `/graphql` on github.com.
func (a *api) graphqlURL() string {
	if strings.HasSuffix(a.base, "/api/v3") {
		return strings.TrimSuffix(a.base, "/v3") + "/graphql"
	}
	return a.base + "/graphql"
}

// pullsForSHA lists the open pull requests whose head is this commit — `GET
// /repos/{o}/{r}/commits/{sha}/pulls`, which is one request and answers the only question a
// finished run raises: whose proposal was that.
func (a *api) pullsForSHA(ctx context.Context, repo, sha string) ([]pull, error) {
	var out []pull
	if err := a.get(ctx, "/repos/"+repo+"/commits/"+sha+"/pulls", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ── comments ─────────────────────────────────────────────────────────────────
//
// REPOSITORY-WIDE, which is what makes them affordable. GitHub lists every issue comment in a
// repository at one endpoint and every review comment at another, both sorted and paginated, so
// a window over the recent ones costs two conditional requests a pass rather than one per item.
// Asking per issue would be a request each, which is the budget this connector does not have.

// comment is one comment, from either listing. The two carry the same fields for what a charter
// reads; where they differ is what they hang off, which is the `kind` the payload records.
type comment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	HTMLURL   string `json:"html_url"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	User      *user  `json:"user"`
	// IssueURL is `.../issues/{n}` on an issue comment; PullRequestURL is `.../pulls/{n}` on a
	// review comment. One of the two is set, and it is where the item number comes from — the
	// listings carry no number of their own.
	IssueURL       string `json:"issue_url"`
	PullRequestURL string `json:"pull_request_url"`
	// Where a review comment is, and what it answers: the file, the line it ends on (null on an
	// outdated comment, whose line is no longer in the diff) and the one a multi-line comment
	// starts on, the diff's side, the commit it was written against, the thread's first comment
	// when it is a reply, and the hunk it was written on, ending at its line. A conversation
	// comment has none of them. The hunk is the one the comment was written on, so the lines it
	// was written on (original_line, original_start_line) are what measure it, and they stay set
	// when an outdated comment's line goes null. A multi-line comment may start on the other side
	// from the one it ends on (start_side); GitHub sends no original of it.
	Path              string `json:"path"`
	Line              *int   `json:"line"`
	StartLine         *int   `json:"start_line"`
	OriginalLine      *int   `json:"original_line"`
	OriginalStartLine *int   `json:"original_start_line"`
	Side              string `json:"side"`
	StartSide         string `json:"start_side"`
	CommitID          string `json:"commit_id"`
	InReplyToID       int64  `json:"in_reply_to_id"`
	DiffHunk          string `json:"diff_hunk"`
}

// number reads the item this comment is on out of whichever url the listing carried.
func (c comment) number() int {
	for _, u := range []string{c.IssueURL, c.PullRequestURL} {
		if u == "" {
			continue
		}
		if i := strings.LastIndex(u, "/"); i >= 0 {
			if n, err := strconv.Atoi(u[i+1:]); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// listComments reads a window of a repository's comments, newest-updated first. `what` is
// "issues" for conversation comments — which are the ones on issues AND on pull requests, since
// a PR's conversation is an issue's — or "pulls" for review comments on a diff.
func (a *api) listComments(ctx context.Context, repo, what string, window int) ([]comment, error) {
	var out []comment
	for page := 1; ; page++ {
		var batch []comment
		q := listQuery("", "updated", page, window, len(out))
		if err := a.get(ctx, "/repos/"+repo+"/"+what+"/comments?"+q, &batch); err != nil {
			return nil, err
		}
		out = append(out, batch...)
		done, err := listDone(len(batch), len(out), 0, window, repo, "comments")
		if done || err != nil {
			return out, err
		}
	}
}
