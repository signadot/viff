package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"github.com/signadot/verse/source/slack"
)

// The Slack source: a bot token, the channels the bot is in, and an events endpoint it owns.
//
// The venue is a Slack app's bot user. Everything this needs is on one — `channels:read` and
// `groups:read` to list what it is in, `channels:history` and `groups:history` to read the
// window, `users:read` for the people (optional; see -users) — and the app's signing secret is
// what makes the events endpoint safe to expose. No user token, no admin scope, no workspace
// installation beyond adding the app and inviting it to the channels you want reflected.
func slackMain(args []string) error {
	fs := flag.NewFlagSet("slack", flag.ExitOnError)
	var c common
	c.bind(fs)
	workspace := fs.String("workspace", "",
		"the id prefix every entity carries (default: the team name the token belongs to)")
	var channels strs
	fs.Var(&channels, "channel", "narrow to this channel name or id (repeatable; "+
		"default: every channel the bot is in)")
	depth := fs.Int("depth", slack.DefaultDepth, "how many recent messages per channel to reflect")
	threads := fs.Bool("threads", true, "also reflect the replies of threaded messages in the window")
	users := fs.Bool("users", true, "reflect the workspace's people as slack:user:* (needs users:read)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	src, err := slack.New(ctx, slack.Config{
		Token:     secret("SLACK_BOT_TOKEN"),
		Workspace: *workspace,
		Depth:     *depth,
		Threads:   *threads,
		Users:     *users,
		Channels:  channels,
		Log:       log.Printf,
	})
	if err != nil {
		return err
	}
	log.Printf("reflecting Slack workspace %s", src.Workspace())

	// The events endpoint is the source's own, because the signing secret is. What crosses to
	// verse is which channel went stale and nothing else — the same thing the git hook sends,
	// and the reason verse never learns to parse a Slack envelope.
	signing := secret("SLACK_SIGNING_SECRET")
	if signing == "" {
		log.Printf("no SLACK_SIGNING_SECRET: the events endpoint will refuse deliveries, and " +
			"only verse's periodic resync will converge")
	}
	routes := map[string]http.Handler{
		"POST /slack/events": src.Events(signing, c.hinter()),
	}
	return c.serve(slack.System, src, routes)
}
