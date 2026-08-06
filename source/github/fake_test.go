package github_test

import (
	"crypto/sha256"
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

	requests    int // every request that reached a handler
	notModified int // those answered 304
}

type fakeRepo struct {
	defaultBranch string
	sha           string
	private       bool
	cloneURL      string
	items         []*fakeItem
	runs          []*fakeRun

	discussions []*fakeDiscussion
	// discussionsOff is the feature turned off, which answers with an empty list exactly as a
	// repository with no discussions does — the confusion hasDiscussionsEnabled exists to
	// resolve. graphqlError is the endpoint refusing, which is HTTP 200 with an errors array.
	discussionsOff bool
	graphqlError   string
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
	head      string // pulls only
	headSHA   string
	labels    []string
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

	if r.Header.Get("Authorization") != "Bearer test-token" {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
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

	switch {
	case len(rest) == 0:
		f.reply(w, r, map[string]any{
			"full_name": name, "name": parts[2], "owner": map[string]any{"login": parts[1]},
			"private": repo.private, "archived": false, "fork": false,
			"default_branch": repo.defaultBranch, "clone_url": repo.cloneURL,
			"ssh_url":  "git@github.com:" + name + ".git",
			"html_url": "https://github.com/" + name,
		})
	case len(rest) >= 3 && rest[0] == "git" && rest[1] == "ref":
		if repo.sha == "" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		f.reply(w, r, map[string]any{"object": map[string]any{"sha": repo.sha}})
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
		f.reply(w, r, item.render(rest[0] == "pulls"))
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
		out = append(out, m)
	}
	return out
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

	var picked []*fakeItem
	for _, i := range repo.items {
		if what == "pulls" && !i.pull {
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

func (i *fakeItem) render(asPull bool) map[string]any {
	labels := make([]map[string]any, 0, len(i.labels))
	for _, l := range i.labels {
		labels = append(labels, map[string]any{"name": l})
	}
	m := map[string]any{
		"number": i.number, "title": i.title, "state": i.state,
		"updated_at": i.updatedAt, "created_at": "2026-01-01T00:00:00Z",
		"html_url": "https://github.com/x/y/" + strconv.Itoa(i.number),
		"user":     map[string]any{"login": "ana"},
		"labels":   labels,
	}
	if asPull {
		m["draft"] = false
		m["merged"] = i.state == "closed"
		m["base"] = map[string]any{"ref": "main", "sha": "bbbb2222"}
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
