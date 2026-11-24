package io

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/client"
	"go.viff.dev/viff/pkg/entity"
)

// The out half of the git io: a ref written where the io reads, and a tree of charters installed
// into the viff. It exists for the repo-charters reconciler (git-issue j9vbwvjah12krs9tqsn0),
// which keeps what runs as a record (gitrecord.go) and has the charters installed from the
// repository's own copy (gitinstall.go). A tag or a branch it writes is at a sha that already
// exists in the repository; the one commit it makes is a record's entry, holding a tree the
// repository has. No tree is written and nothing is merged.
//
// Somebody writes an input under a repository's out place, `git.<repo>.out.<name>`, or a package
// writes one in an outbox it declares for that place:
//
//	to:  tag.v1.3                # everything after the first dot is the tag's name
//	sha: 4f668c1c…               # a commit the repository has; a tag or a short sha resolves
//
// or
//
//	to:  branch.viff/next       # a branch, moved to the sha, forward or back
//	sha: 4f668c1c…
//	create: true                 # or: made where it is absent, and left alone where it stands
//	                             # (a tag takes it too)
//
// and this answers at `<name>~status` with `ref` (the full ref name), `sha` (the commit, in full),
// `at`, and for a branch `was`, the sha it stood at before. A ref already at the sha costs
// nothing. A TAG IS NOT MOVED: one that stands at another sha is refused for good, since a tag is
// a record and a rollback is a new tag on an older commit. A branch moves with a lease on the sha
// it last had, so two writers racing for it do not silently overwrite each other.
//
// WHERE THE REF IS WRITTEN is where the io reads. A mirror pushes to the remote it fetches from,
// with the credential it fetches with (which must then be allowed to push), and writes its own
// copy of the ref so the next reflection sees it without waiting for the fetch. A repository on
// disk gets the ref written in place, which is what a laptop driving the reconciler against a
// clone wants.
type GitSink struct {
	// Viff is the viff instance an install input installs into, as this io (gitinstall.go). Nil
	// installs nothing, and an install input is refused for good.
	Viff *client.Client
	// As is the principal Viff acts as: who an obsolete input's answers are by
	// (gitobsolete.go).
	As string

	g   *Git
	now func() time.Time
}

// Sink is this source's out half, over every repository it reflects.
func (g *Git) Sink() *GitSink { return &GitSink{g: g, now: time.Now} }

// GitOutPlace is the segment a repository's out place ends in.
const GitOutPlace = "out"

// OutRef is where inputs for one repository are written: inside the repository's claim and
// outside every cover the reflection carries (`ref.*`, `head`), so no pass tombstones what
// somebody asked for.
func (g *Git) OutRef(id string) entity.Ref { return entity.RefAt(g.root, id, GitOutPlace) }

// OutRefs is OutRef for every registered repository.
func (g *Git) OutRefs() []entity.Ref {
	all := g.repos.all()
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]entity.Ref, 0, len(ids))
	for _, id := range ids {
		out = append(out, g.OutRef(id))
	}
	return out
}

// repoOf is the repository whose out place an input was asked at, or "" for a place this source
// does not serve.
func (g *Git) repoOf(place entity.Ref) (string, repo, bool) {
	segs := place.Segs()
	if len(segs) != 3 || segs[0] != g.root || segs[2] != GitOutPlace {
		return "", repo{}, false
	}
	r, ok := g.repos.get(segs[1])
	return segs[1], r, ok
}

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// Deliver makes the repository's ref match one input: a tag made, or a branch moved.
func (k *GitSink) Deliver(ctx context.Context, in Input) (entity.Obj, error) {
	_, rp, ok := k.g.repoOf(in.Place)
	if !ok {
		return nil, Permanent(fmt.Errorf("%s is no repository's out place this io serves", in.Place))
	}
	kind, name, found := strings.Cut(in.To, ".")
	if kind == installTarget && found {
		return k.install(ctx, rp, in, name)
	}
	if kind == checkTarget && found {
		return k.check(ctx, rp, in, name)
	}
	if kind == recordTarget && found {
		return k.record(ctx, rp, in, name)
	}
	if in.To == obsoleteTarget {
		return k.obsolete(ctx, in)
	}
	if !found || name == "" || (kind != "tag" && kind != "branch") {
		return nil, Permanent(fmt.Errorf("`to` is tag.<name>, branch.<name>, record.<name>, install.<address>, check.<address> or obsolete, not %q", in.To))
	}
	if strings.HasPrefix(name, "refs/") || strings.Contains(name, "..") || strings.HasSuffix(name, "/") {
		return nil, Permanent(fmt.Errorf("`to` %q: %q is not a ref's short name", in.To, name))
	}
	ref := "refs/heads/" + name
	if kind == "tag" {
		ref = "refs/tags/" + name
	}
	want := strings.TrimSpace(entity.Str(in.Value, "sha"))
	if want == "" {
		return nil, Permanent(fmt.Errorf("an input carries {to, sha}, and this one has no sha"))
	}
	// The commit, in full, and one the repository has. An object it does not have is refused for
	// good rather than retried: the reflection fetched main before anything asked to tag it, so
	// what is missing is a typo, and a typo asked again every pass is a pass wasted.
	sha, err := gitCmd(ctx, rp.dir, "rev-parse", "--verify", "-q", want+"^{commit}")
	if err != nil || !fullSHA.MatchString(sha) {
		return nil, Permanent(fmt.Errorf("sha %q is no commit this repository has", want))
	}
	cur, _ := gitCmd(ctx, rp.dir, "rev-parse", "--verify", "-q", ref)
	now := k.now().UTC().Format(time.RFC3339)
	// CREATE ONLY: a ref made once where it is absent and left alone where it stands. A branch
	// somebody else moves on is never moved back by this, and a tag that marks a commit once is
	// not refused when it is asked for again at another.
	if c := ir.Uncomment(entity.Get(in.Value, "create")); c != nil && c.Type == ir.BoolType && c.Bool && cur != "" {
		if entity.Str(in.Status, "ref") == ref {
			return nil, nil
		}
		return entity.Obj{"ref": ref, "sha": cur, "at": now, "kept": true}, nil
	}
	if cur == sha {
		if entity.Str(in.Status, "sha") == sha {
			return nil, nil // in step, and the status says so
		}
		return entity.Obj{"ref": ref, "sha": sha, "at": now}, nil
	}
	if kind == "tag" && cur != "" {
		return nil, Permanent(fmt.Errorf("tag %s stands at %s and is not moved to %s: a tag is a record, "+
			"and a rollback is a new tag on the older commit", name, cur, sha))
	}
	if rp.remote != "" {
		// Pushed with a lease on what the ref was, so a move somebody else made meanwhile is a
		// refusal rather than an overwrite. An empty lease says the ref must not exist yet.
		args := []string{"push", "--force-with-lease=" + ref + ":" + cur, rp.remote, sha + ":" + ref}
		if _, err := gitCmd(ctx, rp.dir, args...); err != nil {
			return nil, fmt.Errorf("pushing %s to %s: %w", ref, rp.remote, err)
		}
	}
	if _, err := gitCmd(ctx, rp.dir, "update-ref", ref, sha, cur); err != nil {
		return nil, fmt.Errorf("writing %s: %w", ref, err)
	}
	fields := entity.Obj{"ref": ref, "sha": sha, "at": now}
	if cur != "" {
		fields["was"] = cur
	}
	return fields, nil
}
