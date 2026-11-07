package delivery

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	verseio "github.com/signadot/verse/pkg/io"
	"github.com/signadot/verse/pkg/io/slack"
	"github.com/signadot/verse/pkg/task"
)

// SLACK IS A SYSTEM WITH FOUR VERBS, each a call the runner makes with its bot token over the
// task's channel: `slack.post` and `slack.reply` write, `slack.react` marks a message, and
// `slack.read` brings a channel's or a thread's messages into the task. A read is a step too: what
// a channel holds is not the task's until it is read, and an autonomy may keep a channel from it.
//
// EVERY MESSAGE THE TASK WRITES SAYS WHOSE IT IS. The bot is the runner's, and every task the
// runner runs posts as it; the footer names the task, so a person reading the channel knows which
// work said it, and the iod's reflection of the message carries the task's id in its text, which
// is what ties the entity back to the task that made it. A reaction has no text to carry one.
const (
	OpSlackPost  = "slack.post"
	OpSlackReply = "slack.reply"
	OpSlackRead  = "slack.read"
	OpSlackReact = "slack.react"
)

// How many messages a read answers with when it does not say, and at most.
const (
	SlackReadDefault = 20
	SlackReadMost    = 100
)

// SlackFooter is the line a task's message ends with: the task's id, in Slack's italics.
func SlackFooter(taskID string) string { return "_verse task " + taskID + "_" }

// slack answers a `slack.*` op with the runner's bot token: the channel by its name, decided by
// the launch's autonomy and the runner's ceiling on the name, then the call.
func (s *Server) slack(ctx context.Context, tk Task, req Request) (asked, Reply) {
	// The token is read here, as the request is, and the act is performed with what was read.
	token, err := s.Services.SlackToken.read()
	switch {
	case err != nil:
		return before(Reply{Error: "this runner cannot act in Slack: " + err.Error()})
	case token == "":
		return before(Reply{Error: "this runner has no Slack bot token (SLACK_BOT_TOKEN), so it cannot act in Slack"})
	}
	channel, ok := slack.ChannelName(req.Field("channel"))
	if !ok {
		return before(Reply{Refused: fmt.Sprintf("%q is not a channel's name: name it as Slack shows it, without the #", req.Field("channel"))})
	}
	fields := map[string]string{"channel": channel}
	emoji := strings.Trim(strings.TrimSpace(req.Field("emoji")), ":")
	switch req.Op {
	case OpSlackPost, OpSlackReply:
		if strings.TrimSpace(req.Body) == "" {
			return before(Reply{Refused: "a message needs text"})
		}
		switch {
		case req.Op == OpSlackReply && req.Field("thread") == "":
			return before(Reply{Refused: "a reply names the thread it is in, by the ts of its first message"})
		case req.Op == OpSlackReply:
			fields["thread"] = req.Field("thread")
		case req.Field("thread") != "":
			// The autonomy decides a post by its channel and a reply by its thread too: a post
			// naming a thread is a reply the autonomy was not asked about.
			return before(Reply{Refused: "a post names no thread: a message in a thread is a reply"})
		}
	case OpSlackRead:
		fields["thread"] = req.Field("thread")
	case OpSlackReact:
		if req.Field("message") == "" || emoji == "" {
			return before(Reply{Refused: "a reaction names the message, by its ts, and the emoji"})
		}
		fields["emoji"] = emoji
	}
	return asking(task.NewStep(req.Op, fields), func() Reply {
		base, hc := s.Services.SlackAPI, s.http()
		var out string
		switch req.Op {
		case OpSlackPost, OpSlackReply:
			p, err := slack.Post(ctx, token, base, hc, channel, req.Field("thread"), req.Body+"\n\n"+SlackFooter(tk.ID))
			if err != nil {
				return slackFailed(err)
			}
			where := "posted in #" + p.Channel.Name
			if req.Field("thread") != "" {
				where = "replied in #" + p.Channel.Name + ", in the thread " + req.Field("thread")
			}
			out = fmt.Sprintf("%s: ts %s %s", where, p.TS, p.Permalink)
		case OpSlackRead:
			limit := req.Int("limit")
			if limit <= 0 {
				limit = SlackReadDefault
			}
			limit = min(limit, SlackReadMost)
			c, msgs, err := slack.Read(ctx, token, base, hc, channel, req.Field("thread"), req.Field("since"), limit)
			if err != nil {
				return slackFailed(err)
			}
			return Reply{OK: true, Out: readOut(c, req.Field("thread"), req.Field("since"), msgs)}
		case OpSlackReact:
			c, err := slack.React(ctx, token, base, hc, channel, req.Field("message"), emoji)
			if err != nil {
				return slackFailed(err)
			}
			out = fmt.Sprintf("reacted :%s: to %s in #%s", emoji, req.Field("message"), c.Name)
		}
		fmt.Fprintf(s.Log, "delivery: %s: %s\n", tk.ID, out)
		return Reply{OK: true, Out: out}
	})
}

// slackFailed is a failed call as the task reads it: Slack's refusal, which asking again the same
// way gets again, as a refusal to re-plan on, and anything else as the error it is.
func slackFailed(err error) Reply {
	if verseio.IsPermanent(err) {
		return Reply{Refused: err.Error()}
	}
	return Reply{Error: err.Error()}
}

// readOut is a read's messages as the task reads them, one to a line, newest last: each its ts,
// its author and its text, and a thread's replies and the reactions where there are any.
func readOut(c slack.Channel, thread, since string, msgs []slack.Message) string {
	var b strings.Builder
	where := "#" + c.Name
	if thread != "" {
		where += ", the thread " + thread
	}
	fmt.Fprintf(&b, "%s: %d message(s)", where, len(msgs))
	if since != "" {
		fmt.Fprintf(&b, " after %s", since)
	}
	b.WriteString(", newest last")
	for _, m := range msgs {
		who := m.User
		if m.Bot != "" || who == "" {
			who = "bot " + m.Bot
		}
		fmt.Fprintf(&b, "\n%s %s: %s", m.TS, who, m.Text)
		if m.Replies > 0 {
			fmt.Fprintf(&b, " [%d replies]", m.Replies)
		}
		if len(m.Reactions) > 0 {
			var rs []string
			for _, name := range slices.Sorted(maps.Keys(m.Reactions)) {
				rs = append(rs, fmt.Sprintf(":%s: %d", name, m.Reactions[name]))
			}
			fmt.Fprintf(&b, " [%s]", strings.Join(rs, ", "))
		}
	}
	return b.String()
}
