package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/signadot/verse/pkg/task"
)

// GitHub's pull requests: how a runner whose forge is GitHub carries out the github.create-pr and
// github.update-pr steps, the one forge write it makes that is not a ref, and the github.list-prs
// and github.read-pr steps, which read the forge's pull requests for a task that holds no
// credential. Another forge's proposals (GitLab's merge requests) would be a second
// implementation beside this one.
//
// A proposal exists for a repository whose default branch nothing may push to: a protected branch takes
// a merge only through a pull request somebody approves, so "merge this into main" has no act to
// release there, and what a task can deliver is the proposal itself. A pull request is FROM a
// branch the task pushed: the push is what the gate approved, and the pull request is what the
// branch means at the forge. ONE open pull request per head: a push to the head updates it, and
// the next push after it merges or closes opens a fresh one, since a merged or closed pull
// request is not open.
//
// The runner performs it, with its own token, as it performs a push: the task asks over the
// channel and is told what happened.

// GitHubPR is one pull request as GitHub's API asks for it.
type GitHubPR struct {
	API   string `tony:"field=api"`
	Owner string `tony:"field=owner"`
	Repo  string `tony:"field=repo"`
	Head  string `tony:"field=head"`
	Base  string `tony:"field=base"`
	Title string `tony:"field=title,omitzero"`
	Body  string `tony:"field=body,omitzero"`
	// Draft opens it as a draft. An update leaves a pull request's draft state as it is.
	Draft bool `tony:"field=draft,omitzero"`
}

// GitHubAPI is github.com's API, where a runner whose forge is github.com opens pull requests
// and files issues. The API host is not the push host, which is why a network that allows hosts
// has to name it separately. A GitHub Enterprise Server forge has its own (`repos.api` in the
// runner's configuration).
const GitHubAPI = "https://api.github.com"

// RepoPath is the owner and name a repository url names at its forge: the two path segments a
// pull request or an issue is addressed by. Refused for a path on this filesystem, which has no
// forge, and so no pull requests and no issues.
func RepoPath(repo string) (owner, name string, err error) {
	u, err := url.Parse(repo)
	if err != nil || u.Host == "" {
		return "", "", fmt.Errorf("%q is not a forge url, and a pull request or an issue needs one", repo)
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("%q does not name an owner and a repository", repo)
	}
	return parts[0], parts[1], nil
}

// OpenGitHubPR makes sure an open pull request from the head to the base exists, and opens one if
// none does. Idempotent by asking first: a push that updated an open pull request has nothing to
// open, and the forge refuses a second one for the same head anyway, so asking is what turns
// that refusal into an answer.
func OpenGitHubPR(ctx context.Context, hc *http.Client, token string, s GitHubPR) (string, error) {
	repo := s.API + "/repos/" + url.PathEscape(s.Owner) + "/" + url.PathEscape(s.Repo) + "/pulls"
	q := url.Values{"state": {"open"}, "head": {s.Owner + ":" + s.Head}, "base": {s.Base}}
	var open []struct {
		Number int    `json:"number"`
		URL    string `json:"html_url"`
	}
	if err := githubAPI(ctx, hc, token, http.MethodGet, repo+"?"+q.Encode(), nil, &open); err != nil {
		return "", fmt.Errorf("listing pull requests from %s: %w", s.Head, err)
	}
	if len(open) > 0 {
		return fmt.Sprintf("pull request #%d from %s is open and now carries this push: %s",
			open[0].Number, s.Head, open[0].URL), nil
	}
	title := s.Title
	if title == "" {
		title = s.Head
	}
	body := map[string]any{"title": title, "head": s.Head, "base": s.Base, "body": s.Body, "draft": s.Draft}
	var made struct {
		Number int    `json:"number"`
		URL    string `json:"html_url"`
	}
	if err := githubAPI(ctx, hc, token, http.MethodPost, repo, body, &made); err != nil {
		return "", fmt.Errorf("opening a pull request from %s into %s: %w", s.Head, s.Base, err)
	}
	kind := "pull request"
	if s.Draft {
		kind = "draft pull request"
	}
	return fmt.Sprintf("opened %s #%d from %s into %s: %s", kind, made.Number, s.Head, s.Base, made.URL), nil
}

