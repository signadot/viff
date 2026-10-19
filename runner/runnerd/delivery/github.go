package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// GitHub's pull requests: how a runner whose forge is GitHub carries out the github.create-pr and
// github.update-pr steps, the one forge write it makes that is not a ref. Another
// forge's proposals (GitLab's merge requests) would be a second implementation beside this one.
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
}

// GitHubAPI is github.com's API, where a runner whose forge is github.com opens pull requests
// and files issues. The API host is not the push host, which is why a network that allows hosts
// has to name it separately. A GitHub Enterprise Server forge has its own (`systems.github.api`
// in the runner's configuration).
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
	body := map[string]string{"title": title, "head": s.Head, "base": s.Base, "body": s.Body}
	var made struct {
		Number int    `json:"number"`
		URL    string `json:"html_url"`
	}
	if err := githubAPI(ctx, hc, token, http.MethodPost, repo, body, &made); err != nil {
		return "", fmt.Errorf("opening a pull request from %s into %s: %w", s.Head, s.Base, err)
	}
	return fmt.Sprintf("opened pull request #%d from %s into %s: %s", made.Number, s.Head, s.Base, made.URL), nil
}

// githubAPI is one call to GitHub's REST API, with the runner's token.
func githubAPI(ctx context.Context, hc *http.Client, token, method, target string, in, out any) error {
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
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, target, res.Status, firstLine(string(raw)))
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
