package slack

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	viffio "go.viff.dev/viff/pkg/io"
)

// Posting, replying, reading and reacting, for a runner's `slack-*` tools: the Slack calls a
// task makes through its runner, which are not the reflection's or the out half's. They live
// here because the Web API's envelope does (api.go), and a second client would be a second
// place for a refusal that arrives as a 200 to be read as an answer.
//
// A CHANNEL IS NAMED BY ITS NAME, and only among the channels the bot is in. The name is what a
// policy's rows match, so the name a call is decided by and the channel it acts on have to be
// one thing: a call that named a channel by its id would be decided as one channel and act on
// another. So the id is never taken, and the bot's membership is the list a name is looked up
// in, as it is the reflection's (conversations).

// ChannelName is a channel's name as a call names it and a policy's row matches it: lowercase
// and without the `#`. False for what cannot be a channel's name: Slack's are lowercase letters,
// digits, `-` and `_`, at most 80 of them.
func ChannelName(s string) (string, bool) {
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	if name == "" || len(name) > 80 {
		return "", false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return "", false
		}
	}
	return name, true
}

// Channel is a channel the bot is in: its id, which the API takes, and its name, which a call
// gave.
type Channel struct {
	ID, Name string
}

// channelNamed is the channel the bot is in that has this name. A name the bot is in no channel
// by is refused (viffio.Permanent): from here a typo, a channel the bot was never invited to
// and one it was removed from are the same fact.
func (a *api) channelNamed(ctx context.Context, given string) (Channel, error) {
	name, ok := ChannelName(given)
	if !ok {
		return Channel{}, viffio.Permanent(fmt.Errorf("slack: %q is not a channel's name", given))
	}
	all, err := a.conversations(ctx)
	if err != nil {
		return Channel{}, err
	}
	for _, c := range all {
		if c.Name == name {
			return Channel{ID: c.ID, Name: c.Name}, nil
		}
	}
	return Channel{}, viffio.Permanent(fmt.Errorf("slack: the bot is in no channel named %s; invite it there first", name))
}

// Posted is a message the bot said: where, its ts, which a reply and a reaction name, and its
// link.
type Posted struct {
	Channel   Channel
	TS        string
	Permalink string
}

// Post says text in the channel the bot is in by the name channel (`chat:write`), in the thread
// whose first message is at thread when thread is not empty, with the bot token token. base is
// the Web API, "" for Slack's own; hc nil is a default client. The link is asked for after,
// and a message that has none still landed.
func Post(ctx context.Context, token, base string, hc *http.Client, channel, thread, text string) (Posted, error) {
	a := newAPI(token, base, hc)
	if strings.TrimSpace(text) == "" {
		return Posted{}, viffio.Permanent(fmt.Errorf("slack: a message needs text"))
	}
	c, err := a.channelNamed(ctx, channel)
	if err != nil {
		return Posted{}, err
	}
	_, ts, err := a.postMessage(ctx, c.ID, thread, text)
	if err != nil {
		return Posted{}, err
	}
	link, _ := a.permalink(ctx, c.ID, ts)
	return Posted{Channel: c, TS: ts, Permalink: link}, nil
}

// Message is one message as a read answers it.
type Message struct {
	TS   string
	User string // the author's id, U…; "" for a bot's
	Bot  string // a bot's name, for a message a bot said
	Text string
	// Replies is how many replies the message's thread has, where it starts one.
	Replies   int
	Reactions map[string]int
}

// Read is the channel's recent messages, or the thread's under thread, newest last
// (`channels:history`, `groups:history`): at most limit of them, and only those after since
// when since is a ts. A thread's first message comes first, unless since is past it.
func Read(ctx context.Context, token, base string, hc *http.Client, channel, thread, since string, limit int) (Channel, []Message, error) {
	a := newAPI(token, base, hc)
	c, err := a.channelNamed(ctx, channel)
	if err != nil {
		return Channel{}, nil, err
	}
	q := url.Values{"channel": {c.ID}, "limit": {strconv.Itoa(limit)}}
	if since != "" {
		q.Set("oldest", since)
	}
	method := "conversations.history"
	if thread != "" {
		method = "conversations.replies"
		q.Set("ts", thread)
	}
	var r historyResp
	if err := a.call(ctx, method, q, &r); err != nil {
		return c, nil, err
	}
	msgs := r.Messages
	if thread == "" {
		slices.Reverse(msgs) // history is newest first
	}
	var out []Message
	for _, m := range msgs {
		if since != "" && !tsAfter(m.TS, since) {
			continue
		}
		mm := Message{TS: m.TS, User: m.User, Text: m.Text, Replies: m.ReplyCount}
		if m.BotID != "" {
			mm.User, mm.Bot = "", m.Username
		}
		for _, re := range m.Reactions {
			if mm.Reactions == nil {
				mm.Reactions = map[string]int{}
			}
			mm.Reactions[re.Name] = re.Count
		}
		out = append(out, mm)
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return c, out, nil
}

// tsAfter reports whether the ts a is after b: seconds, then the fraction, as numbers.
func tsAfter(a, b string) bool {
	as, af, _ := strings.Cut(a, ".")
	bs, bf, _ := strings.Cut(b, ".")
	if as != bs {
		ai, _ := strconv.ParseInt(as, 10, 64)
		bi, _ := strconv.ParseInt(bs, 10, 64)
		return ai > bi
	}
	ai, _ := strconv.ParseInt(af, 10, 64)
	bi, _ := strconv.ParseInt(bf, 10, 64)
	return ai > bi
}

// React adds the reaction emoji to the message at ts in the channel the bot is in by the name
// channel (`reactions:write`). A reaction the bot already gave is no refusal: the message
// carries it, which is what was asked.
func React(ctx context.Context, token, base string, hc *http.Client, channel, ts, emoji string) (Channel, error) {
	a := newAPI(token, base, hc)
	c, err := a.channelNamed(ctx, channel)
	if err != nil {
		return Channel{}, err
	}
	var r envelope
	err = a.post(ctx, "reactions.add", map[string]any{"channel": c.ID, "timestamp": ts, "name": strings.Trim(emoji, ":")}, &r)
	if err != nil && r.Error == "already_reacted" {
		err = nil
	}
	return c, err
}
