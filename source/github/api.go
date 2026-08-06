package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	token string
	base  string
	cl    *http.Client

	mu     sync.Mutex
	cached map[string]cachedResponse
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
}

func newAPI(token, base string, cl *http.Client) *api {
	if base == "" {
		base = APIBase
	}
	if cl == nil {
		cl = &http.Client{Timeout: 60 * time.Second}
	}
	return &api{
		token:  token,
		base:   strings.TrimRight(base, "/"),
		cl:     cl,
		cached: map[string]cachedResponse{},
	}
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
	target := a.base + path
	a.mu.Lock()
	prev, hadPrev := a.cached[target]
	a.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	if hadPrev && prev.etag != "" {
		req.Header.Set("If-None-Match", prev.etag)
	}

	resp, err := a.cl.Do(req)
	if err != nil {
		return fmt.Errorf("github: GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified && hadPrev {
		return json.Unmarshal(prev.body, out)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("github: GET %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		// Said with the status and the body, because the two failures an operator actually
		// hits are told apart by them: a 401 is the token, a 403 with a rate-limit body is the
		// budget, and "GET failed" tells them neither.
		return &httpError{status: resp.StatusCode, path: path, body: string(trimBody(body))}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("github: GET %s: %w", path, err)
	}
	if etag := resp.Header.Get("ETag"); etag != "" {
		a.mu.Lock()
		a.cached[target] = cachedResponse{etag: etag, body: body}
		a.mu.Unlock()
	}
	return nil
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
	path   string
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("github: GET %s: %s: %s", e.path, http.StatusText(e.status), e.body)
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
	Description   string                 `json:"description"`
	Homepage      string                 `json:"homepage"`
	Topics        []string               `json:"topics"`
	CloneURL      string                 `json:"clone_url"`
	SSHURL        string                 `json:"ssh_url"`
	HTMLURL       string                 `json:"html_url"`
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
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	State     string  `json:"state"`
	Draft     bool    `json:"draft"`
	Merged    bool    `json:"merged"`
	MergedAt  string  `json:"merged_at"`
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
}

type branch struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type user struct {
	Login string `json:"login"`
}

type label struct {
	Name string `json:"name"`
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
		"state":    {state},
		"per_page": {fmt.Sprint(per)},
		"page":     {fmt.Sprint(page)},
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
