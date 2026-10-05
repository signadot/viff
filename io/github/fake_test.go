package github_test

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub is as much of the REST API as this source reads, answering from fixtures.
//
// It implements the four behaviours the source depends on and would be wrong without, rather
// than the whole surface: the state filter, `sort=updated&direction=desc` ordering, paging,
// and — the one that matters most — the issues listing returning pull requests too.
//
// It also serves ETags and answers 304, because conditional requests are in this connector from
// the first commit and a fake that ignored them would leave that untested.
type fakeGitHub struct {
	mu    sync.Mutex
	repos map[string]*fakeRepo

	requests     int      // every request that reached a handler
	notModified  int      // those answered 304
	nextComment  int64    // the id of the last comment the out half made, past 9000
	edits        int      // comments and pull requests the out half changed
	opened       int      // pull requests the out half opened
	issuesOpened int      // issues the out half opened
	reopens      int      // issues the out half reopened
	dismissals   int      // reviews the out half dismissed
	merges       int      // pull requests the out half merged
	closes       int      // issues and pull requests the out half closed
	reviews      []string // the events of the reviews the out half posted, in order
	// app is an app's public key, set when the source authenticates as an app: the fake then
	// mints installation tokens for a JWT that key verifies, and accepts those tokens too.
	app    *rsa.PublicKey
	minted []string
	// failWith makes every request answer with this status, which is how a test asks what a
	// source does when GitHub is refusing rather than when it is empty.
	failWith int
}

type fakeRepo struct {
	defaultBranch string
	sha           string
	private       bool
	cloneURL      string
	items         []*fakeItem
	runs          []*fakeRun

	comments    []*fakeComment
	discussions []*fakeDiscussion
	// missingHeads are branches GitHub does not have: a pull request from one is refused with
	// the 422 GitHub gives, until the branch arrives.
	missingHeads map[string]bool
	// statuses are the commit statuses posted, in order; statusesForbidden refuses a new one as
	// GitHub refuses an App not allowed `statuses: write`, and statusAsks counts every request.
	// trees are the tree objects the fake answers /git/trees/{sha} with, by sha (a commit's sha
	// names its root tree here); treeAsks counts those requests.
	trees    map[string][]fakeEntry
	treeAsks int
	// mergeBases are the merge bases /compare/{base}...{head} answers, by head sha;
	// compareAsks counts those requests.
	mergeBases        map[string]string
	compareAsks       int
	statuses          []fakeStatus
	statusesForbidden bool
	statusAsks        int
	// discussionsOff is the feature turned off, which answers with an empty list exactly as a
	// repository with no discussions does — the confusion hasDiscussionsEnabled exists to
	// resolve. graphqlError is the endpoint refusing, which is HTTP 200 with an errors array.
	discussionsOff bool
	graphqlError   string
}

// fakeEntry is one entry of a tree object.
type fakeEntry struct{ path, typ, sha string }

// fakeStatus is one commit status, as posted.
type fakeStatus struct {
	sha, state, context, description, target string
}

// fakeComment is one comment. `review` says which listing it comes from — GitHub keeps the
// conversation comments (which an issue has, and which a pull request's discussion is) at one
// repository-wide endpoint and the review comments on a diff at another.
type fakeComment struct {
	id      int64
	on      int
	body    string
	review  bool
	updated string
	// login and userID are the author, "ana" and 101 unless said otherwise; the out half's own
	// comments are fakeSelf's.
	login  string
	userID int64
}

// by makes the comment somebody's.
func (c *fakeComment) by(login string, id int64) *fakeComment {
	c.login, c.userID = login, id
	return c
}

type fakeDiscussion struct {
	number   int
	title    string
	closed   bool
	answered bool
	category string
	updated  string
}

// fakeRun is one workflow run. GitHub returns them newest-created first and offers no ordering
// of its own, which is what makes the source's window "the last n created" — so this fake
// answers in id-descending order and nothing in the source may depend on more.
type fakeRun struct {
	id         int64
	name       string
	status     string // queued | in_progress | completed
	conclusion string // "" while it is still going
	headSHA    string
}

