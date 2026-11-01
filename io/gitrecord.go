package io

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
)

// A RECORD is a ref of verse's own in a repository, `refs/verse/<name>`: a chain of commits, each
// holding a tree as its whole tree, the one before as its parent, and the commit the tree was
// taken from as its `From:` trailer. It is what verse keeps in a repository about the repository,
// as git-issue keeps issues in refs of its own: no branch, so nothing checks it out, builds it, or
// opens a pull request onto it, and `git log refs/verse/<name>` is its history.
//
// THE TREE IS ITS IDENTITY. The repo-charters reconciler keeps `refs/verse/live` as what runs: the
// charters tree an answer said yes to. A commit of main carries the tree beside everything else
// in the repository, and is not what runs; the record's commit holds the tree alone.
//
// Asked of the out half:
//
//	to:     record.live        # everything after the first dot is the record's name
//	tree:   4b825dc6…          # a tree the repository has, as the reflection reports one
//	from:   4f668c1c…          # optional: the commit it was taken from, the entry's From: trailer
//	create: true               # optional: made where it is absent, and left alone where it stands
//
// and this answers at `<name>~status` with `ref` (the full ref name), `sha` (the entry's commit),
// `tree`, `at`, and `was`, the entry before, where there was one. A record whose last entry holds
// the tree already is in step and costs nothing, whatever `from` says: the same tree from another
// commit is the same record. An entry is appended with a lease on the one before, and pushed where
// the io reads from, as a branch is moved (gitout.go).
//
// Reflected as `git.<repo>.record.<name>`: `{repo, name, sha, tree, from}`, `from` empty for an
// entry with no From: trailer, which anybody who can push may write.

// recordTarget is the kind a record input's `to` begins with.
const recordTarget = "record"

// recordRefs is the namespace records live in.
const recordRefs = "refs/verse/"

// recordAuthor is who an entry is by: the io, writing as itself, with no address.
var recordAuthor = []string{"-c", "user.name=verse", "-c", "user.email=verse@verse.invalid"}

// recordName refuses for good a name that cannot be a record's: one git does not take as a ref
// under refs/verse/ (`git check-ref-format`, which is git's own rule, so a name it refuses is
// never pushed or written and failed there, to be tried again every pass), and a full refname,
// which would nest. A git that does not answer is an error that may pass.
func recordName(ctx context.Context, name string) error {
	if name == "" || strings.HasPrefix(name, "refs/") {
		return Permanent(fmt.Errorf("%q is not a record's name", name))
	}
	_, err := gitCmd(ctx, "", "check-ref-format", recordRefs+name)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return Permanent(fmt.Errorf("%q is not a record's name: git takes no ref %s", name, recordRefs+name))
	}
	return err
}

