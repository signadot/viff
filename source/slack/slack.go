// Package slack reflects a Slack workspace into the `slack:*` slice of the entity-verse.
//
// It is a deployed source: a program holding a bot token, serving the two routes
// source/sourced serves, with verse dialling it and converging its answers. It cannot be an
// in-process source for the same reason it does not have to be — the credential is the whole
// difference between it and the git source, and a credential belongs where it is already the
// operator's problem to hold one (docs/sources.md, "nothing new about credentials").
//
// WHAT IT REFLECTS is an index, not a warehouse: the channels the bot is in, a bounded tip
// window of each one's messages, and the workspace's people. What an SDLC flow reacts to is
// "somebody said this in #incidents", "the deploy thread got a ✅", "a channel was archived" —
// not every byte the workspace has ever held. A task that needs the rest follows the
// message's url into Slack.
package slack

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/source"
)

// System is the entity slice a Slack source owns and is the single writer of.
const System = "slack"

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
	// Workspace is the id prefix every entity carries and the ownership claim is spelled with.
	// Empty takes the team name auth.test answers with — chosen rather than the opaque team id
	// because it is what appears in a ref an operator reads, and a workspace has exactly one
	// source. It may not contain `:` or `/`, which are the ref's own punctuation.
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
	// System is the slice this source writes, empty meaning System. The operator's to choose,
	// because more than one source gets deployed and the partition between them is drawn by
	// whoever runs them; two systems are disjoint on the first segment, so a deployment that
	// wants no coordination owes nobody a disjointness proof.
	System string

	// Base overrides the Web API root (a test server). HTTPClient overrides the transport.
	Base       string
	HTTPClient *http.Client
	// Log receives what a pass decided to carry on without. nil is silent.
	Log func(format string, args ...any)
}

// Slack is the Source.
type Slack struct {
	api       *api
	system    string
	workspace string
	url       string // the workspace's Slack url, for message permalinks
	depth     int
	threads   bool
	threadCap int
	users     bool
	only      map[string]bool
	log       func(string, ...any)
}

var _ source.Source = (*Slack)(nil)

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
		system:    cfg.System,
		workspace: cfg.Workspace,
		depth:     cfg.Depth,
		threads:   cfg.Threads,
		threadCap: cfg.ThreadDepth,
		users:     cfg.Users,
		log:       cfg.Log,
	}
	if s.system == "" {
		s.system = System
	}
	if strings.ContainsAny(s.system, ":/") {
		return nil, fmt.Errorf("slack: system %q may not contain : or / — it is a segment of "+
			"the address (verse.sources.<system>.<name>) and of the route that serves it", s.system)
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
	s.url = id.URL
	if s.workspace == "" {
		s.workspace = id.Team
	}
	// The workspace is a ref segment before it is anything else, and an id that carried the
	// ref's own punctuation would address something other than what it names.
	if s.workspace == "" || strings.ContainsAny(s.workspace, ":/") {
		return nil, fmt.Errorf("slack: workspace %q may not be empty or contain : or / — it is "+
			"the id prefix of every entity this source writes", s.workspace)
	}
	return s, nil
}

// System is the owned slice.
func (s *Slack) System() string { return s.system }

// Owns is the ownership claim an install proves disjointness with.
//
// ONE ENTRY, where git needs two per repo, because every id here is `<workspace>/…` with no
// exceptions — the workspace has no bare-id entity of its own the way a repo has its head. A
// bot token is issued for one workspace, so a source is one workspace's, and two sources are
// two workspaces: provably disjoint as long as no two workspaces are given the same name.
func (s *Slack) Owns() []source.Own {
	return []source.Own{{At: s.system + ".*." + s.workspace + "/*"}}
}

// ChannelRef is the ref that targets one channel — what a hint names, and what a targeted
// reconcile narrows to.
func ChannelRef(workspace, channel string) entity.Ref {
	return entity.RefAt(System, "channel", workspace+"/"+channel)
}

// ChannelRef is the same ref in THIS source's system, which is what a delivery endpoint must
// hint: a hint at `slack:channel:x` reaches nothing when the source writes `chat:channel:x`.
func (s *Slack) ChannelRef(channel string) entity.Ref {
	return entity.RefAt(s.system, "channel", s.workspace+"/"+channel)
}

// Workspace is the id prefix this source writes under.
func (s *Slack) Workspace() string { return s.workspace }

