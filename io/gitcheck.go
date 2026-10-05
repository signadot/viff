package io

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// The check input of the git io's out half: a tree of charters in the repository, checked against
// what runs at an address, and nothing installed (git-issue dzxn44gw).
//
//	to:   check.repos.verse       # everything after the first dot is the address
//	tree: 4b825dc6…               # the tree object, as the reflection reports it
//
// This reads the tree's charter files out of the repository, as the install input does, and asks
// the verse's charter check (`POST /charter/check`) what an install of them at the address would
// do. The status answers `tree`, `address`, `checkedAt`, and the check's own answer: `verdict`
// (`installs` or `refused`), `refusal`, `packages` (each with what it would add, change, remove,
// and hold) and `pruned`. A tree checked already costs nothing.
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
	if tree == "" {
		return nil, Permanent(fmt.Errorf("a check input carries {to, tree}, and this one has no tree"))
	}
	if entity.Str(in.Status, "tree") == tree && entity.Str(in.Status, "verdict") != "" && entity.Get(in.Status, "error") == nil {
		return nil, nil // checked, and the status says so
	}
	asked := entity.Obj{"tree": tree, "address": at}
	full, err := gitCmd(ctx, rp.dir, "rev-parse", "--verify", "-q", tree+"^{tree}")
	if err != nil || !fullSHA.MatchString(full) {
		return asked, Permanent(fmt.Errorf("tree %q is no tree this repository has", tree))
	}
	docs, readErr, err := treeCharters(ctx, rp.dir, full)
	if err != nil {
		return asked, err
	}
	asked["checkedAt"] = k.now().UTC().Format(time.RFC3339)
	if readErr != nil {
		asked["verdict"], asked["refusal"] = "refused", readErr.Error()
		asked["packages"], asked["pruned"] = entity.Removed(), entity.Removed()
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
	for _, f := range []string{"verdict", "refusal", "packages", "pruned"} {
		if v := entity.Get(answer, f); v != nil {
			asked[f] = v
		} else {
			asked[f] = entity.Removed()
		}
	}
	return asked, nil
}
