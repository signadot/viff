package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// repo is one registered repository: where it is, and — for a mirror — what it is a mirror OF.
//
// A remote makes the read a fetch, and that is the difference between a source that sits next
// to its truth (a build host, a laptop) and one deployed away from it (a pod, which has no
// repository until it makes one). Both are the same reflection afterwards; only how the local
// copy comes to be current differs.
type repo struct {
	dir    string
	remote string // when set, `git fetch` before reading — this dir is a mirror of it
}

// repoSet is the registry two sources share: Git reflects a repository's refs, GitIssue
// reflects the issues stored in one, and both answer the same three questions — which
// repositories am I serving, where is each one, and is it a mirror I have to fetch first.
//
// Shared rather than copied because addMirror is the part that would drift. Its "already
// cloned?" test is one stat of a file whose absence means something specific, and two copies
// of that, one of them maintained, is how a pod ends up re-cloning a volume on every restart.
type repoSet struct {
	mu    sync.Mutex
	repos map[string]repo
}

func newRepoSet() repoSet { return repoSet{repos: map[string]repo{}} }

// add registers a repository already on disk under id (defaulting to the dir's base name) and
// returns the id used. Re-adding an id repoints it.
func (s *repoSet) add(id, dir string) string {
	if id == "" {
		id = filepath.Base(filepath.Clean(dir))
	}
	s.mu.Lock()
	s.repos[id] = repo{dir: dir}
	s.mu.Unlock()
	return id
}

// addMirror registers a MIRROR of remote at dir, cloning it if dir does not already hold one,
// and returns the id used (defaulting to the remote's base name, less any `.git`).
//
// This is what makes a git-backed source deployable away from the repository it reflects. A
// pod has no repositories, and the two obvious answers are both worse than this one: a
// hostPath mounts somebody's laptop into the cluster, and a shared volume makes the reflection
// depend on whatever else writes to it. A mirror is the source's own copy of a remote it can
// reach, which is the ordinary shape of a read-only consumer of a repository.
//
// FETCHING IS THE READ, and that is why it does not violate "a source never acts on the
// external system": `git fetch` takes nothing from the remote but what `git ls-remote` would,
// and writes only into this source's private copy. Pushing, committing and tagging remain the
// outbound executor path, and none of them is here.
//
// A mirror clone maps every ref (`+refs/*:refs/*`), which is why one registry serves both
// sources: the same copy carries refs/heads for Git and refs/git-issues for GitIssue, and neither
// has to know the other exists.
//
// The clone happens HERE, at startup, rather than lazily inside a reflect: an unreachable
// remote or a bad credential is then a task that does not start, in front of the operator
// who just deployed it, instead of a registration that installs clean and reports a failure
// into lastError once a minute forever.
func (s *repoSet) addMirror(ctx context.Context, id, remote, dir string) (string, error) {
	if remote == "" {
		return "", fmt.Errorf("a mirror needs a remote to mirror")
	}
	if id == "" {
		id = MirrorID(remote)
	}
	// A mirror is a bare repository, so HEAD is the file that says one is already there. Not
	// re-cloned when it is: the volume outliving the pod is the point of putting it on one.
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		parent := filepath.Dir(dir)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return "", fmt.Errorf("%s: %w", parent, err)
		}
		// The clone target is made ABSOLUTE first, because the command runs with parent as its
		// working directory and git would otherwise resolve a relative dir a second time
		// against it — a `-state repos` cloning into `repos/repos/<id>`, which then fails
		// every pass with "chdir repos/<id>: no such file or directory" and reads like the
		// remote's fault. Only the default `-state /repos` being absolute kept this hidden.
		target := dir
		if abs, err := filepath.Abs(dir); err == nil {
			target = abs
		}
		if _, err := gitCmd(ctx, parent, "clone", "--mirror", remote, target); err != nil {
			return "", fmt.Errorf("cloning %s into %s: %w", remote, dir, err)
		}
	}
	s.mu.Lock()
	s.repos[id] = repo{dir: dir, remote: remote}
	s.mu.Unlock()
	return id, nil
}

// MirrorID is the id a remote gets when nobody named one: its base name, less any `.git`.
//
// Exported because the caller needs it BEFORE registering: a mirror's directory is derived
// from its id, so `sourced` has to know the default to build the path it then passes back in.
func MirrorID(remote string) string {
	return strings.TrimSuffix(filepath.Base(strings.TrimRight(remote, "/")), ".git")
}

// dirOf answers where a registered repository is.
func (s *repoSet) dirOf(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.repos[id]
	return r.dir, ok
}

// get answers with one registered repository.
func (s *repoSet) get(id string) (repo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.repos[id]
	return r, ok
}

// all is a copy of the registry, so a pass may iterate it without holding the lock across
// however many git invocations it makes.
func (s *repoSet) all() map[string]repo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]repo, len(s.repos))
	for id, r := range s.repos {
		out[id] = r
	}
	return out
}

// ids is every registered id, sorted — so two passes over the same repositories answer in the
// same order.
func (s *repoSet) ids() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.repos))
	for id := range s.repos {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
