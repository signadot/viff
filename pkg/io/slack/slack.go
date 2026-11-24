// Package slack reflects a Slack workspace into the `slack:*` slice of the entity-viff.
//
// It is a deployed source: a program holding a bot token, serving the two routes
// source/iod serves, with viff dialling it and converging its answers. It cannot be an
// in-process source for the same reason it does not have to be — the credential is the whole
// difference between it and the git source, and a credential belongs where it is already the
// operator's problem to hold one (docs/io.md, "nothing new about credentials").
//
// WHAT IT REFLECTS is an index, not a warehouse: the channels the bot is in, a bounded tip
// window of each one's messages, and the workspace's people. What an SDLC flow reacts to is
// "somebody said this in #incidents", "the deploy thread got a ✅", "a channel was archived" —
// not every byte the workspace has ever held. A task that needs the rest follows the
// message's url into Slack.
//
// THE WORKSPACE IS A LEVEL OF THE ADDRESS, and a message stands under its channel:
//
//	slack.acme.channel.C0193           the channels the bot is in
//	slack.acme.message.C0193."17000…"  that channel's window, newest last
//	slack.acme.user.U04AB              the people
//
// So a channel and its history are one subtree — `viff data slack acme message C0193` is the
// window — and the workspace is one claim rather than a prefix inside every id.
package slack

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"go.viff.dev/viff/pkg/entity"
	viffio "go.viff.dev/viff/pkg/io"
)

// Root is the entity slice a Slack source owns and is the single writer of.
const Root = "slack"

// Defaults for what a pass reads. Depth is small on purpose: the window is what a charter
// reacts to, and every message in it is re-read on every pass.
const (
	DefaultDepth       = 20
	DefaultThreadDepth = 50
)

// Config is what a Slack source is built from.
type Config struct {
	// Token is the bot token (`xoxb-…`), sent as a bearer.
	Token string
	// Workspace is the PLACE this source writes under (`slack.<workspace>`) and the ownership
	// claim it proves disjointness with. Empty takes the team name auth.test answers with —
	// chosen rather than the opaque team id because it is what an operator reads in an address,
	// and a workspace has exactly one source.
	Workspace string
	// Depth is how many recent messages per channel the window holds (0 → DefaultDepth).
	Depth int
	// Threads reads replies for the threaded messages in the window.
	Threads bool
	// ThreadDepth bounds one thread's replies (0 → DefaultThreadDepth).
	ThreadDepth int
	// Users reflects the workspace's people as `slack:user:*`. It needs the `users:read` scope,
	// and failing it is not fatal — see reflectAll.
	Users bool
	// Channels narrows to these channel names or ids; empty is every channel the bot is in.
	Channels []string
	// Root is the slice this source writes, empty meaning the package's Root. The operator's to choose,
	// because more than one source gets deployed and the partition between them is drawn by
	// whoever runs them; two roots are disjoint on the first segment, so a deployment that
	// wants no coordination owes nobody a disjointness proof.
	Root string

	// Base overrides the Web API root (a test server). HTTPClient overrides the transport.
	Base       string
	HTTPClient *http.Client
	// Log receives what a pass decided to carry on without. nil is silent.
	Log func(format string, args ...any)
}

// Slack is the Source.
type Slack struct {
	api       *api
	root      string
	workspace string
	url       string // the workspace's Slack url, for message permalinks
	me        string // this bot's own user id, for reading what is addressed to it
	depth     int
	threads   bool
	threadCap int
	users     bool
	only      map[string]bool
	log       func(string, ...any)
}

var _ viffio.Source = (*Slack)(nil)

