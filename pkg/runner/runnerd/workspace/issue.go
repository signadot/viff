package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/signadot/tony-format/git-issue/issuelib"
	"github.com/signadot/tony-format/git-issue/ops"
)

// A REPOSITORY'S OWN TRACKER is the issues `git issue` keeps as refs in it,
// `refs/git-issues/v1/open/<id>` and `.../closed/<id>`. It is distributed the way the code is: a
// task writes issues in its checkout with no credential, as it commits, and they leave when they
// are pushed, as commits do. So the tracker has two kinds of act here. CreateIssue, CommentIssue
// and CloseIssue write in the checkout, in the task's own process. FetchIssues and RefreshIssues bring the remote's tracker
// into the checkout, and PushIssues sends the checkout's to the remote: the runner's, since each
// reaches the remote with its credential.

// IssueRefs is the tracker's namespace as a refspec that brings every generation of it: open and
// closed issues, and the reverse index. No `+`: a fetch by it forces instead, for the reason a
// mirror's own refspecs do (mirrorSpecs).
const IssueRefs = "refs/git-issues/*:refs/git-issues/*"

// CreateIssue files an issue in this checkout's tracker, as `git issue create` there would: the
// title is the first line of its description, as a heading, and the body follows. It is a ref
// in the checkout and nothing has left: PushIssues is how it does. A body is not required.
func (t *Tree) CreateIssue(title, body string) (*issuelib.Issue, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("an issue needs a title")
	}
	description := "# " + title + "\n"
	if body = strings.TrimSpace(body); body != "" {
		description += "\n" + body + "\n"
	}
	issue, err := issuelib.NewGitStoreAt(t.Dir, io.Discard).Create(title, description)
	if err != nil {
		return nil, fmt.Errorf("repo %s: filing an issue: %w", t.Repo.Name(), err)
	}
	return issue, nil
}

// PushIssues sends the checkout's tracker to the remote: every `refs/git-issues/*` ref, each to
// its own name, fast-forward only, and the ref a close or a reopen left deleted (movedIssues). An
// issue is a chain of commits on its ref, so one edited here and at the remote both is a ref the
// remote refuses as non-fast-forward: that is the refusal, in words that say to pull first
// (git-issue's own "edited on both sides"). It answers how many issue refs it sent: none when
// the checkout holds no issue, or the remote already has them all.
//
// From the checkout rather than through the mirror, because what is pushed is the task's own
// refs as the task wrote them — there is no trailer to check and nothing to stage — and the
// credential is used here, in the runner's process, which the task's is not.
func (m *Mirrors) PushIssues(ctx context.Context, dir, remote string, auth transport.AuthMethod) (refs int, refusal string, err error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return 0, "", err
	}
	iter, err := repo.References()
	if err != nil {
		return 0, "", err
	}
	here := map[string]plumbing.Hash{}
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		if strings.HasPrefix(ref.Name().String(), "refs/git-issues/") && ref.Type() == plumbing.HashReference {
			here[ref.Name().String()] = ref.Hash()
		}
		return nil
	})
	if refs = len(here); refs == 0 {
		return 0, "", nil
	}
	// Pinned to the remote the spawn named, held in memory — never a name looked up in the
	// checkout's `.git/config`, which the task writes.
	rem := git.NewRemote(repo.Storer, &config.RemoteConfig{Name: deliveryRemote, URLs: []string{remote}})
	specs := []config.RefSpec{IssueRefs}
	left, moved, err := movedIssues(ctx, repo, rem, here, auth)
	switch {
	case err != nil:
		return refs, "", err
	case moved != "":
		return refs, "an issue moved at the remote (closed or reopened there) since this checkout last pulled: pull the tracker (git-issue-pull), then push again; if the pull leaves it as it is, it was edited here as well, and a person settles it: " + moved, nil
	}
	// Each delete under a lease on what the listing read: a remote that moved the ref since
	// refuses the whole push, rather than losing what it wrote.
	var leases []config.RefSpec
	for _, r := range left {
		specs = append(specs, config.RefSpec(":"+r.Name().String()))
		leases = append(leases, config.RefSpec(r.Hash().String()+":"+r.Name().String()))
	}
	err = rem.PushContext(ctx, &git.PushOptions{RemoteName: deliveryRemote, RefSpecs: specs, Auth: auth, RequireRemoteRefs: leases})
	switch {
	case errors.Is(err, git.NoErrAlreadyUpToDate):
		return 0, "", nil
	case err == nil:
		return refs, "", nil
	}
	if why := Refusal(err); why != "" {
		return refs, "an issue was edited here and at the remote both; pull the tracker first (git-issue-pull), then push again: " + why, nil
	}
	if strings.Contains(err.Error(), "required to be") { // a lease the remote no longer holds
		return refs, "an issue moved at the remote while this push was made; pull the tracker first (git-issue-pull), then push again: " + err.Error(), nil
	}
	return refs, "", fmt.Errorf("pushing the issues to %s: %w", remote, err)
}