// Reflect reads the workspace and returns what `slack:*` should hold. A zero ref reads every
// watched channel (the full pass); a channel or message ref reads just that channel.
func (s *Slack) Reflect(ctx context.Context, ref entity.Ref) (source.Reflection, error) {
	channel, full := s.target(ref)
	if full {
		return s.reflectAll(ctx)
	}
	if channel == "" {
		// A ref in another workspace's slice. An EMPTY reflection with no covers, not an
		// error: it claims nothing, so converge writes nothing and tombstones nothing, which
		// is the honest answer to a question about somebody else's slice.
		return source.Reflection{}, nil
	}
	return s.reflectChannel(ctx, channel)
}

// target reads a ref as "which channel", answering (channel, full).
//
// A message ref narrows to its channel rather than to itself, which is the same subsumption
// git makes: the window is read per channel, so a finer target would cost the same call.
func (s *Slack) target(ref entity.Ref) (channel string, full bool) {
	if ref.System() != s.system || ref.ID() == "" {
		return "", true
	}
	ws, rest, ok := strings.Cut(ref.ID(), "/")
	if !ok || ws != s.workspace {
		return "", false
	}
	switch ref.Kind() {
	case "channel":
		return rest, false
	case "message":
		id, _, _ := strings.Cut(rest, "/") // <channel>/<ts>
		return id, false
	default:
		// A user ref, or a kind this build does not know. A full pass is always a correct
		// answer to a narrower question, and it is the one that cannot be wrong.
		return "", true
	}
}

// reflectAll is the full pass: every channel the bot is in, each one's window, and — when
// asked for — the people.
func (s *Slack) reflectAll(ctx context.Context) (source.Reflection, error) {
	channels, err := s.api.conversations(ctx)
	if err != nil {
		return source.Reflection{}, err
	}
	// Declared BEFORE the read and unconditionally, exactly as the git source declares its
	// per-repo covers: a workspace the bot was removed from every channel of must still cover
	// `slack:channel:{workspace}`, or the channels it can no longer see would stand forever.
	mine := entity.Obj{"workspace": s.workspace}.Payload()
	rn := source.Reflection{Covers: []entity.Pattern{
		{System: s.system, Kind: "channel", Predicate: mine},
		{System: s.system, Kind: "message", Predicate: mine},
	}}
	for _, c := range channels {
		if !s.watched(c) {
			continue
		}
		s.addChannel(&rn, c)
		if err := s.addMessages(ctx, &rn, c.ID); err != nil {
			return source.Reflection{}, err
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
			rn.Covers = append(rn.Covers,
				entity.Pattern{System: s.system, Kind: "user", Predicate: mine})
			for _, u := range users {
				s.addUser(&rn, u)
			}
		}
	}
	return rn, nil
}

// reflectChannel is the targeted pass a hint asks for: one channel, and the covers narrowed to
// it so the answer cannot tombstone another channel's messages.
func (s *Slack) reflectChannel(ctx context.Context, id string) (source.Reflection, error) {
	c, err := s.api.conversationInfo(ctx, id)
	if err != nil {
		return source.Reflection{}, err
	}
	mine := entity.Obj{"workspace": s.workspace, "channel": id}.Payload()
	rn := source.Reflection{Covers: []entity.Pattern{
		{System: s.system, Kind: "channel", Predicate: mine},
		{System: s.system, Kind: "message", Predicate: mine},
	}}
	if !s.watched(c) {
		// Narrowed out of what this source reflects: the covers stand, the entities do not, so
		// the pass tombstones a channel that has left the watched set rather than leaving it
		// standing with a window that will never be refreshed again.
		return rn, nil
	}
	s.addChannel(&rn, c)
	if err := s.addMessages(ctx, &rn, id); err != nil {
		return source.Reflection{}, err
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

func (s *Slack) addChannel(rn *source.Reflection, c channel) {
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
	rn.Add(ChannelRef(s.workspace, c.ID), p)
}

// addMessages reads one channel's window, and the threads hanging off it.
func (s *Slack) addMessages(ctx context.Context, rn *source.Reflection, id string) error {
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

func (s *Slack) addMessage(rn *source.Reflection, channel string, m message) {
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
	rn.Add(entity.RefAt(s.system, "message", s.workspace+"/"+channel+"/"+m.TS), p)
}

func (s *Slack) addUser(rn *source.Reflection, u user) {
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
	rn.Add(entity.RefAt(s.system, "user", s.workspace+"/"+u.ID), p)
}