// New builds a Slack source and asks the token who it is.
//
// The auth.test happens HERE rather than on the first reflect, and it is the same argument the
// git mirror's clone makes: a bad token, a revoked app or a missing scope is then a task
// that does not start, in front of whoever just deployed it — instead of a source that
// installs clean and writes a failure into lastError once a minute forever. It is also what
// fills in the workspace name and the url a message's permalink is built from, so the answer
// is a fact about the token rather than something an operator had to type correctly twice.
func New(ctx context.Context, cfg Config) (*Slack, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("slack: a bot token is required")
	}
	s := &Slack{
		api:       newAPI(cfg.Token, cfg.Base, cfg.HTTPClient),
		root:      cfg.Root,
		workspace: cfg.Workspace,
		depth:     cfg.Depth,
		threads:   cfg.Threads,
		threadCap: cfg.ThreadDepth,
		users:     cfg.Users,
		log:       cfg.Log,
	}
	if s.root == "" {
		s.root = Root
	}
	if strings.ContainsAny(s.root, ":/.") {
		return nil, fmt.Errorf("slack: root %q may not contain : / or . — it is the first "+
			"segment of every address this source writes", s.root)
	}
	if s.depth <= 0 {
		s.depth = DefaultDepth
	}
	if s.threadCap <= 0 {
		s.threadCap = DefaultThreadDepth
	}
	if s.log == nil {
		s.log = func(string, ...any) {}
	}
	if len(cfg.Channels) > 0 {
		s.only = map[string]bool{}
		for _, c := range cfg.Channels {
			s.only[strings.TrimPrefix(c, "#")] = true
		}
	}
	id, err := s.api.authTest(ctx)
	if err != nil {
		return nil, err
	}
	s.url, s.me = id.URL, id.UserID
	if s.workspace == "" {
		s.workspace = id.Team
	}
	// The workspace is one segment of an address, and it is what a person types as a step.
	if s.workspace == "" || strings.ContainsAny(s.workspace, ":/.") {
		return nil, fmt.Errorf("slack: workspace %q may not be empty or contain : / or . — it "+
			"is the place every entity this source writes stands under", s.workspace)
	}
	return s, nil
}

// Root is the owned slice.
func (s *Slack) Root() string { return s.root }

// Owns is the ownership claim an install proves disjointness with.
//
// ONE PLACE, with no glob in it: everything this source writes stands under
// `slack.<workspace>`, so the claim is where its entities live rather than a pattern over ids.
// A bot token is issued for one workspace, so a source is one workspace's, and two sources are
// two workspaces — two places, disjoint because they are different places.
func (s *Slack) Owns() []viffio.Own {
	return []viffio.Own{{At: s.WorkspaceRef().String()}}
}

// place is the address segments this workspace's entities stand under, followed by more.
func (s *Slack) place(more ...string) []string {
	return append([]string{s.root, s.workspace}, more...)
}

// WorkspaceRef is the place this source's entities stand under: a reconcile of it reconciles
// the workspace whole.
func (s *Slack) WorkspaceRef() entity.Ref { return entity.RefAt(s.place()...) }

// ChannelRef is the ref that targets one channel — what a hint names, and what a targeted
// reconcile narrows to.
func ChannelRef(workspace, channel string) entity.Ref {
	return entity.RefAt(Root, workspace, "channel", channel)
}

// ChannelRef is the same ref in THIS source's root, which is what a delivery endpoint must
// hint: a hint at `slack.x.channel.y` reaches nothing when the source writes `chat.x.channel.y`.
func (s *Slack) ChannelRef(channel string) entity.Ref {
	return entity.RefAt(s.place("channel", channel)...)
}

// MessageRef is the ref of one message: its channel, then Slack's own timestamp.
//
// Under a `message` kind keyed by channel rather than inside the channel's own node, which is
// the shape the github source's comments have and for the same reason: a channel node is
// written whole on every pass, and a window standing inside it would be rewritten with it.
// `slack.<ws>.message.<C>.*` is still one place — "the messages in that channel".
func (s *Slack) MessageRef(channel, ts string) entity.Ref {
	return entity.RefAt(s.place("message", channel, ts)...)
}

// UserRef is the ref of one of the workspace's people.
func (s *Slack) UserRef(id string) entity.Ref { return entity.RefAt(s.place("user", id)...) }

// Workspace is the place this source writes under.
func (s *Slack) Workspace() string { return s.workspace }

// Reflect reads the workspace and returns what `slack:*` should hold. A zero ref reads every
// watched channel (the full pass); a channel or message ref reads just that channel.
func (s *Slack) Reflect(ctx context.Context, ref entity.Ref) (viffio.Reflection, error) {
	channel, full := s.target(ref)
	if full {
		return s.reflectAll(ctx)
	}
	if channel == "" {
		// A ref in another workspace's slice. An EMPTY reflection with no covers, not an
		// error: it claims nothing, so converge writes nothing and tombstones nothing, which
		// is the honest answer to a question about somebody else's slice.
		return viffio.Reflection{}, nil
	}
	return s.reflectChannel(ctx, channel)
}

