package slack_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeSlack is a Slack Web API that answers from fixtures.
//
// It answers `ok: false` with HTTP 200 when told to fail, which is the one behaviour a test
// against a real client library would not reproduce and the one this source has to get right:
// a refusal that arrives as a 200 is how a permission error becomes "the workspace is empty".
type fakeSlack struct {
	mu sync.Mutex

	channels []map[string]any            // users.conversations
	info     map[string]map[string]any   // conversations.info, by id
	history  map[string][]map[string]any // conversations.history, by channel
	replies  map[string][]map[string]any // conversations.replies, by "<channel>/<ts>"
	users    []map[string]any

	// The out half's side: what was said, and where.
	posted     []map[string]any // chat.postMessage at channel level, in order: channel, ts, text
	updates    []map[string]any // chat.update, in order: channel, ts, text
	inThread   []map[string]any // chat.postMessage in a thread: what a reply was answered with
	ephemerals []map[string]any // chat.postEphemeral: what one person was told
	opened     map[string]int   // conversations.open, by user
	reacted    []string         // reactions.add, in order: "<channel> <ts> <name>"

	// The click's side: the Socket Mode url apps.connections.open answers with, which takes the
	// app-level token and no other.
	socketURL string

	fail  map[string]string // method → slack error string
	calls map[string]int
}

// appToken is the fake's app-level token, the one apps.connections.open takes.
const appToken = "test-app-token"

func newFake() *fakeSlack {
	return &fakeSlack{
		info:    map[string]map[string]any{},
		history: map[string][]map[string]any{},
		replies: map[string][]map[string]any{},
		opened:  map[string]int{},
		fail:    map[string]string{},
		calls:   map[string]int{},
	}
}

func (f *fakeSlack) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakeSlack) called(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *fakeSlack) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	f.calls[method]++
	// Slack takes each token for its own methods only: the app-level token opens a socket and does
	// nothing else, and the bot token cannot open one.
	want := "Bearer test-token"
	if method == "apps.connections.open" {
		want = "Bearer " + appToken
	}
	if auth := r.Header.Get("Authorization"); auth != want {
		refusal := "invalid_auth"
		if auth == "Bearer test-token" || auth == "Bearer "+appToken {
			refusal = "not_allowed_token_type"
		}
		write(w, map[string]any{"ok": false, "error": refusal})
		return
	}
	if msg, bad := f.fail[method]; bad {
		// HTTP 200 with ok:false — Slack's own shape for a refusal.
		write(w, map[string]any{"ok": false, "error": msg})
		return
	}
	q := r.URL.Query()
	// The writes carry their arguments as a JSON body.
	args := map[string]any{}
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&args)
	}
	str := func(k string) string { v, _ := args[k].(string); return v }
	switch method {
	case "auth.test":
		write(w, map[string]any{
			"ok": true, "team": "Acme", "team_id": "T1",
			"url": "https://acme.slack.com/", "user": "verse", "user_id": "U0",
		})
	case "users.conversations":
		write(w, map[string]any{"ok": true, "channels": f.channels})
	case "conversations.info":
		c, ok := f.info[q.Get("channel")]
		if !ok {
			write(w, map[string]any{"ok": false, "error": "channel_not_found"})
			return
		}
		write(w, map[string]any{"ok": true, "channel": c})
	case "conversations.history":
		write(w, map[string]any{"ok": true, "messages": f.history[q.Get("channel")]})
	case "conversations.replies":
		write(w, map[string]any{"ok": true,
			"messages": f.replies[q.Get("channel")+"/"+q.Get("ts")]})
	case "users.list":
		write(w, map[string]any{"ok": true, "members": f.users})
	case "users.info":
		for _, u := range f.users {
			if u["id"] == q.Get("user") {
				write(w, map[string]any{"ok": true, "user": u})
				return
			}
		}
		write(w, map[string]any{"ok": false, "error": "user_not_found"})
	case "users.lookupByEmail":
		for _, u := range f.users {
			if p, _ := u["profile"].(map[string]any); p != nil && p["email"] == q.Get("email") {
				write(w, map[string]any{"ok": true, "user": u})
				return
			}
		}
		write(w, map[string]any{"ok": false, "error": "users_not_found"})
	case "apps.connections.open":
		write(w, map[string]any{"ok": true, "url": f.socketURL})
	case "conversations.open":
		f.opened[str("users")]++
		write(w, map[string]any{"ok": true, "channel": map[string]any{"id": "D" + str("users")}})
	case "chat.postMessage":
		ts := fmt.Sprintf("1790000000.%06d", len(f.posted)+len(f.inThread)+1)
		said := map[string]any{"channel": str("channel"), "ts": ts, "text": str("text"),
			"thread_ts": str("thread_ts"), "blocks": args["blocks"]}
		if str("thread_ts") != "" {
			f.inThread = append(f.inThread, said)
		} else {
			f.posted = append(f.posted, said)
		}
		write(w, map[string]any{"ok": true, "channel": str("channel"), "ts": ts})
	case "chat.postEphemeral":
		f.ephemerals = append(f.ephemerals, map[string]any{"channel": str("channel"),
			"user": str("user"), "thread_ts": str("thread_ts"), "text": str("text")})
		write(w, map[string]any{"ok": true, "message_ts": "1790000000.000999"})
	case "chat.update":
		f.updates = append(f.updates, map[string]any{"channel": str("channel"), "ts": str("ts"), "text": str("text"),
			"blocks": args["blocks"]})
		write(w, map[string]any{"ok": true, "channel": str("channel"), "ts": str("ts")})
	case "reactions.add":
		r := str("channel") + " " + str("timestamp") + " " + str("name")
		for _, had := range f.reacted {
			if had == r {
				write(w, map[string]any{"ok": false, "error": "already_reacted"})
				return
			}
		}
		f.reacted = append(f.reacted, r)
		write(w, map[string]any{"ok": true})
	case "chat.getPermalink":
		write(w, map[string]any{"ok": true,
			"permalink": "https://acme.slack.com/archives/" + q.Get("channel") + "/p" + q.Get("message_ts")})
	default:
		write(w, map[string]any{"ok": false, "error": "unknown_method"})
	}
}

func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// channel registers one channel in both the list and the by-id lookup, so a full pass and a
// targeted pass see the same thing.
func (f *fakeSlack) channel(id, name string) {
	c := map[string]any{
		"id": id, "name": name, "is_private": false, "is_archived": false,
		"num_members": 3, "created": 1700000000,
		"topic":   map[string]any{"value": "what " + name + " is for"},
		"purpose": map[string]any{"value": ""},
	}
	f.channels = append(f.channels, c)
	f.info[id] = c
}

func (f *fakeSlack) say(channel, ts, user, text string) map[string]any {
	m := map[string]any{"type": "message", "ts": ts, "user": user, "text": text}
	f.history[channel] = append(f.history[channel], m)
	return m
}

// saidTo is what was said in the thread under ts — to everybody when to is "thread", to one
// person by their id otherwise — and "" when nothing was.
func (f *fakeSlack) saidTo(to, thread string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	said := f.inThread
	if to != "thread" {
		said = f.ephemerals
	}
	for _, m := range said {
		if m["thread_ts"] != thread {
			continue
		}
		if to != "thread" && m["user"] != to {
			continue
		}
		return m["text"].(string)
	}
	return ""
}
