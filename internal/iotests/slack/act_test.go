package slack_test

import (
	"context"
	"strings"
	"testing"

	verseio "github.com/signadot/verse/pkg/io"
	"github.com/signadot/verse/pkg/io/slack"
)

// A CHANNEL IS NAMED BY ITS NAME: lowercase, without the `#`, and only what Slack's names can be.
func TestAChannelIsNamedByItsName(t *testing.T) {
	for in, want := range map[string]string{"deploys": "deploys", "#Deploys": "deploys", " eng_ops-2 ": "eng_ops-2"} {
		if got, ok := slack.ChannelName(in); !ok || got != want {
			t.Errorf("%q names %q (%v), want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "#", "two words", "dé", "a/b", strings.Repeat("x", 81)} {
		if got, ok := slack.ChannelName(bad); ok {
			t.Errorf("%q names %q", bad, got)
		}
	}
}

// A POST LANDS IN THE CHANNEL THE BOT IS IN BY THAT NAME, and the answer is its ts and link. A name
// the bot is in no channel by is refused, as is a channel's id given as its name: the name is
// what a policy decided, so it is the only thing a call is looked up by.
func TestAPostLandsInTheChannelNamed(t *testing.T) {
	f := newFake()
	f.channel("C0193", "deploys")
	base := f.serve(t)
	ctx := context.Background()
	p, err := slack.Post(ctx, "test-token", base, nil, "#deploys", "", "v1.3 is out")
	if err != nil {
		t.Fatal(err)
	}
	if p.Channel != (slack.Channel{ID: "C0193", Name: "deploys"}) || p.TS == "" || !strings.Contains(p.Permalink, "/archives/C0193/") {
		t.Errorf("posted %+v", p)
	}
	if len(f.posted) != 1 || f.posted[0]["channel"] != "C0193" || f.posted[0]["text"] != "v1.3 is out" {
		t.Errorf("Slack saw %v", f.posted)
	}
	if _, err := slack.Post(ctx, "test-token", base, nil, "deploys", p.TS, "and it holds"); err != nil || f.saidTo("thread", p.TS) != "and it holds" {
		t.Errorf("a reply: %v, the thread holds %q", err, f.saidTo("thread", p.TS))
	}
	for _, other := range []string{"general", "C0193"} {
		if _, err := slack.Post(ctx, "test-token", base, nil, other, "", "x"); err == nil || !verseio.IsPermanent(err) || !strings.Contains(err.Error(), "no channel named") {
			t.Errorf("a post to %s: %v, want a refusal naming the channel", other, err)
		}
	}
	if _, err := slack.Post(ctx, "test-token", base, nil, "deploys", "", " "); err == nil {
		t.Error("a post with no text was sent")
	}
}

// A READ IS THE CHANNEL'S MESSAGES NEWEST LAST, or a thread's, and only those after `since`: the
// author's id, or a bot's name, the text, and a thread's replies and reactions.
func TestAReadIsNewestLast(t *testing.T) {
	f := newFake()
	f.channel("C0193", "deploys")
	// conversations.history answers newest first.
	f.history["C0193"] = []map[string]any{
		{"ts": "1700000003.000001", "bot_id": "B1", "username": "ci", "text": "green"},
		{"ts": "1700000002.000001", "user": "U2", "text": "deploying", "reply_count": 2,
			"reactions": []map[string]any{{"name": "eyes", "count": 1}}},
		{"ts": "1700000001.000001", "user": "U1", "text": "morning"},
	}
	f.replies["C0193/1700000002.000001"] = []map[string]any{
		{"ts": "1700000002.000001", "user": "U2", "text": "deploying"},
		{"ts": "1700000002.000500", "user": "U1", "text": "go"},
	}
	base := f.serve(t)
	ctx := context.Background()
	c, msgs, err := slack.Read(ctx, "test-token", base, nil, "deploys", "", "", 20)
	if err != nil || c.ID != "C0193" || len(msgs) != 3 {
		t.Fatalf("read %v %+v %v", c, msgs, err)
	}
	if msgs[0].Text != "morning" || msgs[2].Bot != "ci" || msgs[2].User != "" || msgs[1].Replies != 2 || msgs[1].Reactions["eyes"] != 1 {
		t.Errorf("read %+v", msgs)
	}
	if _, msgs, _ := slack.Read(ctx, "test-token", base, nil, "deploys", "", "1700000001.000001", 20); len(msgs) != 2 || msgs[0].Text != "deploying" {
		t.Errorf("since the first: %+v", msgs)
	}
	if _, msgs, _ := slack.Read(ctx, "test-token", base, nil, "deploys", "", "", 1); len(msgs) != 1 || msgs[0].Text != "green" {
		t.Errorf("a limit of one: %+v", msgs)
	}
	if _, msgs, _ := slack.Read(ctx, "test-token", base, nil, "deploys", "1700000002.000001", "", 20); len(msgs) != 2 || msgs[0].Text != "deploying" || msgs[1].Text != "go" {
		t.Errorf("the thread: %+v", msgs)
	}
}

// A REACTION IS ADDED ONCE, and one the bot already gave is no refusal.
func TestAReactionIsAdded(t *testing.T) {
	f := newFake()
	f.channel("C0193", "deploys")
	base := f.serve(t)
	for range 2 {
		if _, err := slack.React(context.Background(), "test-token", base, nil, "deploys", "1700000002.000001", ":rocket:"); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.reacted) != 1 || f.reacted[0] != "C0193 1700000002.000001 rocket" {
		t.Errorf("Slack saw %v", f.reacted)
	}
}
