package task

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Repo is one repository a spawn asks its runner to check out for the task, and what the task
// may do to it. The spawn says this rather than the runner's configuration, because what a run
// may touch is the charter's to declare and the record's to show: `existing: [$subject.head]` is
// a sentence only a charter can write.
//
// A runner that cannot honour one — no credential for the forge, a sandbox that cannot reach it
// — refuses the launch; a run never gets a weaker version of what it asked for.
//
//tony:schemagen=verse-repo,notag
type Repo struct {
	// Repo is `owner/name` at github.com, or a git url. The workspace checks it out under the
	// name part, which is the directory the agent sees and the word a prompt uses for it.
	Repo string `tony:"field=repo"`
	// Access is `read` or `write`. `write` pushes as Push allows, and names it; `read`, the
	// default, may not push at all.
	Access string `tony:"field=access,omitzero"`
	// From is the branch the checkout starts at. Empty is the repository's default branch.
	From string `tony:"field=from,omitzero"`
	// Push is which branches the run may push to, and `write` names it: a repository nothing
	// may be pushed to is `read`.
	Push *RepoPush `tony:"field=push,omitzero"`
}

// Access values.
const (
	AccessRead  = "read"
	AccessWrite = "write"
)

// RepoPush is which branches a run may push to in one repository.
//
//tony:schemagen=verse-repo-push,notag
type RepoPush struct {
	// New are patterns for branches the run may create: `verse/*` is one segment under verse,
	// `verse/**` any depth. A branch a pattern matches is the run's own: it may be pushed again,
	// rewritten and deleted by a later run of the same charter.
	New []string `tony:"field=new,omitzero"`
	// Existing are branches that already exist, by exact name. A push to one fast-forwards it
	// unless Force says otherwise.
	Existing []string `tony:"field=existing,omitzero"`
	// Force lets a push to an Existing branch rewrite it.
	Force bool `tony:"field=force,omitzero"`
}

// Check is what is wrong with a repository as a spawn wrote it, before anything is launched: an
// access that is not a word this knows, a push on a repository that may not write, a write that
// names nothing to push, a pattern that will not parse. Each is refused where the charter is installed, with its author present,
// rather than at the first firing.
func (r Repo) Check() error {
	if strings.TrimSpace(r.Repo) == "" {
		return fmt.Errorf("a repo entry names no repository: `repo: owner/name`")
	}
	switch r.Access {
	case "", AccessRead, AccessWrite:
	default:
		return fmt.Errorf("repo %s: access %q is not `%s` or `%s`", r.Repo, r.Access, AccessRead, AccessWrite)
	}
	if r.Push == nil {
		if r.Access == AccessWrite {
			return fmt.Errorf("repo %s: `access: %s` names what may be pushed in `push` (`new: [patterns]`, "+
				"`existing: [branches]`); a repository nothing may be pushed to is `%s`", r.Repo, AccessWrite, AccessRead)
		}
		return nil
	}
	if r.Access != AccessWrite {
		return fmt.Errorf("repo %s: `push` needs `access: %s`; `%s` may not push", r.Repo, AccessWrite,
			orRead(r.Access))
	}
	if len(r.Push.New) == 0 && len(r.Push.Existing) == 0 {
		return fmt.Errorf("repo %s: `push` names no branch: `new: [patterns]`, `existing: [branches]`", r.Repo)
	}
	for _, p := range r.Push.New {
		if err := CheckBranchPattern(p); err != nil {
			return fmt.Errorf("repo %s: push.new: %w", r.Repo, err)
		}
	}
	for _, b := range r.Push.Existing {
		if strings.TrimSpace(b) == "" {
			return fmt.Errorf("repo %s: push.existing has an empty branch name", r.Repo)
		}
	}
	if r.Push.Force && len(r.Push.Existing) == 0 {
		return fmt.Errorf("repo %s: `force` applies to `existing` branches, and none are named", r.Repo)
	}
	return nil
}

