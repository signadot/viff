package slack_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	viffio "go.viff.dev/viff/pkg/io"
	"go.viff.dev/viff/pkg/io/slack"
)

func newSource(t *testing.T, f *fakeSlack, cfg slack.Config) *slack.Slack {
	t.Helper()
	cfg.Token = "test-token"
	cfg.Base = f.serve(t)
	src, err := slack.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("slack.New: %v", err)
	}
	return src
}

func payloads(rn viffio.Reflection) map[string]entity.Obj {
	out := map[string]entity.Obj{}
	for _, e := range rn.Entities {
		out[e.Ref.String()] = entity.ToObj(e.Payload)
	}
	return out
}

// The full pass: the channels the bot is in, each one's window, the threads hanging off it,
// and the people — with the workspace taken from the token rather than typed twice.
func TestSlackFullPassReflectsWhatAFlowReactsTo(t *testing.T) {
	f := newFake()
	f.channel("C1", "incidents")
	f.channel("C2", "releases")
	parent := f.say("C1", "1700000001.000100", "U1", "prod is down")
	parent["reply_count"] = 2
	parent["reactions"] = []map[string]any{{"name": "eyes", "count": 3}}
	f.replies["C1/1700000001.000100"] = []map[string]any{
		parent,
		{"type": "message", "ts": "1700000002.000100", "user": "U2",
			"text": "on it", "thread_ts": "1700000001.000100"},
	}
	f.say("C2", "1700000003.000100", "U1", "v2 is out")
	f.users = []map[string]any{{
		"id": "U1", "name": "ada", "real_name": "Ada L", "tz": "Europe/Paris",
		"profile": map[string]any{"email": "ada@example.com", "display_name": "ada"},
	}}

	src := newSource(t, f, slack.Config{Threads: true, Users: true})
	if src.Workspace() != "Acme" {
		t.Fatalf("workspace %q, want the team name from the token", src.Workspace())
	}
	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rn.Check(slack.Root); err != nil {
		t.Fatalf("the reflection is not safe to converge: %v", err)
	}

	got := payloads(rn)
	ch := got["slack.Acme.channel.C1"]
	if ch == nil || ch["name"] != "incidents" || ch["channel"] != "C1" {
		t.Fatalf("slack.Acme.channel.C1 = %+v", ch)
	}
	msg := got[`slack.Acme.message.C1."1700000001.000100"`]
	if msg == nil || msg["text"] != "prod is down" || msg["user"] != "U1" {
		t.Fatalf("the message = %+v", msg)
	}
	// The channel field is what a targeted pass's cover selects on, so every kind carries it.
	if msg["channel"] != "C1" || msg["workspace"] != "Acme" {
		t.Fatalf("a message must carry workspace and channel: %+v", msg)
	}
	// Reactions are an OBJECT, name → count, because a payload predicate is how a charter asks
	// about one and `{reactions: {eyes: null}}` is a presence test.
	rs, ok := msg["reactions"].(map[string]any)
	if !ok || rs["eyes"] == nil {
		t.Fatalf("reactions = %#v, want an object keyed by name", msg["reactions"])
	}
	if msg["url"] != "https://acme.slack.com/archives/C1/p1700000001000100" {
		t.Fatalf("permalink = %v", msg["url"])
	}
	reply := got[`slack.Acme.message.C1."1700000002.000100"`]
	if reply == nil || reply["thread"] != "1700000001.000100" {
		t.Fatalf("the thread reply = %+v", reply)
	}
	if u := got["slack.Acme.user.U1"]; u == nil || u["email"] != "ada@example.com" {
		t.Fatalf("slack.Acme.user.U1 = %+v", u)
	}

	// A full pass covers the workspace in all three kinds — a channel the bot was removed from
	// has to be tombstonable, which is what the workspace-wide cover is for.
	kinds := map[string]bool{}
	reach := map[string]entity.Ref{
		"channel": entity.RefAt(slack.Root, "Acme", "channel", "C9"),
		"message": entity.RefAt(slack.Root, "Acme", "message", "C9", "1700000009.000100"),
		"user":    entity.RefAt(slack.Root, "Acme", "user", "U9"),
	}
	for _, c := range rn.Covers {
		if top, ok := entity.PathLiteral(c.Path[0]); !ok || top != slack.Root {
			t.Fatalf("a cover over %v", c.Path)
		}
		// UNDER THE WORKSPACE, which is a level of the address rather than a field every
		// entity has to carry: a cover reaching another workspace's place would be a claim
		// over somebody else's slice.
		if ws, ok := entity.PathLiteral(c.Path[1]); !ok || ws != "Acme" {
			t.Fatalf("a cover not under the workspace: %v", c.Path)
		}
		for kind, ref := range reach {
			if c.MatchRef(ref) {
				kinds[kind] = true
			}
		}
	}
	for _, want := range []string{"channel", "message", "user"} {
		if !kinds[want] {
			t.Fatalf("no cover over %s: %+v", want, rn.Covers)
		}
	}
}

