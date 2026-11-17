package main

import (
	"context"
	"errors"
	"flag"
	"github.com/signadot/verse/pkg/entity"
	verseio "github.com/signadot/verse/pkg/io"
	"log"
	"net/http"
	"strings"

	"github.com/signadot/verse/pkg/io/github"
)

// The GitHub source: a token, a set of repositories, and a webhook it owns.
//
// GITHUB_TOKEN is a fine-grained personal access token with read access to the repositories'
// metadata, CONTENTS, issues, pull requests and Actions (and Dependabot alerts, with `-alerts`) —
// which is what a dogfood deployment wants: it is
// issued from Settings → Developer settings, it carries its owner's visibility, and it needs no
// app registration or private key. A GitHub App is what an org-scale install needs, for the
// higher ceiling and for not being tied to a person, and the measurement in
// git-issue 3eskdzrq says when: a personal token covers about forty repositories
// with discussions on, and effectively unbounded with `-discussions=false`.
//
// OR AN APP: GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID and GITHUB_APP_PRIVATE_KEY (the PEM) make
// the io authenticate as the app's installation, and what the out half does is then the app's
// rather than a person's (git-issue n69fce9dh12krpecqsn0). Given both, the app is used: a
// deployment adds the app's keys to its secret first, and drops GITHUB_TOKEN once the build that
// reads them runs.
func githubMain(args []string) error {
	fs := flag.NewFlagSet("github", flag.ExitOnError)
	var c common
	c.bind(fs)
	var repos strs
	fs.Var(&repos, "repo", "a repository to reflect, as <org>/<name> (repeatable, comma-separated)")
	settled := fs.Int("settled", github.DefaultSettled,
		"closed issues, pull requests and discussions, and settled Dependabot alerts, kept per "+
			"repository")
	runs := fs.Int("runs", github.DefaultRuns, "workflow runs kept per repository")
	comments := fs.Int("comments", github.DefaultComments,
		"comments kept per repository, most recently updated first, across issues and pull "+
			"requests together")
	maxOpen := fs.Int("max-open", github.DefaultMaxOpen,
		"refuse a repository with more open items than this, rather than answer partially "+
			"for a slice the answer claims to cover completely")
	out := fs.Bool("out", false,
		"also act back on each input written under github.<org>.<name>.out, or in an outbox a "+
			"package declares for it: comment, open or adopt a pull request, merge one at a sha, "+
			"review one, or close an issue or pull request. The token then needs write access to "+
			"issues and pull requests, and to contents to merge")
	discussions := fs.Bool("discussions", false,
		"reflect discussions. Off by default: they are GraphQL, so they carry no ETag and are "+
			"the whole of what a quiet repository costs against the rate limit, and they are "+
			"opt-in per repository on GitHub's side so most repositories have none. On, a "+
			"repository that does have them costs two requests a pass and one that does not "+
			"still costs nothing")
	alerts := fs.Bool("alerts", false,
		"reflect Dependabot alerts: the open ones, and the newest -settled fixed or dismissed. "+
			"The token then needs Dependabot alerts read; a repository that refuses the read "+
			"keeps what was reflected")
	var trees strs
	fs.Var(&trees, "tree", "a directory an open pull request reports when it changes it, as "+
		"[<name>=]<path> (repeatable): changes.<name> is the identity of its contents at the head, "+
		"where they differ from the head's merge base; the name defaults to the path's last segment")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var parsedTrees []github.Tree
	for _, spec := range trees {
		t, err := github.ParseTree(spec)
		if err != nil {
			return err
		}
		parsedTrees = append(parsedTrees, t)
	}

	app, err := githubApp()
	if err != nil {
		return err
	}
	token := secret("GITHUB_TOKEN")
	if app != nil {
		log.Printf("github: authenticating as app %s, installation %s", app.AppID, app.InstallationID)
		if token != "" {
			log.Printf("github: GITHUB_TOKEN is set too, and ignored: the app is used")
			token = ""
		}
	}
	ctx := context.Background()
	src, err := github.New(ctx, github.Config{
		Token:           token,
		App:             app,
		Repos:           repos,
		Root:            c.root,
		Settled:         *settled,
		Runs:            *runs,
		Comments:        *comments,
		MaxOpen:         *maxOpen,
		SkipDiscussions: !*discussions,
		Alerts:          *alerts,
		Trees:           parsedTrees,
		Base:            envOr("GITHUB_API_BASE", ""),
		Log:             log.Printf,
	})
	if err != nil {
		return err
	}
	log.Printf("reflecting GitHub repositories %s into %s:*",
		strings.Join(src.Repos(), ", "), src.Root())

	// The webhook is the source's own, because the signing secret is. Configure it under the
	// repository's Settings → Webhooks, pointed at <this source>/github/webhook, with the
	// content type application/json.
	signing := secret("GITHUB_WEBHOOK_SECRET")
	if signing == "" {
		log.Printf("no GITHUB_WEBHOOK_SECRET: the webhook will refuse deliveries, and only " +
			"verse's periodic resync will converge")
	}
	hint := c.hinter()

	// The out half dials the verse, where the reflection is dialled BY it: one pass loop per
	// repository's place, all of them the same sink, which reads the repository off the place.
	if *out {
		sink := src.Sink()
		// The questions a comment is about are read as this io, and a `/verse` comment answers
		// as its author, relayed with their GitHub account beside this io's own credential
		// (io.Controller's shape, for a pull rather than a socket).
		sink.Verse = c.outClient(src.Root())
		sink.As = verseio.As(c.verse(), c.bearer())
		outs, err := c.outHalf(ctx, src.Root(), sink, src.OutRefs(), sink.Verse)
		if err != nil {
			return err
		}
		// A comment's delivery is a reply in the making: the hint has verse reflect it, and a
		// pass asked for now reads the reflection a moment behind the hint. The pass that misses
		// it is caught by the next interval, which is the net either way.
		inner := hint
		hint = func(ctx context.Context, refs ...entity.Ref) {
			inner(ctx, refs...)
			if aboutAComment(refs) {
				for _, o := range outs {
					o.Kick()
				}
			}
		}
	}
	routes := map[string]http.Handler{
		"POST /github/webhook": src.Webhook(signing, hint),
	}
	return c.serve(src.Root(), src, routes)
}

// aboutAComment reports whether a hint names a comment: `<root>.<org>.<name>.comment.<n>.<id>`.
func aboutAComment(refs []entity.Ref) bool {
	for _, ref := range refs {
		if segs := ref.Segs(); len(segs) == 6 && segs[3] == "comment" {
			return true
		}
	}
	return false
}

// githubApp is the app the environment names, nil where it names none. Partly named is refused:
// a deployment that set two of the three meant the app, and falling back to the token would hide
// that the third is missing.
func githubApp() (*github.AppAuth, error) {
	a := github.AppAuth{
		AppID:          secret("GITHUB_APP_ID"),
		InstallationID: secret("GITHUB_APP_INSTALLATION_ID"),
		PrivateKey:     []byte(secret("GITHUB_APP_PRIVATE_KEY")),
	}
	set := 0
	for _, v := range []string{a.AppID, a.InstallationID, string(a.PrivateKey)} {
		if v != "" {
			set++
		}
	}
	switch set {
	case 0:
		return nil, nil
	case 3:
		return &a, nil
	}
	return nil, errors.New("github: GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID and GITHUB_APP_PRIVATE_KEY " +
		"name an app together, and only some are set")
}