// githubAPI is one call to GitHub's REST API, with the runner's token.
func githubAPI(ctx context.Context, hc *http.Client, token, method, target string, in, out any) error {
	return githubCall(ctx, hc, token, method, target, in, out, 1<<20)
}

// githubStatus is the forge's answer when it was not a success, so a caller can tell a pull
// request that is not there (404) from one it could not read.
type githubStatus struct {
	Code int
	err  error
}

func (e *githubStatus) Error() string { return e.err.Error() }

// githubCall is githubAPI with a bound on the answer it reads. An answer past the bound is said
// to be, rather than cut and handed to the decoder as a document that ends early.
func githubCall(ctx context.Context, hc *http.Client, token, method, target string, in, out any, limit int64) error {
	var payload io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 {
		return &githubStatus{Code: res.StatusCode, err: fmt.Errorf("%s %s: %s: %s", method, target, res.Status, firstLine(string(raw)))}
	}
	if int64(len(raw)) > limit {
		return fmt.Errorf("%s %s: the answer is over %d bytes", method, target, limit)
	}
	return json.Unmarshal(raw, out)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 72 {
		s = s[:72]
	}
	return s
}

// UpdateGitHubPR changes the open pull request from the head: its title, body and base, each only
// where the spec says one. Refused, as an error the task reads, when none is open from that
// head: an update of nothing is a proposal that was never made.
func UpdateGitHubPR(ctx context.Context, hc *http.Client, token string, s GitHubPR) (string, error) {
	repo := s.API + "/repos/" + url.PathEscape(s.Owner) + "/" + url.PathEscape(s.Repo) + "/pulls"
	q := url.Values{"state": {"open"}, "head": {s.Owner + ":" + s.Head}}
	var open []struct {
		Number int    `json:"number"`
		URL    string `json:"html_url"`
	}
	if err := githubAPI(ctx, hc, token, http.MethodGet, repo+"?"+q.Encode(), nil, &open); err != nil {
		return "", fmt.Errorf("listing pull requests from %s: %w", s.Head, err)
	}
	if len(open) == 0 {
		return "", fmt.Errorf("no pull request is open from %s to update; create-pr opens one", s.Head)
	}
	body := map[string]string{}
	if s.Title != "" {
		body["title"] = s.Title
	}
	if s.Body != "" {
		body["body"] = s.Body
	}
	if s.Base != "" {
		body["base"] = s.Base
	}
	var made struct {
		Number int    `json:"number"`
		URL    string `json:"html_url"`
	}
	if err := githubAPI(ctx, hc, token, http.MethodPatch, fmt.Sprintf("%s/%d", repo, open[0].Number), body, &made); err != nil {
		return "", fmt.Errorf("updating pull request #%d from %s: %w", open[0].Number, s.Head, err)
	}
	return fmt.Sprintf("updated pull request #%d from %s: %s", made.Number, s.Head, made.URL), nil
}

// The pull request reads: github.list-prs and github.read-pr. Each is a step the task's autonomy
// decides and the runner's ceiling bounds, as the writes are, and each is a GET with the runner's
// token, so the task reads the forge without holding a credential for it.
const (
	OpGitHubListPRs = "github.list-prs"
	OpGitHubReadPR  = "github.read-pr"
)

// How the reads page: a listing item carries both repositories whole, so a page of 50 with
// bodies stays well inside readLimit, and most is where a read stops and says so rather than
// walking a repository of thousands of open pull requests for one task.
const (
	prsPerPage = 50
	prsMost    = 500
	readLimit  = 4 << 20
)

