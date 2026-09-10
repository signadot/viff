package main

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

// A PULL REQUEST is the one thing a destination does after the push, and it is the only forge
// write this runner makes that is not a ref.
//
// It exists for a repository whose default branch nothing may push to: a protected branch takes
// a merge only through a pull request somebody approves, so "merge this into main" has no act to
// release there, and what a charter can deliver is the proposal itself. A destination carrying
// `pull:` keeps ONE open pull request from its ref — a push updates it, since the pull request's
// head is the ref, and the next push after it merges or closes opens a fresh one, since a merged
// or closed pull request is not open.
//
// It is an option on a destination and not a destination of its own, because a pull request is
// FROM a ref work went to: the push is what the gate approved and what the credential was
// released for, and the pull request is what the ref means at the forge. Splitting them would
// hand a task a second thing to ask for, whose only correct use is "right after the first".
//
// The child performs it, with the credential the push already holds, for the same reason the
// child pushes: the parent's protocol answers with a secret rather than a performance, and a
// second op for "and open the pull request" would be the growth that protocol exists to refuse.
// What the child may say about it is nothing — head, base, title and body all come from the
// environment through the parent's reply.

// Pull is the pull request a destination keeps open from its ref.
type Pull struct {
	// Base is the branch the pull request is into. Empty is the repository's own base, which
	// is what a proposal against the default branch wants and the only thing a fixed-base
	// destination could sensibly mean.
	Base string `tony:"field=base,omitzero"`
	// Title and Body take the $task and $sha the ref takes. An empty title names the head.
	Title string `tony:"field=title,omitzero"`
	Body  string `tony:"field=body,omitzero"`
}

// pullSpec is the pull request resolved by the parent and performed by the child: every field a
// forge asks for, and nothing the task chose.
type pullSpec struct {
	API   string `tony:"field=api"`
	Owner string `tony:"field=owner"`
	Repo  string `tony:"field=repo"`
	Head  string `tony:"field=head"`
	Base  string `tony:"field=base"`
	Title string `tony:"field=title,omitzero"`
	Body  string `tony:"field=body,omitzero"`
}

// githubAPI is the one forge this opens pull requests at. The API host is not the push host,
// which is why an environment's egress has to name it separately (checkReachable).
const githubAPI = "https://api.github.com"

// pullAPI is the forge a repository url names, as the API base and the two path segments a pull
// request is addressed by. Refused for anything that is not github.com: a local path has no pull
// requests, and another forge has another API, which would be a second implementation and not a
// second case.
func pullAPI(repo string) (api, owner, name string, err error) {
	u, err := url.Parse(repo)
	if err != nil || u.Host == "" {
		return "", "", "", fmt.Errorf("%q is not a forge url, and a pull request needs one", repo)
	}
	if u.Hostname() != "github.com" {
		return "", "", "", fmt.Errorf("pull requests are opened through GitHub's API, and %s is not github.com", u.Host)
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", fmt.Errorf("%q does not name an owner and a repository", repo)
	}
	return githubAPI, parts[0], parts[1], nil
}

// pullSpec resolves a destination's pull request for this tree: the ref it was pushed to is
// the head, and the environment's text is expanded the way the ref was.
func (t *Tree) pullSpec(d Destination, branch string) (pullSpec, error) {
	api, owner, name, err := pullAPI(t.Repo.Repo)
	if err != nil {
		return pullSpec{}, fmt.Errorf("destination %q: %w", d.Name, err)
	}
	base := d.Pull.Base
	if base == "" {
		base = t.Repo.Base
	}
	return pullSpec{
		API: api, Owner: owner, Repo: name,
		Head:  strings.TrimPrefix(t.DestRef(d.Name, branch), "refs/heads/"),
		Base:  strings.TrimPrefix(base, "refs/heads/"),
		Title: t.expand(d.Pull.Title, branch),
		Body:  t.expand(d.Pull.Body, branch),
	}, nil
}

// ensurePull makes sure an open pull request from the head to the base exists, and opens one if
// none does. Idempotent by asking first: a push that updated an open pull request has nothing to
// open, and the forge refuses a second one for the same head anyway, so asking is what turns
// that refusal into an answer.
func ensurePull(ctx context.Context, hc *http.Client, token string, s pullSpec) (string, error) {
	repo := s.API + "/repos/" + url.PathEscape(s.Owner) + "/" + url.PathEscape(s.Repo) + "/pulls"
	q := url.Values{"state": {"open"}, "head": {s.Owner + ":" + s.Head}, "base": {s.Base}}
	var open []struct {
		Number int    `json:"number"`
		URL    string `json:"html_url"`
	}
	if err := forge(ctx, hc, token, http.MethodGet, repo+"?"+q.Encode(), nil, &open); err != nil {
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
	if err := forge(ctx, hc, token, http.MethodPost, repo, body, &made); err != nil {
		return "", fmt.Errorf("opening a pull request from %s into %s: %w", s.Head, s.Base, err)
	}
	return fmt.Sprintf("opened pull request #%d from %s into %s: %s", made.Number, s.Head, s.Base, made.URL), nil
}

// forge is one call to the forge's API, with the credential the push was released.
func forge(ctx context.Context, hc *http.Client, token, method, target string, in, out any) error {
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