// movedIssues is the remote's refs a push deletes: a close or a reopen moves an issue between
// `open/` and `closed/`, and the ref it left stays at the remote unless the push deletes it, so
// the remote would hold the issue in both and list it open still. A ref is deleted only where
// the remote's copy is one the move was made from, an ancestor of the ref it moved to; one the
// remote edited since is the both-sides edit, named in moved, and nothing is sent.
func movedIssues(ctx context.Context, repo *git.Repository, rem *git.Remote, here map[string]plumbing.Hash, auth transport.AuthMethod) (left []*plumbing.Reference, moved string, err error) {
	there, err := rem.ListContext(ctx, &git.ListOptions{Auth: auth})
	switch {
	case errors.Is(err, transport.ErrEmptyRemoteRepository):
		return nil, "", nil
	case err != nil:
		return nil, "", fmt.Errorf("listing the remote's issues: %w", err)
	}
	for _, r := range there {
		name := r.Name().String()
		to := movedFrom(name)
		if to == "" {
			continue
		}
		tip, ok := here[to]
		if _, stays := here[name]; stays || !ok {
			continue
		}
		// An error is a commit this checkout has not seen: the remote moved past it.
		if ok, err := descends(repo, tip, r.Hash()); err != nil || !ok {
			return nil, strings.TrimPrefix(name, "refs/git-issues/") + " moved at the remote", nil
		}
		left = append(left, r)
	}
	return left, "", nil
}

// movedFrom is the name an issue's ref has in the other namespace: `closed/<id>` for
// `open/<id>`, and the reverse; empty for a ref that is no issue's.
func movedFrom(name string) string {
	if xidr, ok := strings.CutPrefix(name, issuelib.OpenPrefix); ok {
		return issuelib.ClosedPrefix + xidr
	}
	if xidr, ok := strings.CutPrefix(name, issuelib.ClosedPrefix); ok {
		return issuelib.OpenPrefix + xidr
	}
	return ""
}

// FetchIssues brings the mirror's copy of a repository's tracker up to the remote's: the issue
// refs, which the ordinary fetch leaves out (a mirror fetches branches and tags). It answers the
// mirror's directory, for RefreshIssues. A remote with no tracker is a mirror with none, and no
// error.
func (m *Mirrors) FetchIssues(ctx context.Context, remote string) (string, error) {
	dir := m.Dir(remote)
	lock := m.lockFor(dir)
	lock.Lock()
	defer lock.Unlock()
	mirror, err := git.PlainOpen(dir)
	if err != nil {
		return "", fmt.Errorf("mirror of %s: %w", remote, err)
	}
	// Pruned: an issue closed or reopened at the remote leaves the name it moved from, and a
	// mirror that kept it would hand every checkout the issue under both names.
	err = mirror.FetchContext(ctx, &git.FetchOptions{RemoteName: "origin",
		RefSpecs: []config.RefSpec{IssueRefs}, Auth: m.auth(remote), Force: true, Prune: true})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) && !errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return "", fmt.Errorf("fetching the issues of %s: %w", remote, err)
	}
	return dir, nil
}