// target reads a ref as "which channel", answering (channel, full).
//
// A message ref narrows to its channel rather than to itself, which is the same subsumption
// git makes: the window is read per channel, so a finer target would cost the same call.
//
// IT IS A PREFIX READ, because the layout is a path: `slack.<workspace>.<kind>.<…>` says which
// workspace and which channel in its segments, where an id holding `<ws>/<chan>/<ts>` had to be
// cut apart and every reader had to know the punctuation.
func (s *Slack) target(ref entity.Ref) (channel string, full bool) {
	segs := ref.Segs()
	if len(segs) < 2 || segs[0] != s.root {
		return "", true // no place of this source's: a full pass is never a wrong answer
	}
	if segs[1] != s.workspace {
		return "", false // another workspace's place
	}
	if len(segs) < 4 {
		return "", true // the workspace itself, or a kind with no id: read it whole
	}
	switch segs[2] {
	case "channel", "message":
		return segs[3], false // `channel.<C>`, and `message.<C>.<ts>`
	default:
		// A user ref, or a kind this build does not know. A full pass is always a correct
		// answer to a narrower question, and it is the one that cannot be wrong.
		return "", true
	}
}

// reflectAll is the full pass: every channel the bot is in, each one's window, and — when
// asked for — the people.
func (s *Slack) reflectAll(ctx context.Context) (viffio.Reflection, error) {
	channels, err := s.api.conversations(ctx)
	if err != nil {
		return viffio.Reflection{}, err
	}
	// Declared BEFORE the read and unconditionally, exactly as the git source declares its
	// per-repo covers: a workspace the bot was removed from every channel of must still cover
	// the channels, or the ones it can no longer see would stand forever.
	//
	// PLACES, not predicates. The workspace is a level of the address, so "this workspace's
	// channels" is `slack.<ws>.channel.*` — where an id carrying the workspace needed a payload
	// predicate to say it, and a reader had to trust every entity to carry the field.
	rn := viffio.Reflection{Covers: []entity.Pattern{
		viffio.Cover(s.place("channel", "*")...),
		viffio.Cover(s.place("message", "*", "*")...),
	}}
	for _, c := range channels {
		if !s.watched(c) {
			continue
		}
		s.addChannel(&rn, c)
		if err := s.addMessages(ctx, &rn, c.ID); err != nil {
			return viffio.Reflection{}, err
		}
	}
	if s.users {
		// A users.list failure DROPS THE COVER AND THE ENTITIES rather than failing the pass,
		// and this is Covers doing precisely what it is for. The people are a second, separable
		// claim: a source that cannot read them has an answer that is complete for the channels
		// and complete for nothing else, and saying so leaves what it could not read standing
		// instead of deleting it. The channels are not treated this way — they are the reason
		// this source exists, and a pass that lost them has nothing true left to say.
		users, err := s.api.users(ctx)
		if err != nil {
			s.log("slack: users not reflected this pass (%v) — the `users:read` scope is what "+
				"this needs; channels are unaffected", err)
		} else {
			rn.Covers = append(rn.Covers, viffio.Cover(s.place("user", "*")...))
			for _, u := range users {
				s.addUser(&rn, u)
			}
		}
	}
	return rn, nil
}

// reflectChannel is the targeted pass a hint asks for: one channel, and the covers narrowed to
// it so the answer cannot tombstone another channel's messages.
func (s *Slack) reflectChannel(ctx context.Context, id string) (viffio.Reflection, error) {
	c, err := s.api.conversationInfo(ctx, id)
	if err != nil {
		return viffio.Reflection{}, err
	}
	// NARROWED BY THE PATH ITSELF: the channel node, and the messages under that channel — so
	// this pass cannot tombstone another channel's window.
	rn := viffio.Reflection{Covers: []entity.Pattern{
		viffio.Cover(s.place("channel", id)...),
		viffio.Cover(s.place("message", id, "*")...),
	}}
	if !s.watched(c) {
		// Narrowed out of what this source reflects: the covers stand, the entities do not, so
		// the pass tombstones a channel that has left the watched set rather than leaving it
		// standing with a window that will never be refreshed again.
		return rn, nil
	}
	s.addChannel(&rn, c)
	if err := s.addMessages(ctx, &rn, id); err != nil {
		return viffio.Reflection{}, err
	}
	return rn, nil
}