// A targeted pass — what a hint asks for — covers ONE channel, so it cannot tombstone another
// channel's window. This is the same scoping the git source's per-repo covers have.
func TestSlackATargetedPassCoversOnlyItsChannel(t *testing.T) {
	f := newFake()
	f.channel("C1", "incidents")
	f.channel("C2", "releases")
	f.say("C1", "1700000001.000100", "U1", "prod is down")
	f.say("C2", "1700000003.000100", "U1", "v2 is out")

	src := newSource(t, f, slack.Config{})
	rn, err := src.Reflect(context.Background(), slack.ChannelRef("Acme", "C1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rn.Entities {
		if !slices.Contains(e.Ref.Segs(), "C1") {
			t.Fatalf("a targeted pass carried %s", e.Ref)
		}
	}
	// SCOPED BY THE PATH, not by a predicate: a cover names the channel it is about, so what it
	// claims completeness for is visible in the address rather than in a field.
	for _, c := range rn.Covers {
		if lit, ok := entity.PathLiteral(c.Path[3]); !ok || lit != "C1" {
			t.Fatalf("a cover not scoped to the channel: %v", c.Path)
		}
	}
	// A ref in another workspace's slice claims nothing and carries nothing, rather than
	// erroring: it is somebody else's question.
	other, err := src.Reflect(context.Background(), slack.ChannelRef("Other", "C1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Covers) != 0 || len(other.Entities) != 0 {
		t.Fatalf("a ref outside this workspace answered %+v", other)
	}
}

// THE ONE THAT COSTS ENTITIES: Slack refuses with HTTP 200 and `ok: false`. Read as an answer,
// that is an empty reflection carrying covers — an instruction to tombstone the workspace.
func TestSlackARefusalIsAnErrorAndNotAnEmptyReflection(t *testing.T) {
	f := newFake()
	f.channel("C1", "incidents")
	src := newSource(t, f, slack.Config{})

	f.fail["users.conversations"] = "missing_scope"
	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err == nil {
		t.Fatal("a refusal answered as a reflection")
	}
	if !strings.Contains(err.Error(), "missing_scope") {
		t.Fatalf("error %v does not say what Slack said", err)
	}
	if len(rn.Covers) != 0 || len(rn.Entities) != 0 {
		t.Fatalf("a failed reflect answered %+v", rn)
	}
	// And a bad token fails at construction, not on the first pass.
	if _, err := slack.New(context.Background(), slack.Config{
		Token: "wrong", Base: f.serve(t),
	}); err == nil {
		t.Fatal("a bad token built a source")
	}
}

// The people are a SEPARABLE claim: a pass that cannot read them drops the cover and the
// entities rather than failing, so what it could not read is left standing instead of deleted.
// The channels are not treated this way — they are why this source exists.
func TestSlackKeepsTheChannelsWhenThePeopleCannotBeRead(t *testing.T) {
	f := newFake()
	f.channel("C1", "incidents")
	f.say("C1", "1700000001.000100", "U1", "prod is down")
	var logged []string
	src := newSource(t, f, slack.Config{
		Users: true,
		Log:   func(format string, a ...any) { logged = append(logged, format) },
	})

	f.fail["users.list"] = "missing_scope"
	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatalf("a users failure failed the whole pass: %v", err)
	}
	if got := payloads(rn)["slack.Acme.channel.C1"]; got == nil {
		t.Fatal("the channels went with the people")
	}
	for _, c := range rn.Covers {
		if c.MatchRef(entity.RefAt(slack.Root, "Acme", "user", "U1")) {
			t.Fatal("a user cover survived a users read that failed — whatever stands under it " +
				"would be tombstoned by an answer that could not read it")
		}
	}
	if len(logged) == 0 {
		t.Fatal("nothing was said about the scope that is missing")
	}
}

// The window is BOUNDED, and that is the deliberate difference from the git source's commits.
// A repo's history is finite and immutable, so git carries commits under no cover and they
// stay; a channel is an unbounded event stream, so the window is covered and what falls out of
// it is tombstoned. Driven through the Reflector, which is where converge lives.
func TestSlackTheWindowSlidesAndTombstonesWhatFellOutOfIt(t *testing.T) {
	ctx := context.Background()
	store := entitytest.Store(t)
	f := newFake()
	f.channel("C1", "incidents")
	f.say("C1", "1700000001.000100", "U1", "one")
	f.say("C1", "1700000002.000100", "U1", "two")

	src := newSource(t, f, slack.Config{Depth: 2})
	rf := viffio.NewReflector(src, store, 0)
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	held := func(ts string) bool {
		_, ok, _ := store.Get(entity.RefAt(slack.Root, "Acme", "message", "C1", ts))
		return ok
	}
	if !held("1700000001.000100") || !held("1700000002.000100") {
		t.Fatal("the window did not land")
	}

	// Two more arrive, and the fake's history answers newest-two-only — the window slid.
	f.history["C1"] = nil
	f.say("C1", "1700000003.000100", "U1", "three")
	f.say("C1", "1700000004.000100", "U1", "four")
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	if held("1700000001.000100") {
		t.Fatal("a message that fell out of the window is still standing — the message cover " +
			"is what bounds this slice, and without it a busy channel is a warehouse")
	}
	if !held("1700000004.000100") {
		t.Fatal("the newest message did not land")
	}

	// A second pass over an unchanged workspace writes NOTHING: converge skips what has not
	// changed, or every resync re-fires every trigger over the slice.
	var wrote int
	rf.OnPass(func(p viffio.Pass) { wrote = p.Wrote })
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	if wrote != 0 {
		t.Fatalf("an unchanged pass wrote %d entities", wrote)
	}
}

// ONE PLACE PER WORKSPACE, with no glob in it, and two workspaces are provably disjoint because
// they are two places — which is what an install checks before it will let both stand.
func TestSlackOwnershipIsOnePlacePerWorkspace(t *testing.T) {
	f := newFake()
	src := newSource(t, f, slack.Config{})
	owns := viffio.Describe(src).Owns
	if len(owns) != 1 || owns[0].At != "slack.Acme" {
		t.Fatalf("owns %v", owns)
	}
	ok, err := viffio.Disjoint(owns, []viffio.Own{{At: "slack.Other"}})
	if err != nil || !ok {
		t.Fatalf("two workspaces were not provably disjoint (%v, %v)", ok, err)
	}
	if ok, _ := viffio.Disjoint(owns, []viffio.Own{{At: "slack.Acme"}}); ok {
		t.Fatal("a workspace was proved disjoint from itself")
	}
	// Every ref this source writes is inside what it claims — which is the bound on what a
	// --forget removal may tombstone. A place claims what stands beneath it, so this is one
	// claim rather than one per kind.
	for _, ref := range []entity.Ref{
		entity.RefAt("slack", "Acme", "channel", "C1"),
		entity.RefAt("slack", "Acme", "message", "C1", "1700000001.000100"),
		entity.RefAt("slack", "Acme", "user", "U1"),
	} {
		if !claims(t, owns, ref) {
			t.Fatalf("%s is written but not claimed", ref)
		}
	}
}

// -channel narrows what is reflected, and a channel that leaves the watched set is tombstoned
// rather than left standing with a window nothing will refresh again.
func TestSlackNarrowingToChannels(t *testing.T) {
	f := newFake()
	f.channel("C1", "incidents")
	f.channel("C2", "releases")
	f.say("C1", "1700000001.000100", "U1", "one")
	f.say("C2", "1700000002.000100", "U1", "two")

	src := newSource(t, f, slack.Config{Channels: []string{"#incidents"}})
	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	got := payloads(rn)
	if got["slack.Acme.channel.C1"] == nil {
		t.Fatal("the named channel was not reflected")
	}
	if got["slack.Acme.channel.C2"] != nil {
		t.Fatal("a channel outside -channel was reflected")
	}
	// A targeted pass over the un-watched channel covers it and carries nothing: the cover is
	// how "this is no longer mine to reflect" is said.
	rn, err = src.Reflect(context.Background(), slack.ChannelRef("Acme", "C2"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rn.Entities) != 0 || len(rn.Covers) == 0 {
		t.Fatalf("a narrowed-out channel answered %+v", rn)
	}
}

// claims reports whether an ownership list owns anything at ref. Parsed once and asserted on,
// because the parse is where a malformed claim is supposed to stop — see viffio.ParseOwns.
func claims(t *testing.T, owns []viffio.Own, ref entity.Ref) bool {
	t.Helper()
	cs, err := viffio.ParseOwns(owns)
	if err != nil {
		t.Fatalf("parsing %v: %v", owns, err)
	}
	return len(viffio.Claimed(cs, ref)) > 0
}
