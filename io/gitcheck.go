package io

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// The check input of the git io's out half: a tree of charters in the repository, checked against
// what runs at an address, and nothing installed (git-issue dzxn44gw).
//
//	to:    check.repos.verse      # everything after the first dot is the address
//	tree:  4b825dc6…              # the tree object, as the reflection reports it
//
// or, for what a ref holds now, resolved from this io's own copy when the check is made:
//
//	to:    check.repos.verse
//	ref:   verse/live             # a branch or tag, as `git rev-parse` resolves a short name
//	of:    charters               # which `-tree` directory; may be left out when there is one
//	round: 2026-10-04T21:00:00Z   # checked again whenever this changes
//
// This reads the tree's charter files out of the repository, as the install input does, and asks
// the verse's charter check (`POST /charter/check`) what an install of them at the address would
// do. The status answers `tree`, `address`, `checkedAt`, and the check's own answer: `verdict`
// (`installs` or `refused`), `refusal`, `packages` (each with what it would add, change, remove,
// and hold) and `pruned`; and `changes`, how many charters the install would add, change, remove
// or prune, so 0 is a tree that is what runs. A ref's check also answers `ref`, `sha` (the commit
// it stood at) and `round`, a ref that is not there is the verdict `missing`, and
// `differingFor` counts the rounds in a row the ref has differed from what runs at one commit: an
// install verse is making takes a moment, and a reader that waits for 2 does not mistake it for
// drift. A tree checked already, or a round checked already, costs nothing.
//
// A VERDICT OF `refused` IS AN ANSWER, NOT A FAILURE: the check did what it was asked, and the
// status carries the refusal for a reader to act on. A file in the tree that does not parse is
// such a verdict too, said in the same place, since it is the tree's fault and an install would
// be refused for it. What fails the input is not getting an answer: a tree the repository lacks
// (for good), or a verse that cannot be reached (tried again).
//
// IT IS NOT THE INSTALL. The check and the install are two inputs, two routes and two answers;
// nothing here installs, and nothing in the install checks on this one's behalf.

// checkTarget is the kind a check input's `to` begins with.
const checkTarget = "check"

// check carries one check input out, and answers the status fields.
func (k *GitSink) check(ctx context.Context, rp repo, in Input, address string) (entity.Obj, error) {
	if k.Verse == nil {
		return nil, Permanent(fmt.Errorf("this io reads no verse, so it checks nothing: it runs with no address to check against"))
	}
	segs, err := entity.PathSegs(address)
	if err != nil || len(segs) == 0 {
		return nil, Permanent(fmt.Errorf("`to` %q: %q is not an address", in.To, address))
	}
	at := entity.JoinSegs(segs)
	tree := strings.TrimSpace(entity.Str(in.Value, "tree"))
	ref := strings.TrimSpace(entity.Str(in.Value, "ref"))
	round := strings.TrimSpace(entity.Str(in.Value, "round"))
	if (tree == "") == (ref == "") {
		return nil, Permanent(fmt.Errorf("a check input carries {to, tree} or {to, ref}, and this one has %s", map[bool]string{true: "neither", false: "both"}[tree == ""]))
	}
	if entity.Get(in.Status, "error") == nil && entity.Str(in.Status, "verdict") != "" &&
		entity.Str(in.Status, "round") == round && (ref != "" || entity.Str(in.Status, "tree") == tree) {
		return nil, nil // checked, and the status says so
	}
	asked := entity.Obj{"address": at}
	if round != "" {
		asked["round"] = round
	}
	now := k.now().UTC().Format(time.RFC3339)
	sha := ""
	if ref != "" {
		asked["ref"] = ref
		if tree, sha, err = k.refTree(ctx, rp, ref, strings.TrimSpace(entity.Str(in.Value, "of"))); err != nil {
			return asked, err
		}
		if sha == "" {
			asked["checkedAt"], asked["verdict"], asked["sha"], asked["tree"] = now, "missing", entity.Removed(), entity.Removed()
			for _, f := range []string{"refusal", "packages", "pruned", "changes", "differingFor"} {
				asked[f] = entity.Removed()
			}
			return asked, nil
		}
		asked["sha"] = sha
	}
	asked["tree"] = tree
	full, err := gitCmd(ctx, rp.dir, "rev-parse", "--verify", "-q", tree+"^{tree}")
	if err != nil || !fullSHA.MatchString(full) {
		// NOT YET, rather than never: a tree a pull request proposes is reported by the github io,
		// which reads GitHub's API, and can reach this before this io's own copy has fetched the
		// branch that carries it. Asked again on the next pass, after the next fetch. (An install's
		// tree is one this io reflected itself, and stays a refusal for good.)
		return asked, fmt.Errorf("tree %q is no tree this repository has yet", tree)
	}
	docs, readErr, err := treeCharters(ctx, rp.dir, full)
	if err != nil {
		return asked, err
	}
	asked["checkedAt"] = now
	if readErr != nil {
		asked["verdict"], asked["refusal"] = "refused", readErr.Error()
		asked["packages"], asked["pruned"], asked["changes"] = entity.Removed(), entity.Removed(), entity.Removed()
		asked["differingFor"] = differingFor(in.Status, sha, tree, true)
		return asked, nil
	}
	resp, err := k.Verse.PostNodes("/charter/check", url.Values{"at": {at}}, docs)
	if err != nil {
		return asked, fmt.Errorf("checking tree %s at %s: %w", tree, at, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := client.StatusError(resp, "checking tree "+tree+" at "+at)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
			return asked, Permanent(err)
		}
		return asked, err
	}
	body, err := client.OKBody(resp)
	if err != nil {
		return asked, err
	}
	answer, err := codec.ParseNode(body, client.ResponseFormat(resp))
	if err != nil {
		return asked, fmt.Errorf("checking tree %s at %s: reading the answer: %w", tree, at, err)
	}
	// The lists REPLACE what the round before wrote: a status is written as a patch, and a
	// package's `added` of one round would otherwise stand beside the next round's `changed`,
	// since an answer with nothing added says nothing rather than an empty list. `!raw` is the
	// store's own escape for a subtree to land as written (entity.Raw).
	for _, f := range []string{"verdict", "refusal", "packages", "pruned"} {
		switch v := entity.Get(answer, f); {
		case v == nil:
			asked[f] = entity.Removed()
		case f == "packages" || f == "pruned":
			asked[f] = entity.Raw(v)
		default:
			asked[f] = v
		}
	}
	n := countChanges(answer)
	asked["changes"] = n
	asked["differingFor"] = differingFor(in.Status, sha, tree, n > 0 || entity.Str(answer, "verdict") != "installs")
	return asked, nil
}

