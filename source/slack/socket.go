package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/websocket"

	verseapi "github.com/signadot/verse/api"
	"github.com/signadot/verse/client"
)

// Socket Mode: how a click in Slack reaches this program with no Ingress in front of it.
//
// Slack can post a click to a public url, and a verse with no Ingress (staging has none, and is
// not meant to) has none to give it. In Socket Mode this program dials OUT instead: it asks for a
// url with the app-level token (`xapp-…`, `connections:write`), opens a WebSocket to it, and Slack
// sends each event down it as an envelope, which is acknowledged by its id. Slack refreshes the
// connection every few hours by saying `disconnect`; the loop dials again, as it does when the
// socket fails.
//
// A BUTTON CAN ONLY ANSWER A QUESTION. Its value names a question and an option, and a click is
// that answer, made AS THE PERSON WHO CLICKED: users.info turns their Slack id into
// `user:<email>`, and the answer goes to versed's resolve route as them, so versed's permissions
// decide whether they may answer, and a refusal (not an answerer, somebody answered first) comes
// back to them in versed's words, visible to them alone. It is never made as this program's own
// principal: an answer recorded as agent:slack-io would be an answer nobody gave. Anything
// broader than answering is a new kind of button, each with a line saying what it does, because
// a button that could make any request as whoever pressed it is a request its label could lie
// about.
//
// The Slack user comes from an envelope on a socket the app token authenticated, and this program
// then asserts that person to verse as it asserts its own principal; verse trusts it as it trusts
// the CLI's. A principal verse verifies for itself is the fix for both.
type Socket struct {
	s     *Slack
	app   *api // holding the app-level token, which only apps.connections.open takes
	verse string
}

// AnswerAction begins the action id of every button that answers a question.
const AnswerAction = "verse.answer."

// buttonAnswer is what a button's value carries: the question it answers, and with which option.
type buttonAnswer struct {
	Question string `json:"question"`
	Option   string `json:"option"`
}

func answerValue(question, option string) (string, error) {
	b, err := json.Marshal(buttonAnswer{Question: question, Option: option})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Socket is this workspace's Socket Mode listener, answering clicks at the verse at verseAddr.
func (s *Slack) Socket(appToken, verseAddr string) *Socket {
	return &Socket{s: s, app: newAPI(appToken, s.api.base, s.api.cl), verse: verseAddr}
}

// Run keeps a connection open until ctx is done. Slack's disconnect is a request to dial again,
// which is done at once; a failure is dialled again after a backoff that resets once a connection
// holds.
func (k *Socket) Run(ctx context.Context) {
	wait := time.Second
	for ctx.Err() == nil {
		held, err := k.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if held {
			wait = time.Second
		}
		if err == nil {
			continue // asked to reconnect
		}
		k.s.log("slack socket: %v; dialling again in %s", err, wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if !held && wait < time.Minute {
			wait *= 2
		}
	}
}

// session is one connection: dial, then read envelopes until Slack asks for a new one or the
// socket fails. held says whether it got as far as Slack's hello.
func (k *Socket) session(ctx context.Context) (held bool, err error) {
	u, err := k.app.connectionsOpen(ctx)
	if err != nil {
		return false, err
	}
	cfg, err := websocket.NewConfig(u, "https://slack.com")
	if err != nil {
		return false, err
	}
	dial, cancel := context.WithTimeout(ctx, 30*time.Second)
	ws, err := cfg.DialContext(dial)
	cancel()
	if err != nil {
		return false, fmt.Errorf("dialling Socket Mode: %w", err)
	}
	defer ws.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			ws.Close()
		case <-done:
		}
	}()

	for {
		var raw []byte
		if err := websocket.Message.Receive(ws, &raw); err != nil {
			return held, err
		}
		var env struct {
			EnvelopeID string          `json:"envelope_id"`
			Type       string          `json:"type"`
			Payload    json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			k.s.log("slack socket: an envelope that is not JSON: %v", err)
			continue
		}
		// Acknowledged first, before anything is done about it: an envelope Slack does not see
		// acknowledged within a few seconds is sent again, and a click would be answered twice.
		if env.EnvelopeID != "" {
			if err := websocket.JSON.Send(ws, map[string]string{"envelope_id": env.EnvelopeID}); err != nil {
				return held, err
			}
		}
		switch env.Type {
		case "hello":
			held = true
		case "disconnect":
			return held, nil
		case "interactive":
			go k.interactive(ctx, env.Payload) // off the read loop, so acknowledgements keep flowing
		}
	}
}

// interactive answers the question each button in a click names.
func (k *Socket) interactive(ctx context.Context, payload json.RawMessage) {
	var p struct {
		Type string `json:"type"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		ResponseURL string `json:"response_url"`
		Actions     []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(payload, &p); err != nil || p.Type != "block_actions" {
		return
	}
	for _, a := range p.Actions {
		if !strings.HasPrefix(a.ActionID, AnswerAction) {
			continue
		}
		var ans buttonAnswer
		if err := json.Unmarshal([]byte(a.Value), &ans); err != nil || ans.Question == "" || ans.Option == "" {
			k.reply(ctx, p.ResponseURL, "Not answered: this button does not say which question it answers.")
			continue
		}
		k.reply(ctx, p.ResponseURL, k.answer(ctx, p.User.ID, ans))
	}
}

// answer makes the click's answer as the person who clicked, and says what became of it.
func (k *Socket) answer(ctx context.Context, userID string, ans buttonAnswer) string {
	email, err := k.s.api.userEmail(ctx, userID)
	if err != nil {
		return "Not answered: " + err.Error()
	}
	who := "user:" + email
	resp, err := client.New(k.verse, who).Post("/gate/resolve", nil,
		verseapi.Resolve{ID: ans.Question, Choice: ans.Option, By: who})
	if err != nil {
		return "Not answered: " + err.Error()
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return fmt.Sprintf("Answered `%s`: %s.", ans.Question, ans.Option)
	case http.StatusAccepted:
		// A ballot recorded on a question that takes more than one voice; the server says where
		// the tally stands.
		said, _ := io.ReadAll(resp.Body)
		return fmt.Sprintf("`%s`: %s.", ans.Question, strings.TrimSpace(string(said)))
	}
	return "Not answered: " + client.StatusError(resp, "`"+ans.Question+"`").Error()
}

// reply says text to the person who clicked, and to nobody else.
func (k *Socket) reply(ctx context.Context, responseURL, text string) {
	if responseURL == "" {
		return
	}
	if err := k.s.api.respond(ctx, responseURL, text); err != nil {
		k.s.log("slack socket: replying to a click: %v", err)
	}
}