// CheckRepos checks each repository, and that no two would check out into one directory: the
// same repository written twice, or two repositories with one name under different owners.
// A path only the runner's working directory can name (`.`) is left to the runner.
func CheckRepos(repos []Repo) error {
	dirs := map[string]string{}
	for _, r := range repos {
		if err := r.Check(); err != nil {
			return err
		}
		dir, ok := r.Dir()
		if !ok {
			continue
		}
		if other, dup := dirs[dir]; dup {
			return fmt.Errorf("repos %s and %s would both check out as %s/", other, r.Repo, dir)
		}
		dirs[dir] = r.Repo
	}
	return nil
}

// RepoKind is how a `repo:` is written: `owner/name` at github.com, a git url (or an scp-style
// `user@host:path`), or a path on the runner's filesystem.
type RepoKind int

// The kinds of `repo:`.
const (
	RepoGitHub RepoKind = iota
	RepoURL
	RepoPath
)

// Kind reads how the repository is written. `owner/name` is one slash with no scheme, no path
// prefix and nothing an scp-style remote would have; a path is anything that is not a url and
// has no colon before its first slash.
func (r Repo) Kind() RepoKind {
	s := strings.TrimSpace(r.Repo)
	switch {
	case isGitHubShorthand(s):
		return RepoGitHub
	case strings.Contains(s, "://"):
		return RepoURL
	}
	if i := strings.IndexAny(s, ":/"); i >= 0 && s[i] == ':' {
		return RepoURL
	}
	return RepoPath
}

// Remote is the git remote the text names: `https://github.com/owner/name` for the shorthand,
// otherwise the text as written. A path is relative to wherever the runner runs, which only the
// runner can resolve.
func (r Repo) Remote() string {
	s := strings.TrimSpace(r.Repo)
	if r.Kind() == RepoGitHub {
		return "https://github.com/" + s
	}
	return s
}

// Dir is the directory the checkout takes, read from the text alone: the name part of
// `owner/name`, or the last segment of a url or path without `.git`. It is what the agent sees
// and the word a prompt uses for it, and it is what two entries must not share. false when the
// text alone cannot say — `.` or `..`, which name a directory only the runner's working
// directory can.
func (r Repo) Dir() (string, bool) {
	s := strings.TrimSpace(r.Repo)
	var name string
	switch r.Kind() {
	case RepoGitHub:
		name = s[strings.IndexByte(s, '/')+1:]
	case RepoPath:
		name = filepath.Base(filepath.Clean(s))
	default:
		trimmed := strings.TrimRight(s, "/")
		name = trimmed[strings.LastIndexAny(trimmed, "/:")+1:]
	}
	name = strings.TrimSuffix(name, ".git")
	if name == "" || name == "." || name == ".." || name == "/" {
		return "", false
	}
	return name, true
}

func isGitHubShorthand(s string) bool {
	if strings.Contains(s, "://") || strings.ContainsAny(s, ":@\\ ") ||
		strings.HasPrefix(s, "/") || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "~") {
		return false
	}
	owner, name, ok := strings.Cut(s, "/")
	return ok && owner != "" && name != "" && !strings.Contains(name, "/")
}

// MatchBranch is the glob rule a `push.new` pattern has: `*` matches within one path segment,
// `**` across segments, `?` one character, and a pattern with none of them is an exact name.
// Nothing else is special, which is what lets CheckBranchPattern refuse the rest where the
// pattern is written. It is the runner's rule and the install check's, one function, so a
// pattern that installs is a pattern that matches.
func MatchBranch(pattern, branch string) bool {
	pattern = strings.TrimPrefix(pattern, "refs/heads/")
	if !strings.ContainsAny(pattern, "*?") {
		return pattern == branch
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(branch)
}

// CheckBranchPattern is what is wrong with a `push.new` pattern: empty, or using a character
// MatchBranch does not read. A character class or an escape would install and then match
// nothing, which is a refusal the author never sees.
func CheckBranchPattern(p string) error {
	switch {
	case strings.TrimSpace(p) == "":
		return fmt.Errorf("an empty pattern")
	case strings.ContainsAny(p, "[]\\"):
		return fmt.Errorf("pattern %q: only `*`, `**` and `?` are special, not `[`, `]` or `\\`", p)
	}
	return nil
}

func orRead(access string) string {
	if access == "" {
		return AccessRead
	}
	return access
}
