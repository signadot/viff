package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"strings"

	"github.com/signadot/verse/source/github"
)

// The GitHub source: a token, a set of repositories, and a webhook it owns.
//
// GITHUB_TOKEN is a fine-grained personal access token with read access to the repositories'
// metadata, CONTENTS, issues, pull requests and Actions — which is what a dogfood deployment
// wants: it is
// issued from Settings → Developer settings, it carries its owner's visibility, and it needs no
// app registration or private key. A GitHub App is what an org-scale install needs, for the
// higher ceiling and for not being tied to a person, and the measurement in
// git-issue 3eskdzrq says when: a personal token covers about forty repositories
// with discussions on, and effectively unbounded with `-discussions=false`.
func githubMain(args []string) error {
	fs := flag.NewFlagSet("github", flag.ExitOnError)
	var c common
	c.bind(fs)
	var repos strs
	fs.Var(&repos, "repo", "a repository to reflect, as <org>/<name> (repeatable, comma-separated)")
	settled := fs.Int("settled", github.DefaultSettled,
		"closed issues, pull requests and discussions kept per repository")
	runs := fs.Int("runs", github.DefaultRuns, "workflow runs kept per repository")
	comments := fs.Int("comments", github.DefaultComments,
		"comments kept per repository, most recently updated first, across issues and pull "+
			"requests together")
	maxOpen := fs.Int("max-open", github.DefaultMaxOpen,
		"refuse a repository with more open items than this, rather than answer partially "+
			"for a slice the answer claims to cover completely")
	discussions := fs.Bool("discussions", false,
		"reflect discussions. Off by default: they are GraphQL, so they carry no ETag and are "+
			"the whole of what a quiet repository costs against the rate limit, and they are "+
			"opt-in per repository on GitHub's side so most repositories have none. On, a "+
			"repository that does have them costs two requests a pass and one that does not "+
			"still costs nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	src, err := github.New(ctx, github.Config{
		Token:           secret("GITHUB_TOKEN"),
		Repos:           repos,
		System:          c.system,
		Settled:         *settled,
		Runs:            *runs,
		Comments:        *comments,
		MaxOpen:         *maxOpen,
		SkipDiscussions: !*discussions,
		Base:            envOr("GITHUB_API_BASE", ""),
		Log:             log.Printf,
	})
	if err != nil {
		return err
	}
	log.Printf("reflecting GitHub repositories %s into %s:*",
		strings.Join(src.Repos(), ", "), src.System())

	// The webhook is the source's own, because the signing secret is. Configure it under the
	// repository's Settings → Webhooks, pointed at <this source>/github/webhook, with the
	// content type application/json.
	signing := secret("GITHUB_WEBHOOK_SECRET")
	if signing == "" {
		log.Printf("no GITHUB_WEBHOOK_SECRET: the webhook will refuse deliveries, and only " +
			"verse's periodic resync will converge")
	}
	routes := map[string]http.Handler{
		"POST /github/webhook": src.Webhook(signing, c.hinter()),
	}
	return c.serve(src.System(), src, routes)
}
