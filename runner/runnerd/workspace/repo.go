package workspace

import (
	"strings"

	"github.com/signadot/verse/runner"
)

// Repo is one repository in a workspace: the launch's repo (its name and where its checkout
// starts), and the remote it is fetched from. FromLaunch is the one way to make one from a
// launch. What the task may push to it is its block's push shapes, and the autonomy's at each push.
type Repo struct {
	runner.Repo
	// Remote is a git remote or a local path. It is fetched into a bare mirror shared by every
	// task using it, so N concurrent tasks cost one object store per repository, with the
	// mirrors' own credential for it.
	Remote string
}

// FromLaunch is the repos a launch asked for, as a workspace checks them out: each fetched from
// forge, where the runner reaches its repos' host (`https://github.com`, or a directory holding
// `owner/name` repositories), and checked out under its name.
func FromLaunch(repos []runner.Repo, forge string) ([]Repo, error) {
	if err := runner.CheckRepos(repos); err != nil {
		return nil, err
	}
	out := make([]Repo, len(repos))
	for i, r := range repos {
		out[i] = Repo{Repo: r, Remote: strings.TrimSuffix(forge, "/") + "/" + r.Path()}
	}
	return out, nil
}
