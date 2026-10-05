package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/io/github"
)

// The git source, served rather than run in task.
//
// verse can already reflect a local repository with `verse io add --dir`, and that path is
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
	var trees strs
	fs.Var(&trees, "tree", "a directory whose tree sha is reflected on every ref, as [<name>=]<path> "+
		"(repeatable): tree.<name> is the identity of its contents at that ref; the name defaults to "+
		"the path's last segment")
	out := fs.Bool("out", false,
		"also act back on each input written under git.<repo>.out, or in an outbox a package "+
			"declares for it: {to: tag.<name>, sha} makes a tag, {to: branch.<name>, sha} moves a "+
			"branch, {to: install.<address>, tree} installs a tree of charters, each answered on "+
			"<name>~status; a mirror pushes with the credential it fetches with")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(repos) == 0 && len(mirrors) == 0 {
		return fmt.Errorf("nothing to reflect: name a -repo or a -mirror")
	}

	ctx := context.Background()
	// AS AN APP, where the environment names one (GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID and
	// GITHUB_APP_PRIVATE_KEY, as the github io takes them): the app's installation token is the
	// only credential git presents, so a fetch and a push are the app's, and a branch only the
	// app may push to (verse/live) is one this io can move. Before the first clone, which is the
	// first thing that asks git for a credential.
	app, err := githubApp()
	if err != nil {
		return err
	}
	if app != nil {
		tok, err := github.NewAppToken(ctx, *app, envOr("GITHUB_API_BASE", ""), nil)
		if err != nil {
			return err
		}
		if err := verseio.UseGitToken(ctx, tok.Token, filepath.Join(*state, ".credential"), time.Minute); err != nil {
			return err
		}
		log.Printf("git presents app %s's installation %s", app.AppID, app.InstallationID)
	}
	git := verseio.NewGit()
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
			id = verseio.MirrorID(remote)
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
	for _, t := range trees {
		if err := git.AddTree(t); err != nil {
			return err
		}
	}
	// No delivery routes: git's hint is a git hook (verseio.Git.InstallHook), which posts to
	// verse directly. There is nothing for this program to receive — which is the shape every
	// source would have if every external system let you install a four-line shell script.
	if err := git.SetRoot(c.root); err != nil {
		return err
	}
	// The out half dials the verse, where the reflection is dialled BY it: one pass loop per
	// repository, writing refs where this program reads them.
	if *out {
		sink := git.Sink()
		// An install input installs a tree of charters into the verse, as this io
		// (io/gitinstall.go): the one client proves it at the door and makes the request.
		sink.Verse = c.outClient(git.Root())
		if _, err := c.outHalf(ctx, git.Root(), sink, git.OutRefs(), sink.Verse); err != nil {
			return err
		}
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