// fakeItem is an issue or a pull request. One type, because on GitHub they share a number space
// and the issues endpoint returns both — which is the thing the source has to get right.
type fakeItem struct {
	number    int
	title     string
	state     string // open | closed
	updatedAt string // RFC3339, and what `sort=updated` orders by
	pull      bool
	comments  int
	head      string // pulls only
	base      string // pulls only, and only one the out half opened; "" renders as main
	// selfOpened is a pull request the out half opened, which GitHub will not let it review.
	selfOpened bool
	// closedBy is who closed it, as GitHub answers on the item's own read.
	closedBy string
	body     string
	headSHA  string
	labels   []string
	// reviews are the reviews posted on a pull request, oldest first.
	reviews []*fakeReview
}

// fakeReview is one review on a pull request.
type fakeReview struct {
	id           int64
	state, login string
}

func newFake() *fakeGitHub {
	return &fakeGitHub{repos: map[string]*fakeRepo{}}
}

func (f *fakeGitHub) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakeGitHub) repo(name string) *fakeRepo {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := &fakeRepo{defaultBranch: "main", sha: "aaaa1111", cloneURL: "https://github.com/" + name + ".git"}
	f.repos[name] = r
	return r
}

// issue adds one, newest-updated last unless said otherwise.
func (r *fakeRepo) issue(number int, title, state, updatedAt string) *fakeItem {
	i := &fakeItem{number: number, title: title, state: state, updatedAt: updatedAt}
	r.items = append(r.items, i)
	return i
}

func (r *fakeRepo) pr(number int, title, state, updatedAt, headSHA string) *fakeItem {
	i := &fakeItem{number: number, title: title, state: state, updatedAt: updatedAt,
		pull: true, head: "feature-" + strconv.Itoa(number), headSHA: headSHA}
	r.items = append(r.items, i)
	return i
}

func (r *fakeRepo) comment(id int64, on int, body string, review bool, updated string) *fakeComment {
	c := &fakeComment{id: id, on: on, body: body, review: review, updated: updated}
	r.comments = append(r.comments, c)
	return c
}

func (r *fakeRepo) run(id int64, name, status, conclusion, headSHA string) *fakeRun {
	x := &fakeRun{id: id, name: name, status: status, conclusion: conclusion, headSHA: headSHA}
	r.runs = append(r.runs, x)
	return x
}

func (r *fakeRepo) discussion(number int, title string, closed bool, updated string) *fakeDiscussion {
	d := &fakeDiscussion{number: number, title: title, closed: closed, updated: updated, category: "General"}
	r.discussions = append(r.discussions, d)
	return d
}

// fakeSelf is who the fake says the credential is: the token's owner on GET /user, and the app's
// bot for an installation. What the out half opens is theirs.
const fakeSelf = "verse-bot"

// author is who opened an item: the io, or a person.
func (i *fakeItem) author() string {
	if i.selfOpened {
		return fakeSelf
	}
	return "ana"
}

func (r *fakeRepo) find(number int) *fakeItem {
	for _, i := range r.items {
		if i.number == number {
			return i
		}
	}
	return nil
}

func (r *fakeRepo) remove(number int) {
	var out []*fakeItem
	for _, i := range r.items {
		if i.number != number {
			out = append(out, i)
		}
	}
	r.items = out
}

func (f *fakeGitHub) counts() (requests, notModified int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, f.notModified
}

