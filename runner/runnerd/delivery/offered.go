package delivery

import "github.com/signadot/verse/runner/runnerd/workspace"

// Offered is the names of the tools a workspace of repos would have, for a runner admitting a
// launch (verserunner.Admit) before any workspace exists: `push` where some repository may be
// pushed to, the pull-request tools where such a repository is at a forge, `fetch` where there is
// a repository at all. The tools themselves (Client.Tools) apply the same conditions tree by
// tree, and a test holds the two to each other.
func Offered(repos []workspace.Repo) []string {
	var out []string
	if writesAny(repos) {
		out = append(out, PushToolName)
	}
	if forgeAny(repos) {
		out = append(out, CreatePullToolName, UpdatePullToolName)
	}
	if len(repos) > 0 {
		out = append(out, FetchToolName)
	}
	return out
}

// writesAny is whether some repository may be pushed to.
func writesAny(repos []workspace.Repo) bool {
	for _, r := range repos {
		if r.Writes() {
			return true
		}
	}
	return false
}

// forgeAny is whether some repository that may be pushed to is at a forge with a pull-request API.
func forgeAny(repos []workspace.Repo) bool {
	for _, r := range repos {
		if _, _, _, err := PullAPI(r.Repo); err == nil && r.Writes() {
			return true
		}
	}
	return false
}
