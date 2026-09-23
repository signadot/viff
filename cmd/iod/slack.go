package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"github.com/signadot/verse/client"
	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/io/slack"
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
	out := fs.Bool("out", false, "also run the out half: say in Slack what is written under "+
		"slack.<ws>.out, and write each one's <name>~status (needs chat:write, im:write); with "+
		"SLACK_APP_TOKEN, a reply in a message's thread answers the question it asks "+
		"(needs users:read.email and the message events)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	src, err := slack.New(ctx, slack.Config{
		Root:      c.root,
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
	signing, app := secret("SLACK_SIGNING_SECRET"), secret("SLACK_APP_TOKEN")
	if signing == "" && app == "" {
		log.Printf("no SLACK_SIGNING_SECRET and no SLACK_APP_TOKEN: no delivery reaches this, and " +
			"only verse's periodic resync will converge")
	}
	routes := map[string]http.Handler{
		"POST /slack/events": src.Events(signing, c.hinter()),
	}

	// The out half dials the verse, where the reflection is dialled BY it: it reads what is asked
	// and writes what became of it, as a principal of its own. Its address defaults to the one the
	// hint url already names, so a deployment says where verse is once.
	addr := c.verse()
	var sink *slack.Sink
	var o *verseio.Out
	if *out {
		sink = src.Sink(client.New(addr, c.principal(c.root)))
		outs, err := c.outHalf(ctx, src.Root(), sink, []entity.Ref{src.OutRef()})
		if err != nil {
			return err
		}
		o = outs[0]
	}

	// Socket Mode, which this dials out to, so no Ingress is needed: Slack's event deliveries
	// arrive down it as hints, and with -out so do presses of the buttons it said.
	switch {
	case app != "":
		sock := src.Socket(app)
		sock.Hint = c.hinter()
		if o != nil {
			sock.Asking, sock.Answered = sink.Asked, o.Kick
			log.Printf("slack socket: hearing events, and answering questions at %s as whoever replies in the thread", addr)
		} else {
			log.Printf("slack socket: hearing events")
		}
		// The control seam: a client factory rather than a client, so what it asks for is asked
		// as whoever Slack says did the thing and never as this program (io.Controller).
		go func() {
			if err := sock.Control(ctx, verseio.As(addr)); err != nil && ctx.Err() == nil {
				log.Printf("slack socket: %v", err)
			}
		}()
	case o != nil:
		log.Printf("no SLACK_APP_TOKEN: questions are asked in Slack, and nothing there can answer one")
	}
	return c.serve(src.Root(), src, routes)
}