// watched reports whether a channel is one this source reflects.
func (s *Slack) watched(c channel) bool {
	if s.only == nil {
		return true
	}
	return s.only[c.Name] || s.only[c.ID]
}

func (s *Slack) addChannel(rn *viffio.Reflection, c channel) {
	p := entity.Obj{
		"workspace": s.workspace,
		"channel":   c.ID,
		"name":      c.Name,
		"private":   c.IsPrivate,
		"archived":  c.IsArchived,
	}
	if c.Topic.Value != "" {
		p["topic"] = c.Topic.Value
	}
	if c.Purpose.Value != "" {
		p["purpose"] = c.Purpose.Value
	}
	if c.NumMembers > 0 {
		p["members"] = c.NumMembers
	}
	if c.Created > 0 {
		p["created"] = c.Created
	}
	rn.Add(s.ChannelRef(c.ID), p)
}

// addMessages reads one channel's window, and the threads hanging off it.
func (s *Slack) addMessages(ctx context.Context, rn *viffio.Reflection, id string) error {
	msgs, err := s.api.history(ctx, id, s.depth)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		s.addMessage(rn, id, m)
		if !s.threads || m.ReplyCount == 0 {
			continue
		}
		// A thread is read only for a parent still IN the window, which is the one raggedness
		// worth knowing about: a reply does not bump its parent in the channel's history, so a
		// long-running thread on an old message stops being reflected when the parent ages
		// out. The window is the bound on everything here, threads included.
		replies, err := s.api.replies(ctx, id, m.TS, s.threadCap)
		if err != nil {
			return err
		}
		for _, r := range replies {
			if r.TS == m.TS {
				continue // the parent, which is already in
			}
			s.addMessage(rn, id, r)
		}
	}
	return nil
}

func (s *Slack) addMessage(rn *viffio.Reflection, channel string, m message) {
	p := entity.Obj{
		"workspace": s.workspace,
		"channel":   channel,
		"ts":        m.TS,
		"text":      m.Text,
	}
	if m.User != "" {
		p["user"] = m.User
	}
	if m.BotID != "" {
		p["bot"] = m.BotID
	}
	if m.Username != "" {
		p["username"] = m.Username
	}
	if m.Subtype != "" {
		p["subtype"] = m.Subtype
	}
	if m.ThreadTS != "" && m.ThreadTS != m.TS {
		p["thread"] = m.ThreadTS
	}
	if m.ReplyCount > 0 {
		p["replies"] = m.ReplyCount
	}
	if m.Edited != nil && m.Edited.TS != "" {
		p["edited"] = m.Edited.TS
	}
	if len(m.Reactions) > 0 {
		// name → count, rather than a list of objects, because a payload predicate is how a
		// charter asks about this and an object is what one can ask about: `{reactions:
		// {shipit: null}}` is a presence test (entity.Pattern.Predicate), and the same question
		// against a list would need a match this grammar does not have.
		rs := map[string]any{}
		for _, r := range m.Reactions {
			rs[r.Name] = r.Count
		}
		p["reactions"] = rs
	}
	if s.url != "" {
		p["url"] = strings.TrimRight(s.url, "/") + "/archives/" + channel + "/p" +
			strings.Replace(m.TS, ".", "", 1)
	}
	rn.Add(s.MessageRef(channel, m.TS), p)
}

func (s *Slack) addUser(rn *viffio.Reflection, u user) {
	p := entity.Obj{
		"workspace": s.workspace,
		"user":      u.ID,
		"name":      u.Name,
		"bot":       u.IsBot,
		"deleted":   u.Deleted,
	}
	if u.RealName != "" {
		p["real"] = u.RealName
	}
	if u.Profile.DisplayName != "" {
		p["display"] = u.Profile.DisplayName
	}
	if u.Profile.Email != "" {
		p["email"] = u.Profile.Email
	}
	if u.Profile.Title != "" {
		p["title"] = u.Profile.Title
	}
	if u.TZ != "" {
		p["tz"] = u.TZ
	}
	rn.Add(s.UserRef(u.ID), p)
}
