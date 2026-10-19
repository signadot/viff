package runner

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Repo is one repository a run works in: a fact of the `git` system, under `with.git`. What may
// leave it is not here: a push is a step, `git.push`, and the task's policy decides each by its
// repo, ref, and whether it deletes or rewrites.
//
//tony:schemagen=verse-runner-repo,notag
type Repo struct {
	// Repo is `owner/name`, a repo on github.com, or `host/owner/name` on the forge at host.
	Repo string `tony:"field=repo"`
	// From is the branch the workspace starts at. Absent is the default branch.
	From string `tony:"field=from,omitzero"`
}

// Tracker is a repository's own issue tracker a run works with: a fact of the `git-issue`
// system, under `with.git-issue`, naming a repository of `with.git`, in whose checkout the
// tracker is.
//
//tony:schemagen=verse-runner-tracker,notag
type Tracker struct {
	Repo string `tony:"field=repo"`
}

// DefaultHost is the forge a repo written `owner/name` is on.
const DefaultHost = "github.com"

// Host is the forge the repo is on: the host of `host/owner/name`, in lower case (a host name is
// case-insensitive), else DefaultHost.
func (r Repo) Host() string {
	if host, path, ok := strings.Cut(r.Repo, "/"); ok && strings.Contains(path, "/") {
		return strings.ToLower(host)
	}
	return DefaultHost
}

// Path is the repo at its forge: `owner/name`.
func (r Repo) Path() string {
	if host, path, ok := strings.Cut(r.Repo, "/"); ok && strings.Contains(path, "/") && host != "" {
		return path
	}
	return r.Repo
}

// Name is the directory the repo checks out into, and the word a prompt uses for it: the name
// part of `owner/name`.
func (r Repo) Name() string {
	_, name, _ := strings.Cut(r.Path(), "/")
	return name
}

// Canonical is the repo as a step's `repo` field spells it, however the spawn wrote it:
// `host/owner/name`, in lower case, as the forge reads a name. A policy's or a ceiling's `repo`
// pattern is held against this spelling, so `Acme/Secret` in a spawn is the repository a row
// on `github.com/acme/secret` names.
func (r Repo) Canonical() string { return strings.ToLower(r.Host() + "/" + r.Path()) }

// CheckWith checks the facts under `with`: each repo of `git`, that no two check out into one
// directory (the same repo twice, or two repos with one name under different owners or
// forges), and that each tracker of `git-issue` names a repo of `git`.
func CheckWith(w With) error {
	if err := CheckGit(w.Git); err != nil {
		return err
	}
	for i, t := range w.GitIssue {
		if !hasRepo(w.Git, t.Repo) {
			return fmt.Errorf("with.git-issue[%d]: %q is not a repo of with.git, whose checkout the tracker is in", i, t.Repo)
		}
	}
	return nil
}

// CheckGit checks each repo of `with.git`, and that no two check out into one directory.
func CheckGit(repos []Repo) error {
	dirs := map[string]string{}
	for i, r := range repos {
		if err := r.check(); err != nil {
			return fmt.Errorf("with.git[%d]: %w", i, err)
		}
		if other, dup := dirs[r.Name()]; dup {
			return fmt.Errorf("with.git[%d]: %s and %s would both check out as %s/", i, other, r.Repo, r.Name())
		}
		dirs[r.Name()] = r.Repo
	}
	return nil
}

func hasRepo(repos []Repo, name string) bool {
	for _, r := range repos {
		if r.Repo == name {
			return true
		}
	}
	return false
}

func (r Repo) check() error {
	parts := strings.Split(r.Repo, "/")
	if len(parts) == 3 && !repoHost.MatchString(strings.ToLower(parts[0])) {
		return fmt.Errorf("repo: %q is not owner/name or host/owner/name", r.Repo)
	}
	owner, name, ok := strings.Cut(r.Path(), "/")
	if len(parts) < 2 || len(parts) > 3 || !ok || !repoPart(owner) || !repoPart(name) {
		return fmt.Errorf("repo: %q is not owner/name or host/owner/name", r.Repo)
	}
	if strings.HasSuffix(name, ".git") {
		return fmt.Errorf("repo: %q names its repo without .git: %s", r.Repo, strings.TrimSuffix(r.Repo, ".git"))
	}
	return nil
}

// repoPart is an owner's or a repo's name as GitHub spells them: letters, digits, `-`, `_` and
// `.`, and never `.` or `..`, which as a path would reach outside the forge.
func repoPart(s string) bool {
	return s != "." && s != ".." && repoPartChars.MatchString(s)
}

var (
	repoPartChars = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	// repoHost is a forge's host name: labels of letters, digits and `-`, with at least one dot,
	// and maybe a port.
	repoHost = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+(:[0-9]{1,5})?$`)
)

// BranchState is what the runner knows of a branch when it performs a push or opens a pull
// request from it, and fills into the step for a row to match.
type BranchState struct {
	// Exists: the branch is at the forge. Own: its tip there is a commit this task made, by the
	// trailers every commit it delivers carries, however it got there (the task's push, or the
	// sweep landing one), and however many times the task has been resumed since. Created: this
	// run set out to make it, and it is still the run's, which is what lets a push rewrite it
	// without saying `force`.
	Exists, Own, Created bool
}

// CheckBranchName refuses what git refuses in a branch name, and what would reach outside
// refs/heads: empty, `..`, a leading dash, a control character, a lock suffix, `@{`.
func CheckBranchName(b string) error {
	switch {
	case b == "":
		return errors.New("no branch named")
	case strings.HasPrefix(b, "-"), strings.HasPrefix(b, "/"), strings.HasSuffix(b, "/"),
		strings.HasSuffix(b, "."), strings.HasSuffix(b, ".lock"),
		strings.Contains(b, ".."), strings.Contains(b, "//"), strings.Contains(b, "@{"),
		strings.Contains(b, "/."), strings.HasPrefix(b, "."),
		strings.ContainsAny(b, " ~^:?*[\\\x7f"):
		return fmt.Errorf("%q is not a branch name git accepts", b)
	}
	for _, c := range b {
		if c < 0x20 {
			return fmt.Errorf("%q is not a branch name git accepts", b)
		}
	}
	return nil
}

// BranchRef is the full ref of a branch a task named, checked: `refs/heads/<branch>`.
func BranchRef(branch string) (string, error) {
	branch = strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	if err := CheckBranchName(branch); err != nil {
		return "", err
	}
	return "refs/heads/" + branch, nil
}
