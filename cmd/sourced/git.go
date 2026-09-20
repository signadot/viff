package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/signadot/verse/source"
)

// The git source, served rather than run in task.
//
// verse can already reflect a local repository with `verse source add --dir`, and that path is
// not going anywhere. This is for the two cases it cannot reach: a repository on a machine
// verse is not running on, and a repository that does not exist yet where the source lives —
// which is every pod, and is what -mirror answers.
func gitMain(args []string) error {
	fs := flag.NewFlagSet("git", flag.ExitOnError)
	var c common
	c.bind(fs)
	var repos, mirrors strs
	fs.Var(&repos, "repo", "a repository already on disk, as [<id>=]<dir> (repeatable)")
	fs.Var(&mirrors, "mirror", "a remote to mirror and keep fetched, as [<id>=]<url> (repeatable)")
	state := fs.String("state", envOr("SOURCED_STATE", "/repos"),
		"where -mirror clones live (one directory per repo id)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(repos) == 0 && len(mirrors) == 0 {
		return fmt.Errorf("nothing to reflect: name a -repo or a -mirror")
	}

	ctx := context.Background()
	git := source.NewGit()
	for _, spec := range repos {
		id, dir, err := split(spec)
		if err != nil {
			return fmt.Errorf("-repo %q: %w", spec, err)
		}
		log.Printf("reflecting %s at %s", git.AddRepo(id, dir), dir)
	}
	for _, spec := range mirrors {
		id, remote, err := split(spec)
		if err != nil {
			return fmt.Errorf("-mirror %q: %w", spec, err)
		}
		if id == "" {
			id = source.MirrorID(remote)
		}
		// The clone happens here, at startup, and a failure ends the task: an unreachable
		// remote or a credential that is not in this pod is something to find out while
		// somebody is watching the rollout, not once a minute in a field of the registration.
		dir := filepath.Join(*state, id)
		if _, err := git.AddMirror(ctx, id, remote, dir); err != nil {
			return err
		}
		log.Printf("reflecting %s, mirrored from %s into %s", id, remote, dir)
	}
	// No delivery routes: git's hint is a git hook (source.Git.InstallHook), which posts to
	// verse directly. There is nothing for this program to receive — which is the shape every
	// source would have if every external system let you install a four-line shell script.
	if err := git.SetRoot(c.root); err != nil {
		return err
	}
	return c.serve(git.Root(), git, nil)
}

// split reads an optional `<id>=` prefix off a spec, leaving the rest whole — a url has its own
// `=` in a query string, and an id may not contain one.
func split(spec string) (id, rest string, err error) {
	if i := strings.Index(spec, "="); i >= 0 {
		id, rest = spec[:i], spec[i+1:]
	} else {
		rest = spec
	}
	if rest == "" {
		return "", "", fmt.Errorf("want [<id>=]<value>")
	}
	return id, rest, nil
}
