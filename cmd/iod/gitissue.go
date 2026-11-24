package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"path/filepath"

	viffio "go.viff.dev/viff/pkg/io"
)

// The git-issue source: the issues `git issue` keeps inside a git repository, reflected.
//
// It takes the same -repo/-mirror registry as `iod git` and is a separate subcommand for
// the reason the source is a separate type: the two answer different questions about one
// directory, and an operator who wants branch state without a tracker — or a tracker without
// fifty commits a pass — says so by running one and not the other. Running both over the same
// repository is the ordinary case; they write disjoint slices, so nothing coordinates.
func gitIssueMain(args []string) error {
	fs := flag.NewFlagSet("git-issue", flag.ExitOnError)
	var c common
	c.bind(fs)
	var repos, mirrors strs
	fs.Var(&repos, "repo", "a repository already on disk, as [<id>=]<dir> (repeatable)")
	fs.Var(&mirrors, "mirror", "a remote to mirror and keep fetched, as [<id>=]<url> (repeatable)")
	state := fs.String("state", envOr("SOURCED_STATE", "/repos"),
		"where -mirror clones live (one directory per repo id)")
	bodies := fs.Bool("bodies", true,
		"reflect each issue's description body and the text of its discussion; off makes the "+
			"slice an index of issue state — with how many comments and when the last was — "+
			"rather than a copy of everyone's prose")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(repos) == 0 && len(mirrors) == 0 {
		return fmt.Errorf("nothing to reflect: name a -repo or a -mirror")
	}

	ctx := context.Background()
	if err := gitAsApp(ctx, *state); err != nil {
		return err
	}
	gi := viffio.NewGitIssue()
	gi.SetBodies(*bodies)
	for _, spec := range repos {
		id, dir, err := split(spec)
		if err != nil {
			return fmt.Errorf("-repo %q: %w", spec, err)
		}
		log.Printf("reflecting issues in %s at %s", gi.AddRepo(id, dir), dir)
	}
	for _, spec := range mirrors {
		id, remote, err := split(spec)
		if err != nil {
			return fmt.Errorf("-mirror %q: %w", spec, err)
		}
		if id == "" {
			id = viffio.MirrorID(remote)
		}
		// The clone happens here, at startup, and a failure ends the task — the same bargain
		// `iod git` makes, for the same reason: an unreachable remote is something to find
		// out while somebody is watching the rollout.
		dir := filepath.Join(*state, id)
		if _, err := gi.AddMirror(ctx, id, remote, dir); err != nil {
			return err
		}
		log.Printf("reflecting issues in %s, mirrored from %s into %s", id, remote, dir)
	}
	if err := gi.SetRoot(c.root); err != nil {
		return err
	}
	// No delivery routes, and no hook either. There is one reference-transaction hook per
	// repository and `iod git` is usually holding it, so this source converges on the
	// resync; an operator who wants it faster adds a line to their own hook, which
	// docs/reference/iod.md spells out.
	if c.hintURL != "" {
		log.Printf("hints accepted at %s — name %s to reconcile a repository",
			c.hintURL, gi.RepoRef("<repo>"))
	}
	return c.serve(gi.Root(), gi, nil)
}