// refTree resolves a ref to the commit it stands at and the tree of a `-tree` directory there:
// the empty tree where the directory is not at that commit, and no commit (sha "") for a ref
// this repository does not have.
func (k *GitSink) refTree(ctx context.Context, rp repo, ref, of string) (tree, sha string, err error) {
	if strings.HasPrefix(ref, "-") || strings.Contains(ref, "..") {
		return "", "", Permanent(fmt.Errorf("`ref` %q is not a ref's short name", ref))
	}
	var path string
	switch {
	case of != "":
		for _, t := range k.g.trees {
			if t.name == of {
				path = t.path
			}
		}
		if path == "" {
			return "", "", Permanent(fmt.Errorf("`of` %q names no `-tree` this io reflects", of))
		}
	case len(k.g.trees) == 1:
		path = k.g.trees[0].path
	default:
		return "", "", Permanent(fmt.Errorf("a check of a ref names which `-tree` directory (`of`): this io reflects %d", len(k.g.trees)))
	}
	sha, err = gitCmd(ctx, rp.dir, "rev-parse", "--verify", "-q", ref+"^{commit}")
	if err != nil || !fullSHA.MatchString(sha) {
		return "", "", nil
	}
	tree, err = gitCmd(ctx, rp.dir, "rev-parse", "--verify", "-q", sha+":"+path)
	if err != nil || !fullSHA.MatchString(tree) {
		return EmptyTree, sha, nil
	}
	return tree, sha, nil
}

// countChanges is how many charters an install of the checked tree would add, change, remove or
// prune.
func countChanges(answer entity.Payload) int {
	n := len(Strs(entity.Get(answer, "pruned")))
	if pkgs := ir.Uncomment(entity.Get(answer, "packages")); pkgs != nil {
		for _, p := range pkgs.Values {
			for _, f := range []string{"added", "changed", "removed"} {
				n += len(Strs(entity.Get(p, f)))
			}
		}
	}
	return n
}

// differingFor is the rounds in a row a check has found its ref (or tree) differing from what
// runs, at one commit: one more than the status says where it differed at this commit before,
// 1 where it starts to, and 0 where it does not differ.
func differingFor(status entity.Payload, sha, tree string, differs bool) int {
	if !differs {
		return 0
	}
	if entity.Str(status, "sha") == sha && entity.Str(status, "tree") == tree {
		if n := ir.Uncomment(entity.Get(status, "differingFor")); n != nil && n.Type == ir.NumberType && n.Int64 != nil && *n.Int64 > 0 {
			return int(*n.Int64) + 1
		}
	}
	return 1
}