// record carries one record input out.
func (k *GitSink) record(ctx context.Context, rp repo, in Input, name string) (entity.Obj, error) {
	if err := recordName(ctx, name); err != nil {
		return nil, fmt.Errorf("`to` %q: %w", in.To, err)
	}
	ref := recordRefs + name
	want := strings.TrimSpace(entity.Str(in.Value, "tree"))
	if want == "" {
		return nil, Permanent(fmt.Errorf("a record input carries {to, tree}, and this one has no tree"))
	}
	// A tree the repository lacks is refused for good: the reflection read it before anything
	// asked to record it.
	tree, err := gitCmd(ctx, rp.dir, "rev-parse", "--verify", "-q", want+"^{tree}")
	if err != nil || !fullSHA.MatchString(tree) {
		return nil, Permanent(fmt.Errorf("tree %q is no tree this repository has", want))
	}
	from := strings.TrimSpace(entity.Str(in.Value, "from"))
	if from != "" {
		full, err := gitCmd(ctx, rp.dir, "rev-parse", "--verify", "-q", from+"^{commit}")
		if err != nil || !fullSHA.MatchString(full) {
			return nil, Permanent(fmt.Errorf("from %q is no commit this repository has", from))
		}
		from = full
	}
	cur, _ := gitCmd(ctx, rp.dir, "rev-parse", "--verify", "-q", ref)
	now := k.now().UTC().Format(time.RFC3339)
	if c := ir.Uncomment(entity.Get(in.Value, "create")); c != nil && c.Type == ir.BoolType && c.Bool && cur != "" {
		if entity.Str(in.Status, "ref") == ref {
			return nil, nil
		}
		return entity.Obj{"ref": ref, "sha": cur, "at": now, "kept": true}, nil
	}
	if cur != "" {
		if held, _ := gitCmd(ctx, rp.dir, "rev-parse", "--verify", "-q", cur+"^{tree}"); held == tree {
			if entity.Str(in.Status, "sha") == cur {
				return nil, nil // in step, and the status says so
			}
			return entity.Obj{"ref": ref, "sha": cur, "tree": tree, "at": now}, nil
		}
	}
	msg := name + ": " + tree + "\n"
	if from != "" {
		msg += "\nFrom: " + from + "\n"
	}
	args := append(append([]string{}, recordAuthor...), "commit-tree", tree, "-m", msg)
	if cur != "" {
		args = append(args, "-p", cur)
	}
	sha, err := gitCmd(ctx, rp.dir, args...)
	if err != nil || !fullSHA.MatchString(sha) {
		return nil, fmt.Errorf("writing an entry of %s: %v", ref, err)
	}
	if rp.remote != "" {
		args := []string{"push", "--force-with-lease=" + ref + ":" + cur, rp.remote, sha + ":" + ref}
		if _, err := gitCmd(ctx, rp.dir, args...); err != nil {
			return nil, fmt.Errorf("pushing %s to %s: %w", ref, rp.remote, err)
		}
	}
	if _, err := gitCmd(ctx, rp.dir, "update-ref", ref, sha, cur); err != nil {
		return nil, fmt.Errorf("writing %s: %w", ref, err)
	}
	fields := entity.Obj{"ref": ref, "sha": sha, "tree": tree, "at": now}
	if cur != "" {
		fields["was"] = cur
	}
	return fields, nil
}

// recordInfo is one record as the reflection reads it.
type recordInfo struct{ sha, tree, from string }

// gitRecords is every record the repository holds, by name.
func gitRecords(ctx context.Context, dir string) (map[string]recordInfo, error) {
	out, err := gitCmd(ctx, dir, "for-each-ref",
		"--format=%(refname) %(objectname) %(tree) %(trailers:key=From,valueonly,separator=)", recordRefs)
	if err != nil {
		return nil, err
	}
	recs := map[string]recordInfo{}
	for _, line := range nonEmptyLines(out) {
		f := strings.Fields(line)
		if len(f) < 3 || !strings.HasPrefix(f[0], recordRefs) {
			continue
		}
		r := recordInfo{sha: f[1], tree: f[2]}
		if len(f) >= 4 {
			r.from = f[3]
		}
		recs[strings.TrimPrefix(f[0], recordRefs)] = r
	}
	return recs, nil
}

// recordTree is the commit a record stands at and the tree it holds, or no commit (sha "") for a
// record the repository does not have.
func recordTree(ctx context.Context, dir, name string) (tree, sha string, err error) {
	if err := recordName(ctx, name); err != nil {
		return "", "", fmt.Errorf("`record`: %w", err)
	}
	sha, err = gitCmd(ctx, dir, "rev-parse", "--verify", "-q", recordRefs+name+"^{commit}")
	if err != nil || !fullSHA.MatchString(sha) {
		return "", "", nil
	}
	tree, err = gitCmd(ctx, dir, "rev-parse", "--verify", "-q", sha+"^{tree}")
	if err != nil || !fullSHA.MatchString(tree) {
		return "", "", fmt.Errorf("reading the tree of %s: %v", recordRefs+name, err)
	}
	return tree, sha, nil
}
