package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"strings"

	"go.viff.dev/viff/pkg/io/linear"
)

// The Linear source: a credential, a set of teams, and a webhook it owns.
//
// LINEAR_API_KEY is the Authorization header verbatim, which is how one variable carries both
// of Linear's mechanisms — a personal API key (`lin_api_…`) goes bare, an OAuth access token
// as `Bearer lin_oauth_…`. A personal key is what a dogfood deployment wants: it is issued
// from Settings → API, it carries its owner's visibility, and it needs no app registration or
// redirect url. OAuth is what a multi-workspace install would need, and this source does not
// have to know which it was given.
func linearMain(args []string) error {
	fs := flag.NewFlagSet("linear", flag.ExitOnError)
	var c common
	c.bind(fs)
	var teams strs
	fs.Var(&teams, "team", "a team key to reflect, e.g. ENG (repeatable; "+
		"default: every team the credential can see)")
	pageSize := fs.Int("page-size", linear.DefaultPageSize, "issues per GraphQL page")
	max := fs.Int("max-issues", linear.DefaultMaxIssues,
		"refuse to reflect a team with more issues than this, rather than answer partially "+
			"for a slice the answer claims to cover completely")
	out := fs.Bool("out", false, "also run the out half: comment on the issue each input under "+
		"linear.<team>.out names in its `to` (ENG-42), and write each one's <name>~status")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	src, err := linear.New(ctx, linear.Config{
		Root:      c.root,
		Auth:      secret("LINEAR_API_KEY"),
		Teams:     teams,
		PageSize:  *pageSize,
		MaxIssues: *max,
		Log:       log.Printf,
	})
	if err != nil {
		return err
	}
	log.Printf("reflecting Linear teams %s", strings.Join(src.Teams(), ", "))

	// The webhook is the source's own, because the signing secret is. Configure it in Linear
	// under Settings → API → Webhooks, pointed at <this source>/linear/webhook.
	signing := secret("LINEAR_WEBHOOK_SECRET")
	if signing == "" {
		log.Printf("no LINEAR_WEBHOOK_SECRET: the webhook will refuse deliveries, and only " +
			"viff's periodic resync will converge")
	}
	routes := map[string]http.Handler{
		"POST /linear/webhook": src.Webhook(signing, c.hinter()),
	}

	// The out half dials the viff instance, where the reflection is dialled BY it: one pass loop per
	// team's place, all of them the same sink, since an issue identifier says which team it is.
	if *out {
		if _, err := c.outHalf(ctx, src.Root(), src.Sink(), src.OutRefs(), nil); err != nil {
			return err
		}
	}
	return c.serve(src.Root(), src, routes)
}