// RefreshIssues brings one checkout's copy of its repository's tracker up to the mirror's, which
// the caller has just fetched (FetchIssues): the mirror's objects are linked in, and every
// `refs/git-issues/*` ref is set to the mirror's, under its own name, so `git issue` in the
// checkout reads what the remote holds. It answers what moved, and the open issues as the
// checkout now lists them. Safe for the reason Refresh is: objects are only added, and the refs
// written are the tracker's, which nothing in the checkout holds a lock on. An issue the task
// edited here and the remote moved too is left as the task has it: the push says so.
func (t *Tree) RefreshIssues(mirror string) (moved []string, open []*issuelib.Issue, err error) {
	if err := linkObjects(mirror, t.Dir); err != nil {
		return nil, nil, fmt.Errorf("repo %s: sharing the mirror's objects: %w", t.Repo.Name(), err)
	}
	mr, err := git.PlainOpen(mirror)
	if err != nil {
		return nil, nil, fmt.Errorf("repo %s: %w", t.Repo.Name(), err)
	}
	// Opened afresh, so this side sees the packs just linked in: go-git reads the list once.
	repo, err := git.PlainOpen(t.Dir)
	if err != nil {
		return nil, nil, fmt.Errorf("repo %s: %w", t.Repo.Name(), err)
	}
	refs, err := mr.References()
	if err != nil {
		return nil, nil, fmt.Errorf("repo %s: %w", t.Repo.Name(), err)
	}
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name()
		if !strings.HasPrefix(name.String(), "refs/git-issues/") || ref.Type() != plumbing.HashReference {
			return nil
		}
		was := "new"
		// An issue is one chain whichever namespace holds it: one this checkout has under the
		// other (open/ for closed/, or the reverse) is the same issue, moved at the remote.
		// Where the remote's tip carries this checkout's, the move is taken, and the ref it
		// left goes; otherwise the checkout's stands, the task's, and the push says so.
		if other := movedFrom(name.String()); other != "" {
			if _, both := mr.Storer.Reference(plumbing.ReferenceName(other)); both != nil { // the remote holds it under this name alone
				if old, err := repo.Storer.Reference(plumbing.ReferenceName(other)); err == nil {
					if ok, _ := descends(repo, ref.Hash(), old.Hash()); !ok {
						return nil
					}
					if err := repo.Storer.SetReference(plumbing.NewHashReference(name, ref.Hash())); err != nil {
						return err
					}
					if err := repo.Storer.RemoveReference(old.Name()); err != nil {
						return err
					}
					moved = append(moved, strings.TrimPrefix(name.String(), "refs/git-issues/")+" "+
						ShortSha(old.Hash().String())+".."+ShortSha(ref.Hash().String())+", from "+
						strings.TrimPrefix(other, "refs/git-issues/"))
					return nil
				}
			}
		}
		if old, err := repo.Storer.Reference(name); err == nil {
			if old.Hash() == ref.Hash() {
				return nil
			}
			if ok, _ := descends(repo, ref.Hash(), old.Hash()); !ok {
				return nil // edited here too: the task's, until it pulls by hand or pushes and is told
			}
			was = ShortSha(old.Hash().String())
		}
		if err := repo.Storer.SetReference(plumbing.NewHashReference(name, ref.Hash())); err != nil {
			return err
		}
		moved = append(moved, strings.TrimPrefix(name.String(), "refs/git-issues/")+" "+was+".."+ShortSha(ref.Hash().String()))
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("repo %s: %w", t.Repo.Name(), err)
	}
	sort.Strings(moved)
	open, err = issuelib.NewGitStoreAt(t.Dir, io.Discard).List(false)
	if err != nil {
		return nil, nil, fmt.Errorf("repo %s: listing its issues: %w", t.Repo.Name(), err)
	}
	return moved, open, nil
}

// descends reports whether commit h has old among its ancestors (or is old): a fast-forward.
func descends(repo *git.Repository, h, old plumbing.Hash) (bool, error) {
	c, err := repo.CommitObject(h)
	if err != nil {
		return false, err
	}
	o, err := repo.CommitObject(old)
	if err != nil {
		return false, err
	}
	return o.IsAncestor(c)
}

// CommentIssue says something on an issue in this checkout's tracker, as `git issue comment`
// there would: a commit on the issue's ref, here, until PushIssues. It answers the issue.
func (t *Tree) CommentIssue(id, text string) (*issuelib.Issue, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("a comment needs a body")
	}
	issue, _, err := ops.Comment(issuelib.NewGitStoreAt(t.Dir, io.Discard), strings.TrimSpace(id), text)
	if err != nil {
		return nil, fmt.Errorf("repo %s: commenting on %s: %w", t.Repo.Name(), id, err)
	}
	return issue, nil
}

// CloseIssue closes an issue in this checkout's tracker, as `git issue close` there would: its
// ref moves from open to closed, here, until PushIssues; commit, when given, is the one that
// closed it. It answers the issue.
func (t *Tree) CloseIssue(id, commit string) (*issuelib.Issue, error) {
	issue, err := ops.Close(issuelib.NewGitStoreAt(t.Dir, io.Discard), strings.TrimSpace(id), strings.TrimSpace(commit))
	if err != nil {
		return nil, fmt.Errorf("repo %s: closing %s: %w", t.Repo.Name(), id, err)
	}
	return issue, nil
}