func (f *fakeGitHub) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	f.mu.Unlock()

	f.mu.Lock()
	fail := f.failWith
	f.mu.Unlock()
	if fail != 0 {
		http.Error(w, `{"message":"API rate limit exceeded"}`, fail)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/app/installations/"+fakeInstallation+"/access_tokens" {
		f.mint(w, r)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/app" {
		writeJSON(w, map[string]any{"slug": strings.TrimSuffix(fakeSelf, "[bot]")})
		return
	}
	if !f.authorized(r.Header.Get("Authorization")) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/user" {
		writeJSON(w, map[string]any{"login": fakeSelf})
		return
	}
	if r.URL.Path == "/graphql" {
		f.graphql(w, r)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// /repos/{org}/{name}/...
	if len(parts) < 3 || parts[0] != "repos" {
		http.NotFound(w, r)
		return
	}
	name := parts[1] + "/" + parts[2]
	f.mu.Lock()
	repo, ok := f.repos[name]
	f.mu.Unlock()
	if !ok {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	rest := parts[3:]

	// THE WRITES THE OUT HALF MAKES: a comment on an issue or pull request, and a change to one.
	if r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodPut {
		f.write(w, r, repo, rest)
		return
	}

	switch {
	case len(rest) == 0:
		f.reply(w, r, map[string]any{
			"full_name": name, "name": parts[2], "owner": map[string]any{"login": parts[1]},
			"private": repo.private, "archived": false, "fork": false,
			"default_branch": repo.defaultBranch, "clone_url": repo.cloneURL,
			"has_discussions": !repo.discussionsOff && len(repo.discussions) > 0,
			"ssh_url":         "git@github.com:" + name + ".git",
			"html_url":        "https://github.com/" + name,
		})
	case len(rest) == 3 && rest[0] == "pulls" && rest[2] == "reviews":
		n, _ := strconv.Atoi(rest[1])
		i := repo.find(n)
		if i == nil {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		out := []map[string]any{}
		f.mu.Lock()
		for _, rv := range i.reviews {
			out = append(out, map[string]any{"id": rv.id, "state": rv.state, "user": map[string]any{"login": rv.login}})
		}
		f.mu.Unlock()
		f.reply(w, r, out)
	case len(rest) == 2 && rest[0] == "compare":
		_, head, _ := strings.Cut(rest[1], "...")
		f.mu.Lock()
		repo.compareAsks++
		mb, ok := repo.mergeBases[head]
		f.mu.Unlock()
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		f.reply(w, r, map[string]any{"merge_base_commit": map[string]any{"sha": mb}})
	case len(rest) == 3 && rest[0] == "git" && rest[1] == "trees":
		f.mu.Lock()
		repo.treeAsks++
		entries, ok := repo.trees[rest[2]]
		f.mu.Unlock()
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		out := []map[string]any{}
		for _, e := range entries {
			out = append(out, map[string]any{"path": e.path, "type": e.typ, "sha": e.sha})
		}
		f.reply(w, r, map[string]any{"sha": rest[2], "tree": out})
	case len(rest) >= 3 && rest[0] == "git" && rest[1] == "ref":
		if repo.sha == "" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		f.reply(w, r, map[string]any{"object": map[string]any{"sha": repo.sha}})
	case len(rest) == 3 && rest[1] == "comments" && (rest[0] == "issues" || rest[0] == "pulls"):
		id, _ := strconv.ParseInt(rest[2], 10, 64)
		for _, c := range repo.comments {
			if c.id == id && c.review == (rest[0] == "pulls") {
				f.reply(w, r, f.renderComment(c))
				return
			}
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	case len(rest) == 2 && rest[1] == "comments" && (rest[0] == "issues" || rest[0] == "pulls"):
		f.reply(w, r, f.listComments(repo, rest[0] == "pulls", r))
	case len(rest) == 3 && rest[0] == "actions" && rest[1] == "runs":
		id, _ := strconv.ParseInt(rest[2], 10, 64)
		var found *fakeRun
		for _, x := range repo.runs {
			if x.id == id {
				found = x
			}
		}
		if found == nil {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		f.reply(w, r, f.renderRun(found))
	case len(rest) == 3 && rest[0] == "commits" && rest[2] == "pulls":
		// The pull requests whose head is this commit, which is the one question a finished
		// run raises: whose proposal was that.
		out := []map[string]any{}
		for _, i := range repo.items {
			if i.pull && i.headSHA == rest[1] {
				out = append(out, i.render(true))
			}
		}
		f.reply(w, r, out)
	case len(rest) == 2 && rest[0] == "actions" && rest[1] == "runs":
		f.reply(w, r, map[string]any{
			"total_count":   len(repo.runs),
			"workflow_runs": f.listRuns(repo, r),
		})
	case len(rest) == 1 && (rest[0] == "issues" || rest[0] == "pulls"):
		f.reply(w, r, f.list(repo, rest[0], r))
	case len(rest) == 2 && (rest[0] == "issues" || rest[0] == "pulls"):
		n, _ := strconv.Atoi(rest[1])
		item := repo.find(n)
		if item == nil || (rest[0] == "pulls" && !item.pull) {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		f.reply(w, r, item.renderAs(rest[0] == "pulls", true))
	default:
		http.NotFound(w, r)
	}
}

// graphql answers the discussions query, including the two ways it can decline to.
func (f *fakeGitHub) graphql(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Variables struct {
			Owner  string   `json:"owner"`
			Name   string   `json:"name"`
			First  int      `json:"first"`
			After  string   `json:"after"`
			States []string `json:"states"`
		} `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := req.Variables.Owner + "/" + req.Variables.Name
	f.mu.Lock()
	repo, ok := f.repos[name]
	f.mu.Unlock()
	if !ok {
		writeJSON(w, map[string]any{"data": nil, "errors": []map[string]any{
			{"message": "Could not resolve to a Repository with the name '" + name + "'."}}})
		return
	}
	if repo.graphqlError != "" {
		// HTTP 200 with an errors array, which is GraphQL's own shape for a refusal and the
		// one a client must not read as an answer.
		writeJSON(w, map[string]any{"data": nil, "errors": []map[string]any{
			{"message": repo.graphqlError}}})
		return
	}

	want := "OPEN"
	if len(req.Variables.States) > 0 {
		want = req.Variables.States[0]
	}
	// The state filter, the UPDATED_AT-descending order and `first` are what the source
	// depends on, so the fake implements them rather than answering with everything: a fake
	// that ignored `first` would make a window look like it worked when it did not.
	var picked []*fakeDiscussion
	if !repo.discussionsOff {
		for _, d := range repo.discussions {
			if (want == "CLOSED") != d.closed {
				continue
			}
			picked = append(picked, d)
		}
	}
	sort.SliceStable(picked, func(a, b int) bool { return picked[a].updated > picked[b].updated })
	if n := req.Variables.First; n > 0 && n < len(picked) {
		picked = picked[:n]
	}
	nodes := []map[string]any{}
	for _, d := range picked {
		nodes = append(nodes, map[string]any{
			"number": d.number, "title": d.title, "body": "",
			"url":       "https://github.com/" + name + "/discussions/" + strconv.Itoa(d.number),
			"createdAt": "2026-01-01T00:00:00Z", "updatedAt": d.updated,
			"closed": d.closed, "isAnswered": d.answered,
			"category": map[string]any{"name": d.category},
			"author":   map[string]any{"login": "ana"},
			"comments": map[string]any{"totalCount": 0},
		})
	}
	writeJSON(w, map[string]any{"data": map[string]any{
		"repository": map[string]any{
			"hasDiscussionsEnabled": !repo.discussionsOff,
			"discussions": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
				"nodes":    nodes,
			},
		},
	}})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// listRuns answers newest-created first, which for this fake is id-descending, and pages.
func (f *fakeGitHub) listRuns(repo *fakeRepo, r *http.Request) []map[string]any {
	runs := append([]*fakeRun(nil), repo.runs...)
	sort.SliceStable(runs, func(a, b int) bool { return runs[a].id > runs[b].id })
	from, to := pageBounds(r, len(runs))
	out := make([]map[string]any, 0, to-from)
	for _, x := range runs[from:to] {
		out = append(out, f.renderRun(x))
	}
	return out
}

// listComments answers one of the two repository-wide comment listings, newest-updated first.
func (f *fakeGitHub) listComments(repo *fakeRepo, review bool, r *http.Request) []map[string]any {
	var picked []*fakeComment
	for _, c := range repo.comments {
		if c.review == review {
			picked = append(picked, c)
		}
	}
	sort.SliceStable(picked, func(a, b int) bool { return picked[a].updated > picked[b].updated })
	from, to := pageBounds(r, len(picked))
	out := make([]map[string]any, 0, to-from)
	for _, c := range picked[from:to] {
		out = append(out, f.renderComment(c))
	}
	return out
}

// renderComment is shared by the listing and the single-comment read, so a targeted pass and a
// full one cannot disagree about a comment's shape — the asymmetry that bit the pull request.
func (f *fakeGitHub) renderComment(c *fakeComment) map[string]any {
	login, userID := c.login, c.userID
	if login == "" {
		login, userID = "ana", 101
	}
	m := map[string]any{
		"id": c.id, "body": c.body, "user": map[string]any{"login": login, "id": userID},
		"created_at": "2026-01-01T00:00:00Z", "updated_at": c.updated,
		"html_url": "https://github.com/x/y/comment/" + strconv.FormatInt(c.id, 10),
	}
	// The number is only in the url the listing carries, which is the whole reason the source
	// reads it out of one rather than expecting a field.
	if c.review {
		m["pull_request_url"] = "https://api.github.com/repos/x/y/pulls/" + strconv.Itoa(c.on)
	} else {
		m["issue_url"] = "https://api.github.com/repos/x/y/issues/" + strconv.Itoa(c.on)
	}
	return m
}

// renderRun is shared by the listing and the single-run read, so the two cannot disagree about
// the shape of a run — which is exactly how a targeted pass comes to differ from a full one.
func (f *fakeGitHub) renderRun(x *fakeRun) map[string]any {
	m := map[string]any{
		"id": x.id, "name": x.name, "event": "push", "status": x.status,
		"head_branch": "main", "head_sha": x.headSHA,
		"run_number": int(x.id), "run_attempt": 1,
		"html_url":   "https://github.com/x/y/actions/runs/" + strconv.FormatInt(x.id, 10),
		"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:01:00Z",
		"actor": map[string]any{"login": "ana"},
	}
	if x.conclusion != "" {
		m["conclusion"] = x.conclusion
	}
	return m
}

// pageBounds reads per_page and page the way the API does.
func pageBounds(r *http.Request, n int) (int, int) {
	q := r.URL.Query()
	per, _ := strconv.Atoi(q.Get("per_page"))
	if per <= 0 {
		per = 30
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page <= 0 {
		page = 1
	}
	from := (page - 1) * per
	if from > n {
		from = n
	}
	to := from + per
	if to > n {
		to = n
	}
	return from, to
}

// list applies the state filter, the sort, and the paging — the three the source depends on.
func (f *fakeGitHub) list(repo *fakeRepo, what string, r *http.Request) []map[string]any {
	q := r.URL.Query()
	state := q.Get("state")
	_, head, _ := strings.Cut(q.Get("head"), ":") // `head=<org>:<branch>`, as GitHub takes it

	var picked []*fakeItem
	for _, i := range repo.items {
		if what == "pulls" && !i.pull {
			continue
		}
		if head != "" && (!i.pull || i.head != head) {
			continue
		}
		// The issues endpoint returns pull requests too. That is not an oddity of this fake;
		// it is GitHub's behaviour, and filtering it is the source's job.
		if state != "" && state != "all" && i.state != state {
			continue
		}
		picked = append(picked, i)
	}
	if q.Get("sort") == "updated" {
		desc := q.Get("direction") == "desc"
		sort.SliceStable(picked, func(a, b int) bool {
			if desc {
				return picked[a].updatedAt > picked[b].updatedAt
			}
			return picked[a].updatedAt < picked[b].updatedAt
		})
	}

	from, to := pageBounds(r, len(picked))
	out := make([]map[string]any, 0, to-from)
	for _, i := range picked[from:to] {
		out = append(out, i.render(what == "pulls"))
	}
	return out
}

func (i *fakeItem) render(asPull bool) map[string]any { return i.renderAs(asPull, false) }

func (i *fakeItem) renderAs(asPull, detail bool) map[string]any {
	labels := make([]map[string]any, 0, len(i.labels))
	for _, l := range i.labels {
		labels = append(labels, map[string]any{"name": l})
	}
	m := map[string]any{
		"number": i.number, "title": i.title, "state": i.state,
		"comments":   i.comments,
		"updated_at": i.updatedAt, "created_at": "2026-01-01T00:00:00Z",
		"html_url": "https://github.com/x/y/" + strconv.Itoa(i.number),
		"user":     map[string]any{"login": i.author()},
		"labels":   labels,
	}
	if i.closedBy != "" {
		// When it closed is in the listings and the single read alike; who closed it is on the
		// issue's own read only, which is the one the out half's close reads.
		m["closed_at"] = i.updatedAt
		if detail {
			m["closed_by"] = map[string]any{"login": i.closedBy}
		}
	}
	if asPull {
		if !detail {
			// GitHub's pulls LISTING does not carry a comment count; only the single-pull read
			// does. Reproduced because a fake that answered the same either way would hide the
			// disagreement this asymmetry causes.
			delete(m, "comments")
		}
		m["draft"] = false
		m["merged"] = i.state == "closed"
		base := i.base
		if base == "" {
			base = "main"
		}
		m["base"] = map[string]any{"ref": base, "sha": "bbbb2222"}
		m["body"] = i.body
		m["head"] = map[string]any{"ref": i.head, "sha": i.headSHA}
		return m
	}
	if i.pull {
		// What makes an item in the issues listing a pull request, and the only thing that
		// does: a `pull_request` key. Without it the source cannot tell them apart.
		m["pull_request"] = map[string]any{"url": "https://api.github.com/pulls"}
	}
	return m
}

// reply serves JSON with an ETag, and answers 304 when the client already has it.
func (f *fakeGitHub) reply(w http.ResponseWriter, r *http.Request, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	if r.Header.Get("If-None-Match") == etag {
		f.mu.Lock()
		f.notModified++
		f.mu.Unlock()
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(body); err != nil {
		panic(fmt.Sprintf("fake github: %v", err))
	}
}

// write answers the two writes the out half makes, recording what it was asked so a test can see
// what landed on GitHub.
func (f *fakeGitHub) write(w http.ResponseWriter, r *http.Request, repo *fakeRepo, rest []string) {
	var body struct {
		Body        string `json:"body"`
		Title       string `json:"title"`
		Head        string `json:"head"`
		Base        string `json:"base"`
		SHA         string `json:"sha"`
		Event       string `json:"event"`
		State       string `json:"state"`
		Context     string `json:"context"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"message":"Problems parsing JSON"}`, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && len(rest) == 3 && rest[0] == "issues" && rest[2] == "comments":
		on, err := strconv.Atoi(rest[1])
		if err != nil || repo.find(on) == nil {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		f.nextComment++
		id := 9000 + f.nextComment
		repo.comments = append(repo.comments, &fakeComment{id: id, on: on, body: body.Body, login: fakeSelf, userID: 1})
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": id, "html_url": "https://github.com/c/" + strconv.FormatInt(id, 10)})
	case r.Method == http.MethodPatch && len(rest) == 3 && rest[0] == "issues" && rest[1] == "comments":
		id, _ := strconv.ParseInt(rest[2], 10, 64)
		for _, c := range repo.comments {
			if c.id == id {
				c.body = body.Body
				f.edits++
				writeJSON(w, map[string]any{"id": id})
				return
			}
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	case r.Method == http.MethodPost && len(rest) == 3 && rest[0] == "pulls" && rest[2] == "reviews":
		n, _ := strconv.Atoi(rest[1])
		i := repo.find(n)
		switch {
		case i == nil || !i.pull:
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case i.selfOpened:
			http.Error(w, `{"message":"Unprocessable Entity","errors":["Can not approve your own pull request"]}`,
				http.StatusUnprocessableEntity)
		default:
			f.nextComment++
			f.reviews = append(f.reviews, body.Event)
			state := map[string]string{"APPROVE": "APPROVED", "REQUEST_CHANGES": "CHANGES_REQUESTED"}[body.Event]
			i.reviews = append(i.reviews, &fakeReview{id: 7000 + f.nextComment, state: state, login: fakeSelf})
			writeJSON(w, map[string]any{"id": 7000 + f.nextComment, "state": body.Event,
				"html_url": "https://github.com/r/" + strconv.FormatInt(7000+f.nextComment, 10)})
		}
	case r.Method == http.MethodPut && len(rest) == 5 && rest[0] == "pulls" && rest[2] == "reviews" && rest[4] == "dismissals":
		n, _ := strconv.Atoi(rest[1])
		id, _ := strconv.ParseInt(rest[3], 10, 64)
		i := repo.find(n)
		if i == nil {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		for _, rv := range i.reviews {
			if rv.id == id {
				rv.state = "DISMISSED"
				f.dismissals++
				writeJSON(w, map[string]any{"id": id, "state": "DISMISSED"})
				return
			}
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	case r.Method == http.MethodPut && len(rest) == 3 && rest[0] == "pulls" && rest[2] == "merge":
		n, _ := strconv.Atoi(rest[1])
		i := repo.find(n)
		switch {
		case i == nil || !i.pull:
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case i.state != "open":
			http.Error(w, `{"message":"Pull Request is not mergeable"}`, http.StatusMethodNotAllowed)
		case body.SHA != "" && body.SHA != i.headSHA:
			http.Error(w, `{"message":"Head branch was modified. Review and try the merge again."}`, http.StatusConflict)
		default:
			i.state = "closed" // which the fake renders as merged
			f.merges++
			writeJSON(w, map[string]any{"sha": "dddd4444", "merged": true})
		}
	case r.Method == http.MethodPost && len(rest) == 2 && rest[0] == "statuses":
		repo.statusAsks++
		if repo.statusesForbidden {
			http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
			return
		}
		repo.statuses = append(repo.statuses, fakeStatus{sha: rest[1], state: body.State, context: body.Context,
			description: body.Description, target: body.TargetURL})
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": len(repo.statuses), "state": body.State, "context": body.Context})
	case r.Method == http.MethodPost && len(rest) == 1 && rest[0] == "issues":
		number := 1
		for _, i := range repo.items {
			if i.number >= number {
				number = i.number + 1
			}
		}
		i := &fakeItem{number: number, title: body.Title, body: body.Body, state: "open",
			updatedAt: "2026-10-01T00:00:00Z", selfOpened: true}
		repo.items = append(repo.items, i)
		f.issuesOpened++
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, i.renderAs(false, true))
	case r.Method == http.MethodPatch && len(rest) == 2 && rest[0] == "issues" && body.State == "" && body.Title != "":
		n, _ := strconv.Atoi(rest[1])
		i := repo.find(n)
		if i == nil || i.pull {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		i.title, i.body = body.Title, body.Body
		f.edits++
		writeJSON(w, i.renderAs(false, true))
	case r.Method == http.MethodPatch && len(rest) == 2 && rest[0] == "issues" && body.State == "open":
		n, _ := strconv.Atoi(rest[1])
		i := repo.find(n)
		if i == nil {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		i.state, i.closedBy = "open", ""
		f.reopens++
		writeJSON(w, i.renderAs(false, true))
	case r.Method == http.MethodPatch && len(rest) == 2 && rest[0] == "issues" && body.State == "closed":
		n, _ := strconv.Atoi(rest[1])
		i := repo.find(n)
		if i == nil {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		i.state, i.closedBy, i.updatedAt = "closed", "verse-bot", "2026-10-01T12:00:00Z"
		f.closes++
		writeJSON(w, i.renderAs(false, true))
	case r.Method == http.MethodPost && len(rest) == 1 && rest[0] == "pulls":
		if repo.missingHeads[body.Head] {
			// GitHub's answer to a head branch it does not have.
			w.WriteHeader(http.StatusUnprocessableEntity)
			writeJSON(w, map[string]any{"message": "Validation Failed", "errors": []any{map[string]any{"resource": "PullRequest", "field": "head", "code": "invalid"}}})
			return
		}
		number := 1
		for _, i := range repo.items {
			if i.number >= number {
				number = i.number + 1
			}
		}
		i := &fakeItem{number: number, title: body.Title, body: body.Body, state: "open", pull: true,
			head: body.Head, base: body.Base, headSHA: "cccc3333", updatedAt: "2026-10-01T00:00:00Z",
			selfOpened: true}
		repo.items = append(repo.items, i)
		f.opened++
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, i.render(true))
	case r.Method == http.MethodPatch && len(rest) == 2 && rest[0] == "pulls":
		n, _ := strconv.Atoi(rest[1])
		i := repo.find(n)
		if i == nil || !i.pull {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		i.title, i.body = body.Title, body.Body
		f.edits++
		writeJSON(w, i.render(true))
	default:
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}
}

// fakeInstallation is the installation id the fake mints for.
const fakeInstallation = "167"

// mint answers an installation token for an app's JWT, verifying it as GitHub does: signed by the
// app's key, issued by the app, and not expired.
func (f *fakeGitHub) mint(w http.ResponseWriter, r *http.Request) {
	jwt, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(jwt, ".")
	f.mu.Lock()
	key := f.app
	f.mu.Unlock()
	if !ok || len(parts) != 3 || key == nil {
		http.Error(w, `{"message":"A JSON web token could not be decoded"}`, http.StatusUnauthorized)
		return
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err != nil || rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		http.Error(w, `{"message":"A JSON web token could not be decoded"}`, http.StatusUnauthorized)
		return
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Iss != "5154941" || claims.Exp < time.Now().Unix() {
		http.Error(w, `{"message":"'Issuer' claim ('iss') must be an Integer"}`, http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	tok := fmt.Sprintf("ghs_fake%d", len(f.minted)+1)
	f.minted = append(f.minted, tok)
	f.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{"token": tok, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
}

// authorized is whether a request carries the test token or a token the fake minted.
func (f *fakeGitHub) authorized(h string) bool {
	if h == "Bearer test-token" {
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, tok := range f.minted {
		if h == "Bearer "+tok {
			return true
		}
	}
	return false
}

// revokeTokens makes every token minted so far one GitHub no longer accepts, as a revoked token or
// one the io kept past its expiry is.
func (f *fakeGitHub) revokeTokens() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.minted {
		f.minted[i] = "revoked:" + f.minted[i]
	}
}

// mintCount is how many installation tokens the fake minted.
func (f *fakeGitHub) mintCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.minted)
}

// openedCount is how many pull requests the out half opened.
func (f *fakeGitHub) openedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opened
}

// issuesOpenedCount is how many issues the out half opened.
func (f *fakeGitHub) issuesOpenedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.issuesOpened
}

// statusesOn is every commit status posted in a repository, in order.
func (f *fakeGitHub) statusesOn(repo string) []fakeStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeStatus(nil), f.repos[repo].statuses...)
}

// commentsOn is the bodies of the comments on one issue or pull request, in the order made.
func (f *fakeGitHub) commentsOn(repo string, on int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.repos[repo].comments {
		if c.on == on {
			out = append(out, c.body)
		}
	}
	return out
}

// editCount is how many comment edits the out half made.
func (f *fakeGitHub) editCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.edits
}

// mergeCount is how many pull requests the out half merged.
func (f *fakeGitHub) mergeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.merges
}

// closeCount is how many issues and pull requests the out half closed.
func (f *fakeGitHub) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

// reviewEvents is the events of the reviews the out half posted, in order.
func (f *fakeGitHub) reviewEvents() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reviews...)
}
