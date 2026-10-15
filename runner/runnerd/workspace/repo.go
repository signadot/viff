package workspace

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/signadot/verse/task"
)

// Repo is one repository in a workspace: where it comes from, where its checkout starts, and
// what the task may push to it. It is the spawn's repository (task.Repo) as this package checks
// it out, and FromSpec is the one way to make one from a spawn.
type Repo struct {
	// Name is the directory it checks out into: the name part of `owner/name`, or the last
	// segment of a url. It is what the agent sees and what a prompt refers to.
	Name string `tony:"field=name"`
	// Repo is a git remote or a local path. It is fetched into a bare mirror shared by every
	// task using it, so N concurrent tasks cost one object store per repository.
	Repo string `tony:"field=repo"`
	// Base is the ref a checkout starts from — a branch, a tag, a sha. Empty is the
	// repository's default branch, read from the mirror's HEAD when the workspace opens, and
	// written back here so the trees say where they started.
	Base string `tony:"field=base,omitzero"`
	// Access is task.AccessRead or task.AccessWrite. Only `write` may push, and only as Push
	// allows.
	Access string `tony:"field=access,omitzero"`
	// Push is which branches a task may push to here, and nil is NOTHING LEAVES. It is per
	// repository on purpose: the ordinary workspace is one repository being changed beside one
	// or more that are there to be read, and a reference repository that could push back is a
	// blast radius nobody asked for.
	Push *task.RepoPush `tony:"field=push,omitzero"`
}

// FromSpec is the repository a spawn asked for, as a workspace checks it out: `owner/name` is
// a repository at github.com, anything else is the remote as written, and the directory is the
// name part.
func FromSpec(s task.Repo) (Repo, error) {
	if err := s.Check(); err != nil {
		return Repo{}, err
	}
	remote := s.Remote()
	name, ok := s.Dir()
	if s.Kind() == task.RepoPath {
		// ABSOLUTE, because the side that uses it is not the side that wrote it: the task
		// process runs with its cwd set to the workspace, so `./scratch/x.git` names two
		// different places on the two sides, and only one of them exists. This is also where
		// `.` gets its name, which the text alone could not give it.
		abs, err := filepath.Abs(remote)
		if err != nil {
			return Repo{}, fmt.Errorf("repo %q: %w", s.Repo, err)
		}
		remote = abs
		name, ok = strings.TrimSuffix(filepath.Base(abs), ".git"), true
	}
	if !ok || name == "" || name == "." || name == ".." || name == "/" {
		return Repo{}, fmt.Errorf("repo %q: no directory name can be read from it", s.Repo)
	}
	return Repo{Name: name, Repo: remote, Base: s.From, Access: s.Access, Push: s.Push}, nil
}

// FromSpecs is FromSpec over a spawn's list, refusing two that would check out into one
// directory: two repositories named `app` under different owners are two trees with one name.
func FromSpecs(specs []task.Repo) ([]Repo, error) {
	if err := task.CheckRepos(specs); err != nil {
		return nil, err
	}
	var out []Repo
	dirs := map[string]string{}
	for _, s := range specs {
		r, err := FromSpec(s)
		if err != nil {
			return nil, err
		}
		if other, dup := dirs[r.Name]; dup {
			return nil, fmt.Errorf("repos %s and %s would both check out as %s/", other, s.Repo, r.Name)
		}
		dirs[r.Name] = s.Repo
		out = append(out, r)
	}
	return out, nil
}

// Check is what is wrong with a repository before anything is checked out, as a configuration
// that writes one directly is checked.
func (r Repo) Check() error {
	switch {
	case r.Name == "":
		return fmt.Errorf("a repo has no name — it is the directory the agent sees")
	case strings.ContainsAny(r.Name, `/\`) || r.Name == "." || r.Name == "..":
		return fmt.Errorf("repo %q: a name is one directory, so it may not contain a separator", r.Name)
	case r.Repo == "":
		return fmt.Errorf("repo %q names no repository", r.Name)
	}
	return task.Repo{Repo: r.Repo, Access: r.Access, From: r.Base, Push: r.Push}.Check()
}

// Writes reports whether anything may leave this repository at all.
func (r Repo) Writes() bool { return r.Access == task.AccessWrite && r.Push != nil }

// Permit decides one push: the ref it goes to, whether it may rewrite what is there, or why it
// is refused. The branch is what the task said; the answer is the allowance's, and it is read
// on BOTH sides of the process boundary — in the task, so the agent is told before anything is
// gated, and in the runner, which is the side that must not trust what the task sent.
//
//   - A branch a `new` pattern matches is the run's own: it may be created, pushed again,
//     rewritten, and deleted.
//   - A branch in `existing` may be updated; rewritten only if `force` says so; never deleted.
//   - Anything else is refused, with the allowance in words.
func (r Repo) Permit(branch string, del bool) (ref string, force bool, err error) {
	branch = strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	if err := checkBranchName(branch); err != nil {
		return "", false, err
	}
	if !r.Writes() {
		return "", false, fmt.Errorf("repository %s may not push: it is %s", r.Name, orRead(r.Access))
	}
	ref = "refs/heads/" + branch
	for _, p := range r.Push.New {
		if task.MatchBranch(p, branch) {
			return ref, true, nil
		}
	}
	for _, e := range r.Push.Existing {
		if strings.TrimPrefix(e, "refs/heads/") == branch {
			if del {
				return "", false, fmt.Errorf("branch %s of %s may be updated, not deleted: deleting is "+
					"for branches a run created (push.new: %s)", branch, r.Name, orNone(r.Push.New))
			}
			return ref, r.Push.Force, nil
		}
	}
	return "", false, fmt.Errorf("branch %q is not one %s may push to — it may create %s and update %s",
		branch, r.Name, orNone(r.Push.New), orNone(r.Push.Existing))
}

// Allowance says in words what may be pushed here, for the brief and the tool's description.
func (r Repo) Allowance() string {
	if !r.Writes() {
		return "read only — nothing here is delivered anywhere"
	}
	var parts []string
	if len(r.Push.New) > 0 {
		parts = append(parts, "create, rewrite or delete branches matching "+strings.Join(r.Push.New, ", "))
	}
	if len(r.Push.Existing) > 0 {
		how := "fast-forward"
		if r.Push.Force {
			how = "update or rewrite"
		}
		parts = append(parts, how+" "+strings.Join(r.Push.Existing, ", "))
	}
	return "may " + strings.Join(parts, "; ")
}

// checkBranchName refuses what git refuses in a ref component, and what would let a name reach
// outside refs/heads: empty, `..`, a leading dash, a control character, a lock suffix, `@{`.
func checkBranchName(b string) error {
	switch {
	case b == "":
		return fmt.Errorf("no branch named")
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

func orRead(access string) string {
	if access == "" {
		return task.AccessRead
	}
	return access
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}