// ghUser and the rest are what the reads decode of GitHub's answers: what the task is told, no
// more.
type ghUser struct {
	Login string `json:"login"`
}

type ghRef struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type ghPR struct {
	Number             int      `json:"number"`
	Title              string   `json:"title"`
	State              string   `json:"state"`
	Draft              bool     `json:"draft"`
	Merged             bool     `json:"merged"`
	User               *ghUser  `json:"user"`
	Base               *ghRef   `json:"base"`
	Head               *ghRef   `json:"head"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
	HTMLURL            string   `json:"html_url"`
	Body               string   `json:"body"`
	RequestedReviewers []ghUser `json:"requested_reviewers"`
	RequestedTeams     []struct {
		Slug string `json:"slug"`
	} `json:"requested_teams"`
}

type ghReview struct {
	User        *ghUser `json:"user"`
	State       string  `json:"state"`
	SubmittedAt string  `json:"submitted_at"`
	Body        string  `json:"body"`
}

// githubPages reads a listing a page at a time until a short page or most items, and says whether
// most cut it short.
func githubPages[T any](ctx context.Context, hc *http.Client, token, target string, perPage, most int) ([]T, bool, error) {
	var all []T
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	for page := 1; ; page++ {
		var batch []T
		if err := githubCall(ctx, hc, token, http.MethodGet, fmt.Sprintf("%s%sper_page=%d&page=%d", target, sep, perPage, page), nil, &batch, readLimit); err != nil {
			return nil, false, err
		}
		all = append(all, batch...)
		if len(batch) < perPage {
			return all, false, nil
		}
		if len(all) >= most {
			return all[:most], true, nil
		}
	}
}

// ListGitHubPRs is the repository's open pull requests, one line each, newest change first as
// GitHub orders them.
func ListGitHubPRs(ctx context.Context, hc *http.Client, token, api, owner, repo string) (string, error) {
	pulls := api + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/pulls?state=open&sort=updated&direction=desc"
	prs, cut, err := githubPages[ghPR](ctx, hc, token, pulls, prsPerPage, prsMost)
	if err != nil {
		return "", fmt.Errorf("listing the open pull requests of %s/%s: %w", owner, repo, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s/%s: %d open pull request(s), most recently changed first", owner, repo, len(prs))
	if cut {
		fmt.Fprintf(&b, "; the first %d, and more may be open", prsMost)
	}
	for _, p := range prs {
		b.WriteString("\n")
		b.WriteString(prLine(p))
	}
	return b.String(), nil
}

// prLine is one pull request on one line: what a list says of each.
func prLine(p ghPR) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s — by %s", p.Number, p.Title, login(p.User))
	if p.Draft {
		b.WriteString(", draft")
	}
	if p.Base != nil && p.Head != nil {
		fmt.Fprintf(&b, ", %s <- %s", p.Base.Ref, p.Head.Ref)
	}
	fmt.Fprintf(&b, ", updated %s; %s", p.UpdatedAt, waitingOn(p))
	return b.String()
}

// waitingOn is whom a review is requested from: GitHub drops a reviewer once they review and adds
// them back on a re-request, so these are the people and teams the pull request waits on.
func waitingOn(p ghPR) string {
	var people, teams []string
	for _, u := range p.RequestedReviewers {
		people = append(people, u.Login)
	}
	for _, t := range p.RequestedTeams {
		teams = append(teams, t.Slug)
	}
	if len(people) == 0 && len(teams) == 0 {
		return "no review requested"
	}
	out := "review requested from " + strings.Join(people, ", ")
	if len(teams) > 0 {
		if len(people) > 0 {
			out += ", "
		}
		out += "teams " + strings.Join(teams, ", ")
	}
	return out
}

func login(u *ghUser) string {
	if u == nil || u.Login == "" {
		return "(nobody)"
	}
	return u.Login
}

// ReadGitHubPR is one pull request: its fields, whom it waits on, its reviews oldest first, and
// its description last, since that is the part its author wrote and the longest.
func ReadGitHubPR(ctx context.Context, hc *http.Client, token, api, owner, repo string, n int) (string, error) {
	one := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", api, url.PathEscape(owner), url.PathEscape(repo), n)
	var p ghPR
	if err := githubCall(ctx, hc, token, http.MethodGet, one, nil, &p, readLimit); err != nil {
		return "", fmt.Errorf("reading pull request #%d of %s/%s: %w", n, owner, repo, err)
	}
	reviews, cut, err := githubPages[ghReview](ctx, hc, token, one+"/reviews", 100, prsMost)
	if err != nil {
		return "", fmt.Errorf("reading the reviews of pull request #%d of %s/%s: %w", n, owner, repo, err)
	}
	var b strings.Builder
	state := p.State
	if p.Merged {
		state = "merged"
	}
	if p.Draft {
		state += ", draft"
	}
	fmt.Fprintf(&b, "#%d %s (%s) %s\n", p.Number, p.Title, state, p.HTMLURL)
	fmt.Fprintf(&b, "by %s", login(p.User))
	if p.Base != nil && p.Head != nil {
		fmt.Fprintf(&b, ", %s <- %s", p.Base.Ref, p.Head.Ref)
	}
	fmt.Fprintf(&b, "; created %s, updated %s\n%s\n", p.CreatedAt, p.UpdatedAt, waitingOn(p))
	fmt.Fprintf(&b, "%d review(s), oldest first", len(reviews))
	if cut {
		fmt.Fprintf(&b, "; the first %d, and there may be more", prsMost)
	}
	for _, r := range reviews {
		fmt.Fprintf(&b, "\n%s %s %s", r.SubmittedAt, login(r.User), r.State)
		if body := strings.TrimSpace(r.Body); body != "" {
			b.WriteString(": " + strings.ReplaceAll(body, "\n", "\n    "))
		}
	}
	if p.Head != nil && p.Head.SHA != "" {
		b.WriteString(prChecks(ctx, hc, token, api, owner, repo, p.Head.SHA))
	}
	if body := strings.TrimSpace(p.Body); body != "" {
		b.WriteString("\nThe description, as its author wrote it:\n" + body)
	}
	return b.String(), nil
}

// prChecks is what CI says of a pull request's head: its check runs and its commit statuses,
// those failing and those not yet done by name and link, those passing counted. A task that
// opened or pushed to a pull request reads here whether it is green, without a credential of its
// own. A source the forge will not give is said in its own line, and the counts say which source
// they are from, so a read of half the checks never reads as a green one; the rest of the read
// goes on.
func prChecks(ctx context.Context, hc *http.Client, token, api, owner, repo, sha string) string {
	at := fmt.Sprintf("%s/repos/%s/%s/commits/%s", api, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(sha))
	var runs struct {
		Runs []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			URL        string `json:"html_url"`
		} `json:"check_runs"`
	}
	var combined struct {
		Statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
			URL     string `json:"target_url"`
		} `json:"statuses"`
	}
	runsErr := githubCall(ctx, hc, token, http.MethodGet, at+"/check-runs?per_page=100", nil, &runs, readLimit)
	statusErr := githubCall(ctx, hc, token, http.MethodGet, at+"/status?per_page=100", nil, &combined, readLimit)
	if runsErr != nil && statusErr != nil {
		return fmt.Sprintf("\nchecks on %.9s: not read (check runs: %v; commit statuses: %v)", sha, runsErr, statusErr)
	}
	// What a failed call decoded, if anything, is not counted.
	if runsErr != nil {
		runs.Runs = nil
	}
	if statusErr != nil {
		combined.Statuses = nil
	}
	var failing, pending []string
	passing, skipped := 0, 0
	for _, r := range runs.Runs {
		switch {
		case r.Status != "completed":
			pending = append(pending, fmt.Sprintf("%s (%s) %s", r.Name, r.Status, r.URL))
		case r.Conclusion == "success" || r.Conclusion == "neutral":
			passing++
		case r.Conclusion == "skipped":
			skipped++
		default:
			failing = append(failing, fmt.Sprintf("%s (%s) %s", r.Name, r.Conclusion, r.URL))
		}
	}
	for _, st := range combined.Statuses {
		switch st.State {
		case "success":
			passing++
		case "pending":
			pending = append(pending, fmt.Sprintf("%s (pending) %s", st.Context, st.URL))
		default:
			failing = append(failing, fmt.Sprintf("%s (%s) %s", st.Context, st.State, st.URL))
		}
	}
	// Counts of one source alone say so, where the other was not read.
	of := ""
	switch {
	case runsErr != nil:
		of = ", from the commit statuses alone"
	case statusErr != nil:
		of = ", from the check runs alone"
	}
	var b strings.Builder
	if len(failing)+len(pending)+passing+skipped == 0 {
		fmt.Fprintf(&b, "\nchecks on %.9s: none reported%s", sha, of)
	} else {
		fmt.Fprintf(&b, "\nchecks on %.9s: %d failing, %d not done, %d passing, %d skipped%s", sha, len(failing), len(pending), passing, skipped, of)
	}
	if runsErr != nil {
		fmt.Fprintf(&b, "\n  not read: check runs (%v)", runsErr)
	}
	if statusErr != nil {
		fmt.Fprintf(&b, "\n  not read: commit statuses (%v)", statusErr)
	}
	for _, f := range failing {
		b.WriteString("\n  failing: " + f)
	}
	for _, p := range pending {
		b.WriteString("\n  not done: " + p)
	}
	return b.String()
}

// githubRead answers a `github.list-prs` or a `github.read-pr`: the step decided by the launch's
// autonomy and the runner's ceiling, then the read with this runner's token. A pull request that
// is not there is a refusal, an answer to re-plan on, where a forge that fails is an error.
func (s *Server) githubRead(ctx context.Context, tk Task, req Request) Reply {
	t := tk.Workspace.Tree(req.Repo)
	if t == nil {
		return Reply{Error: fmt.Sprintf("no repository %q in this workspace", req.Repo)}
	}
	if _, has := s.Ceilings["github"]; !has {
		return Reply{Refused: "this runner reads nothing at GitHub for its tasks: it has no `github` system"}
	}
	fields := map[string]string{"repo": t.Repo.Repo.Canonical()}
	if req.Op == OpGitHubReadPR {
		fields["pr"] = req.PR
	}
	if why := s.refused(tk, req.Decision, task.NewStep(req.Op, fields)); why != "" {
		return Reply{Refused: why}
	}
	owner, name, err := RepoPath(t.Repo.Remote)
	if err != nil {
		return Reply{Refused: err.Error()}
	}
	tok, err := tk.Credential(t.Repo)
	if err != nil {
		return Reply{Error: err.Error()}
	}
	var out string
	if req.Op == OpGitHubListPRs {
		out, err = ListGitHubPRs(ctx, s.http(), tok, s.Services.GitHubAPI, owner, name)
	} else {
		n, nerr := strconv.Atoi(req.PR)
		if nerr != nil || n <= 0 {
			return Reply{Refused: fmt.Sprintf("%q is not a pull request number", req.PR)}
		}
		out, err = ReadGitHubPR(ctx, s.http(), tok, s.Services.GitHubAPI, owner, name, n)
		var st *githubStatus
		if errors.As(err, &st) && st.Code == http.StatusNotFound {
			return Reply{Refused: fmt.Sprintf("there is no pull request #%d at %s/%s", n, owner, name)}
		}
	}
	if err != nil {
		return Reply{Error: err.Error()}
	}
	fmt.Fprintf(s.Log, "delivery: %s: %s of %s/%s\n", tk.ID, req.Op, owner, name)
	return Reply{OK: true, Out: out}
}
