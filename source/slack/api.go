package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The Slack Web API, as much of it as a reflection and the out half need.
//
// encoding/json rather than codec, and this file is on scripts/check-json.sh's allowlist for
// the reason task/harness/llm.go is: this is somebody else's wire format. verse renders in
// tony; Slack does not, and re-rendering its schema through codec would buy nothing but a
// second place for it to be wrong.
//
// Hand-rolled rather than slack-go, which is the same call the tree already made for the
// OpenAI schema. What is needed is ten methods and a handful of structs; a client library would
// bring a retry policy, an events framework and a bot toolkit, and the one thing a connector
// must be is small enough to read.

// APIBase is where the Web API lives. Overridable so a test can point at an httptest server.
const APIBase = "https://slack.com/api"

type api struct {
	token string
	base  string
	cl    *http.Client
}

// envelope is the part of every Slack response that says whether it is an answer.
//
// THE TRAP THIS FILE EXISTS TO AVOID: Slack answers a refused call with HTTP 200 and
// `{"ok": false, "error": "…"}`. A client that checked the status and decoded the rest would
// read a permission failure as a workspace with no channels in it — and an empty reflection
// carrying covers is a legitimate instruction to tombstone everything under them. So `ok` is
// checked on every call, in one place, and nothing here can forget to.
type envelope struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	Meta  struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

func (e envelope) err() error {
	if e.OK {
		return nil
	}
	if e.Error == "" {
		return fmt.Errorf("slack: refused, with no reason given")
	}
	return fmt.Errorf("slack: %s", e.Error)
}

func (e envelope) cursor() string { return e.Meta.NextCursor }

// answer is what call() needs of a response type: whether Slack said yes, and where the next
// page is.
type answer interface {
	err() error
	cursor() string
}

// call issues one Web API request and decodes it, treating every way it can fail as an error.
func (a *api) call(ctx context.Context, method string, params url.Values, out answer) error {
	u := a.base + "/" + method
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	return a.do(req, method, out)
}

// post issues one Web API call whose arguments are a JSON body: the writes, which Slack takes as
// POST, and whose text should not ride in a URL.
func (a *api) post(ctx context.Context, method string, args any, out answer) error {
	b, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/"+method, strings.NewReader(string(b)))
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	return a.do(req, method, out)
}

// do sends a request and decodes the answer, treating every way it can fail as an error.
func (a *api) do(req *http.Request, method string, out answer) error {
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.cl.Do(req)
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	// A 429 is the one status worth naming: it is the answer a source gets for asking too
	// often, it fixes itself, and reading it as anything else would send an operator looking
	// at scopes. See source.Throttle for the side that prevents it.
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("slack %s: rate limited (retry after %s)", method,
			resp.Header.Get("Retry-After"))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack %s: %s", method, resp.Status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("slack %s: undecodable answer: %w", method, err)
	}
	return out.err()
}

// paged runs a cursor-paged method to exhaustion, calling take for each page.
//
// To EXHAUSTION rather than to a first page, because a partial list under a complete cover is
// how a reflection tombstones what it merely failed to read. The cap is a runaway guard, and
// hitting it is an error for the same reason.
func (a *api) paged(ctx context.Context, method string, params url.Values, out answer, take func()) error {
	const maxPages = 50
	q := url.Values{}
	for k, v := range params {
		q[k] = v
	}
	for page := 0; ; page++ {
		if page == maxPages {
			return fmt.Errorf("slack %s: more than %d pages — narrow what this source "+
				"reflects rather than reflecting a slice it cannot answer completely for",
				method, maxPages)
		}
		if err := a.call(ctx, method, q, out); err != nil {
			return err
		}
		take()
		next := out.cursor()
		if next == "" {
			return nil
		}
		q.Set("cursor", next)
	}
}

// ── the shapes, and only the fields a reflection carries ─────────────────────

type textValue struct {
	Value string `json:"value"`
}

type channel struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	IsPrivate  bool      `json:"is_private"`
	IsArchived bool      `json:"is_archived"`
	IsGeneral  bool      `json:"is_general"`
	NumMembers int       `json:"num_members"`
	Created    int64     `json:"created"`
	Topic      textValue `json:"topic"`
	Purpose    textValue `json:"purpose"`
}

type reaction struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type message struct {
	Type       string     `json:"type"`
	Subtype    string     `json:"subtype"`
	TS         string     `json:"ts"`
	ThreadTS   string     `json:"thread_ts"`
	User       string     `json:"user"`
	BotID      string     `json:"bot_id"`
	Username   string     `json:"username"`
	Text       string     `json:"text"`
	ReplyCount int        `json:"reply_count"`
	Reactions  []reaction `json:"reactions"`
	Edited     *struct {
		TS   string `json:"ts"`
		User string `json:"user"`
	} `json:"edited"`
}

type user struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"real_name"`
	Deleted  bool   `json:"deleted"`
	IsBot    bool   `json:"is_bot"`
	IsAdmin  bool   `json:"is_admin"`
	TZ       string `json:"tz"`
	Profile  struct {
		Email       string `json:"email"`
		Title       string `json:"title"`
		DisplayName string `json:"display_name"`
	} `json:"profile"`
}

// ── the reflection's six calls ──────────────────────────────────────────────

// Identity is what a token turns out to be — the answer to auth.test.
type Identity struct {
	Team   string `json:"team"`
	TeamID string `json:"team_id"`
	URL    string `json:"url"`
	User   string `json:"user"`
	UserID string `json:"user_id"`
	BotID  string `json:"bot_id"`
}

type authTestResp struct {
	envelope
	Identity
}

// authTest asks who this token is. It is the cheapest call with an answer, so it doubles as
// the startup check: a bad token fails the task rather than every reflect.
func (a *api) authTest(ctx context.Context) (Identity, error) {
	var r authTestResp
	if err := a.call(ctx, "auth.test", nil, &r); err != nil {
		return Identity{}, err
	}
	return r.Identity, nil
}

// ── the out half's four: open a DM, post, update, and ask for a permalink ──────

type openResp struct {
	envelope
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
}

// openDM answers the DM channel with one person (`im:write`), opening it if it is not open yet.
// Opening an open one answers the same channel, so it is safe on every post.
func (a *api) openDM(ctx context.Context, userID string) (string, error) {
	var r openResp
	if err := a.post(ctx, "conversations.open", map[string]any{"users": userID}, &r); err != nil {
		return "", err
	}
	if r.Channel.ID == "" {
		return "", fmt.Errorf("slack conversations.open: answered no channel for %s", userID)
	}
	return r.Channel.ID, nil
}

type postResp struct {
	envelope
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// postMessage says text in channel (`chat:write`) and answers where it landed: the channel Slack
// names and the message's ts, which is what an update needs. blocks, when there are any, are the
// message's layout, and text is then what a notification and a screen reader show.
func (a *api) postMessage(ctx context.Context, channel, text string, blocks []any) (string, string, error) {
	var r postResp
	args := map[string]any{"channel": channel, "text": text}
	if len(blocks) > 0 {
		args["blocks"] = blocks
	}
	if err := a.post(ctx, "chat.postMessage", args, &r); err != nil {
		return "", "", err
	}
	if r.TS == "" {
		return "", "", fmt.Errorf("slack chat.postMessage: answered no ts")
	}
	if r.Channel == "" {
		r.Channel = channel
	}
	return r.Channel, r.TS, nil
}

// updateMessage replaces the text, and the blocks, of the message at ts in channel (`chat:write`).
// No blocks clears the ones it had, so an input that drops its buttons drops them from Slack too.
func (a *api) updateMessage(ctx context.Context, channel, ts, text string, blocks []any) error {
	var r envelope
	if blocks == nil {
		blocks = []any{}
	}
	args := map[string]any{"channel": channel, "ts": ts, "text": text, "blocks": blocks}
	return a.post(ctx, "chat.update", args, &r)
}

type permalinkResp struct {
	envelope
	Permalink string `json:"permalink"`
}

// permalink is the message's link, for a reader who wants to go and look.
func (a *api) permalink(ctx context.Context, channel, ts string) (string, error) {
	var r permalinkResp
	q := url.Values{"channel": {channel}, "message_ts": {ts}}
	if err := a.call(ctx, "chat.getPermalink", q, &r); err != nil {
		return "", err
	}
	return r.Permalink, nil
}

type conversationsResp struct {
	envelope
	Channels []channel `json:"channels"`
}

// conversations lists the channels the bot is a member of.
//
// users.conversations rather than conversations.list, and the difference is honesty about
// permission: a bot can enumerate public channels it has never been invited to, and can read
// the history of none of them. Reflecting a channel whose messages will always be missing
// would put an entity in the verse that no trigger can ever say anything true about. So the
// membership IS the configuration — invite the bot to what you want reflected.
func (a *api) conversations(ctx context.Context) ([]channel, error) {
	var out []channel
	var r conversationsResp
	params := url.Values{
		"types":            {"public_channel,private_channel"},
		"limit":            {"200"},
		"exclude_archived": {"false"},
	}
	err := a.paged(ctx, "users.conversations", params, &r, func() {
		out = append(out, r.Channels...)
		r.Channels = nil
	})
	return out, err
}

type conversationsInfoResp struct {
	envelope
	Channel channel `json:"channel"`
}

// conversationInfo reads one channel, which is what a targeted pass needs instead of the list.
func (a *api) conversationInfo(ctx context.Context, id string) (channel, error) {
	var r conversationsInfoResp
	err := a.call(ctx, "conversations.info", url.Values{"channel": {id}}, &r)
	return r.Channel, err
}

type historyResp struct {
	envelope
	Messages []message `json:"messages"`
}

// history reads the last `limit` messages of a channel — the bounded tip window.
func (a *api) history(ctx context.Context, id string, limit int) ([]message, error) {
	var r historyResp
	err := a.call(ctx, "conversations.history", url.Values{
		"channel": {id},
		"limit":   {fmt.Sprint(limit)},
	}, &r)
	return r.Messages, err
}

// replies reads a thread, parent first. The parent comes back in the list and is dropped by
// the caller, which already has it.
func (a *api) replies(ctx context.Context, id, ts string, limit int) ([]message, error) {
	var r historyResp
	err := a.call(ctx, "conversations.replies", url.Values{
		"channel": {id},
		"ts":      {ts},
		"limit":   {fmt.Sprint(limit)},
	}, &r)
	return r.Messages, err
}

type usersResp struct {
	envelope
	Members []user `json:"members"`
}

// users lists the workspace's members.
func (a *api) users(ctx context.Context) ([]user, error) {
	var out []user
	var r usersResp
	err := a.paged(ctx, "users.list", url.Values{"limit": {"200"}}, &r, func() {
		out = append(out, r.Members...)
		r.Members = nil
	})
	return out, err
}

// ── the click's three: who clicked, the socket to hear it on, and the reply ───

type userInfoResp struct {
	envelope
	User user `json:"user"`
}

// userEmail is the email of the person with id userID (`users:read.email`): how a click in Slack
// becomes a principal in the verse.
func (a *api) userEmail(ctx context.Context, userID string) (string, error) {
	var r userInfoResp
	if err := a.call(ctx, "users.info", url.Values{"user": {userID}}, &r); err != nil {
		return "", err
	}
	if r.User.Profile.Email == "" {
		return "", fmt.Errorf("slack users.info: %s has no email this app may read", userID)
	}
	return r.User.Profile.Email, nil
}

type connectionsResp struct {
	envelope
	URL string `json:"url"`
}

// connectionsOpen asks for a Socket Mode url. It is called on an api holding the APP-level token
// (`xapp-…`, `connections:write`), not the bot's.
func (a *api) connectionsOpen(ctx context.Context) (string, error) {
	var r connectionsResp
	if err := a.post(ctx, "apps.connections.open", map[string]any{}, &r); err != nil {
		return "", err
	}
	if r.URL == "" {
		return "", fmt.Errorf("slack apps.connections.open: answered no url")
	}
	return r.URL, nil
}

// respond replies to a click, visibly to the person who clicked and nobody else, through the url
// Slack sent with it. That url is its own authority, so it carries no token.
func (a *api) respond(ctx context.Context, responseURL, text string) error {
	b, err := json.Marshal(map[string]any{
		"response_type": "ephemeral", "replace_original": false, "text": text,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responseURL, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := a.cl.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack response_url: %s", resp.Status)
	}
	return nil
}

// newAPI builds the client. Its own http.Client, never http.DefaultClient, so the timeout
// below bounds this source's calls and nobody else's.
func newAPI(token, base string, cl *http.Client) *api {
	if base == "" {
		base = APIBase
	}
	if cl == nil {
		cl = &http.Client{Timeout: 30 * time.Second}
	}
	return &api{token: token, base: strings.TrimRight(base, "/"), cl: cl}
}
