package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/websocket"

	verseapi "github.com/signadot/verse/api"
	"github.com/signadot/verse/client"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/task"
)

// Socket Mode: how what somebody says in Slack reaches this program with no Ingress in front of it.
//
// Slack can post an event to a public url, and a verse with no Ingress (staging has none, and is
// not meant to) has none to give it. In Socket Mode this program dials OUT instead: it asks for a
// url with the app-level token (`xapp-…`, `connections:write`), opens a WebSocket to it, and Slack
// sends each event down it as an envelope, which is acknowledged by its id. Slack refreshes the
// connection every few hours by saying `disconnect`; the loop dials again, as it does when the
// socket fails.
//
// Once an app is in Socket Mode, Slack sends its Events API deliveries down the socket too, and
// no longer to the request url. They become the same hints the request url's handler sends
// (events.go), so the reflection hears a new message in seconds with no public endpoint.
//
// A QUESTION IS ANSWERED BY REPLYING TO IT. The out half posts a message asking one, and a reply
// in that message's thread saying one of the question's options is that answer — typed plainly, or
// addressed to the bot (`@verse proceed`), which is the same event with a mention in it. No
// button, no dialog, no slash command: a reply renders wherever Slack does, reads the same in a
// notification as on screen, and is what one person would have typed to another.
//
// Two halves, kept apart, because only one of them is allowed to be clever. WHICH question a reply
// answers is decided without reading the words at all: the thread's parent is a message this
// program posted, and it remembers what each of its messages is asking (Out.Asked). WHAT the reply
// says is matched against that question's own options, plus `y`/`n` for the two a gate has unless
// it says otherwise. A reply matching none of them is somebody talking, and is left alone. When
// something interprets prose here one day, it inherits that split: it chooses among the options on
// the record or says it cannot tell, and it never decides which question is meant.
//
// A REPLY ANSWERS THE WORDS IT SAW (9ce3bva0, stage 8). It carries the edit of the ask the message
// shows, so one to an ask whose `then` has changed since is refused, and the redrawn message is
// answered as it now reads. `force <option>` answers yourself an ask a resolver serves for you.
// Under a turn — an ask that takes `reply` — any words are the reply.
//
// AS THE PERSON WHO TYPED IT: users.info turns their Slack id into `user:<email>`, and the answer
// goes to versed's resolve route as them, so versed's permissions decide whether they may answer.
// An answer is said back in the thread, where everyone reading it can see what was decided; a
// refusal — not an answerer, somebody answered first — is said to them alone, in versed's words,
// since it is about them rather than about the question. The question's own record is what the
// message then shows, redrawn by the out half (out.go, body), which Answered asks for at once.
//
// It is never made as this program's own principal: an answer recorded as agent:slack-io would be
// an answer nobody gave. The Slack user comes from an envelope on a socket the app token
// authenticated, and this program then asserts that person to verse as it asserts its own
// principal; verse trusts it as it trusts the CLI's. A principal verse verifies for itself is the
// fix for both.
type Socket struct {
	s   *Slack
	app *api // holding the app-level token, which only apps.connections.open takes
	// as is the client factory io.Controller is handed: one verse, and a request made as
	// whoever Slack says did the thing. nil until Control runs, and then never as this program.
	as func(principal string) *client.Client

	// Hint, when set, receives the hint each Events API delivery calls for, as the request url's
	// handler would send it.
	Hint verseio.Hinter

	// Asking, when set, answers which questions the message at (channel, ts) is asking: the out
	// half's index of its own messages. Without it nothing here answers anything.
	Asking func(channel, ts string) []Asked

	// Answered, when set, is called after a reply answers a question or casts a ballot: the out
	// half's Kick, so the message says so without waiting for its next pass.
	Answered func()
}

// Socket is this workspace's Socket Mode listener, answering clicks at the verse at verseAddr.
func (s *Slack) Socket(appToken string) *Socket {
	return &Socket{s: s, app: newAPI(appToken, s.api.base, s.api.cl)}
}

// Control is io.Controller: the socket, run until ctx is done, answering as whoever typed.
func (k *Socket) Control(ctx context.Context, as func(principal string) *client.Client) error {
	k.as = as
	k.Run(ctx)
	return ctx.Err()
}

var _ verseio.Controller = (*Socket)(nil)

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
		// acknowledged within a few seconds is sent again, and a reply would be answered twice.
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
		case "events_api":
			var d delivery
			if err := json.Unmarshal(env.Payload, &d); err != nil {
				k.s.log("slack socket: an event that does not decode: %v", err)
				continue
			}
			// Off the read loop, so acknowledgements keep flowing: the hint the reflection
			// wants, and the answer somebody may have just typed.
			go k.s.hintFor(ctx, d, k.Hint)
			go k.said(ctx, env.Payload)
		}
	}
}

// said answers the question a reply in a thread is answering, if it is answering one.
//
// A delivery is read TWICE, here and as a hint (events.go), and deliberately so: `delivery` is
// incapable of carrying a payload into the store, which is the law the reflection lives under,
// while an answer is a control act and never a write. Two narrow readers of one envelope, neither
// able to do the other's job.
func (k *Socket) said(ctx context.Context, payload json.RawMessage) {
	var p struct {
		Event struct {
			Type     string `json:"type"`
			Subtype  string `json:"subtype"`
			Channel  string `json:"channel"`
			User     string `json:"user"`
			BotID    string `json:"bot_id"`
			Text     string `json:"text"`
			TS       string `json:"ts"`
			ThreadTS string `json:"thread_ts"`
		} `json:"event"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return
	}
	e := p.Event
	// A person, in a thread, saying something new — said plainly (`message`) or at the bot
	// (`app_mention`), which are one act and arrive as two events. Not this program's own
	// messages, since its receipts are in the same threads; not an edit or a join; and not
	// anything at channel level, which is somebody talking: nothing says which question that
	// would be about.
	if (e.Type != "message" && e.Type != "app_mention") || e.Subtype != "" || e.BotID != "" ||
		e.User == "" || e.ThreadTS == "" || e.ThreadTS == e.TS || k.Asking == nil {
		return
	}
	// ONE MESSAGE, TWO EVENTS. A message that mentions this bot arrives twice — as `message` and
	// as `app_mention` — and handling both would record what somebody said twice, or record it
	// as talk while answering it. The mention is the copy that says it was addressed here, so
	// the plain one is dropped and the mention does the work.
	if e.Type == "message" && k.s.me != "" && strings.Contains(e.Text, "<@"+k.s.me+">") {
		return
	}
	asked := k.Asking(e.Channel, e.ThreadTS)
	if len(asked) == 0 {
		return // not one of this program's questions
	}
	// A thread whose questions have all settled still hears what is said in it — the words go on
	// the questions, where whoever asks why later will look — and tells somebody who addressed
	// the bot what became of the thing they were answering.
	if open := pending(asked); len(open) == 0 {
		noted := k.say(ctx, e.User, asked, e.Text)
		if e.Type == "app_mention" {
			say := "That is settled: " + asked[0].Line + " Nothing left to answer."
			if noted {
				say += " I have noted what you said on the question."
			}
			k.aside(ctx, e.Channel, e.ThreadTS, e.User, say)
		}
		return
	} else {
		asked = open
	}
	// `force <option>`: the person answers an ask their resolver serves for them, themselves
	// (9ce3bva0, stage 5). Said, so it is never done by accident.
	text, force := forcing(e.Text)
	id, option, err := choose(text, asked, e.Type == "app_mention")
	if err != nil {
		// It was meant as an answer and could not be read as one, so the words are kept and the
		// person is told both things: an answer nobody recorded and nobody mentioned is the one
		// outcome they cannot act on.
		say := err.Error() + "."
		if k.say(ctx, e.User, asked, e.Text) {
			say += " I have noted what you said on the question."
		}
		k.aside(ctx, e.Channel, e.ThreadTS, e.User, say)
		return
	}
	// A TURN TAKES WORDS: where the one thing open here is a conversation's turn, what is said is
	// the reply, and the words go with it rather than beside it.
	if id == "" {
		if turn, ok := replying(asked); ok {
			id, option = turn.ID, task.Reply
		}
	}
	if id == "" {
		// Not an answer, so it is talk about the question — and talk about a question belongs ON
		// the question, where whoever reads it later reads it too, rather than only in the
		// window it was typed in. It decides nothing: a say carries no choice.
		noted := k.say(ctx, e.User, asked, e.Text)
		// Silence for the thread at large, since answering people for talking would make every
		// conversation under a question a conversation with a program. Somebody who addressed
		// the bot by name is owed a reply, and "I did not understand you, so I wrote it down"
		// is one.
		if e.Type == "app_mention" {
			say := "Sorry — I did not get that. To answer, say one of " + code(optionsOf(asked)) + "."
			if noted {
				say += " I have noted what you said on the question."
			}
			k.aside(ctx, e.Channel, e.ThreadTS, e.User, say)
		}
		return
	}
	var say string
	if option == task.Reply {
		say = strings.TrimSpace(stripMentions(text))
		if strings.EqualFold(say, task.Reply) {
			say = ""
		}
	}
	said, answered, public := k.answer(ctx, e.User, askedAs(asked, id), option, say, force)
	if answered && k.Answered != nil {
		k.Answered()
	}
	if public {
		k.inThread(ctx, e.Channel, e.ThreadTS, said)
		return
	}
	// What went wrong is about the person who typed it rather than about the question, so the
	// thread does not need it.
	k.aside(ctx, e.Channel, e.ThreadTS, e.User, said)
}

// pending is the questions in a thread that still take an answer.
func pending(asked []Asked) []Asked {
	var out []Asked
	for _, a := range asked {
		if !a.Settled {
			out = append(out, a)
		}
	}
	return out
}

// choose reads a reply as one of the options the thread's questions take: the question and the
// option, nothing at all for a reply that is not an answer, and an error for one that could be
// two.
//
// TWO READINGS, and which one applies is not a matter of degree. A reply to the thread at large
// must SAY an option — one word, the question's own or `y`/`n` — because people talk under these
// messages and no amount of confidence should let a conversation answer a gate by accident.
// Addressed to the bot BY NAME (`@verse approve it pls`), prose is read as well, because that
// message was written to this program and means to be acted on.
//
// What prose may yield is still one of the ids on the record — never an effect of its own — and
// when it could be two, or carries a negation this program has no business resolving, it yields
// nothing and says so. That is the seam a model slots into later: it chooses among the options or
// says it cannot tell, and it never decides which question was meant (Socket.Asking does that,
// without reading a word).
func choose(text string, asked []Asked, addressed bool) (id, option string, err error) {
	said := strings.ToLower(strings.TrimSpace(stripMentions(text)))
	if said == "" {
		return "", "", nil
	}
	if word := strings.Trim(said, ".!?,;:"); !strings.ContainsAny(word, " \t\n") {
		id, option, err = matching(asked, func(opt string) bool {
			return word == strings.ToLower(opt) || slices.Contains(aliases(opt), word)
		})
		if id != "" || err != nil || !addressed {
			return id, option, err
		}
	} else if !addressed {
		return "", "", nil // a sentence in the thread is talk, and talk is not an answer
	}

	// Addressed, and not one bare word: read it as a sentence.
	words := strings.FieldsFunc(said, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	has := func(w string) bool { return slices.Contains(words, w) }
	for _, no := range []string{"not", "dont", "don", "never", "cannot", "cant", "unless", "until"} {
		if has(no) {
			// "do not ship it" and "ship it" differ by one word, and a program that reads them
			// the same answers gates nobody answered. Whoever wrote it can say the word.
			return "", "", fmt.Errorf("I am not sure whether that is a yes or a no")
		}
	}
	return matching(asked, func(opt string) bool {
		if has(strings.ToLower(opt)) {
			return true
		}
		for _, w := range meaning(opt) {
			if has(w) {
				return true
			}
		}
		return false
	})
}

// matching is the one option some test picks out of the thread's questions, or an error when it
// picks more than one — two questions in a thread, each taking `proceed`, is a reply that cannot
// be placed rather than one to place twice.
func matching(asked []Asked, picks func(option string) bool) (id, option string, err error) {
	var could []string
	for _, a := range asked {
		for _, opt := range a.Options {
			if !picks(opt) {
				continue
			}
			if id == "" {
				id, option = a.ID, opt
			} else if opt != option || a.ID != id {
				could = append(could, "`"+opt+"`")
			}
		}
	}
	if len(could) > 0 {
		return "", "", fmt.Errorf("I read that as %s and `%s` both — say one of them on its own",
			strings.Join(could, ", "), option)
	}
	return id, option, nil
}

// meaning is the everyday words for an option a gate did not name itself: the two every gate has
// unless it says otherwise. An option of the gate's own is said by its id, which is the word its
// author chose for it.
func meaning(option string) []string {
	switch option {
	case "proceed":
		return []string{"approve", "approved", "approves", "yes", "yep", "yeah", "ok", "okay",
			"lgtm", "ship", "go", "sure", "please", "agreed", "fine"}
	case "deny":
		return []string{"reject", "rejected", "no", "nope", "refuse", "refused", "block",
			"blocked", "stop", "hold", "veto"}
	}
	return nil
}

// optionsOf is every word the thread's questions take, said once each.
func optionsOf(asked []Asked) []string {
	var out []string
	for _, a := range asked {
		for _, opt := range a.Options {
			if !slices.Contains(out, opt) {
				out = append(out, opt)
			}
		}
	}
	return out
}

// aliases are the other words that ARE an option, said alone: for the two a gate has unless it
// says otherwise, the short one and the plain one. "yes" to "Ask ani to review it?" is the answer
// everybody types, and a reply that has to be spelled `proceed` is a reply people get wrong.
func aliases(option string) []string {
	switch option {
	case "proceed":
		return []string{"y", "yes"}
	case "deny":
		return []string{"n", "no"}
	}
	return nil
}

// stripMentions drops `<@U123>` and the like, so that answering the bot by name is answering.
func stripMentions(text string) string {
	for {
		i := strings.Index(text, "<")
		if i < 0 {
			return text
		}
		j := strings.Index(text[i:], ">")
		if j < 0 {
			return text
		}
		text = text[:i] + text[i+j+1:]
	}
}

// say records what somebody said about the thread's questions, as them, answering nothing. It
// reports whether anything was written, since what it tells them afterwards depends on it.
func (k *Socket) say(ctx context.Context, userID string, asked []Asked, text string) bool {
	text = strings.TrimSpace(stripMentions(text))
	if text == "" || k.as == nil {
		return false
	}
	email, err := k.s.api.userEmail(ctx, userID)
	if err != nil {
		k.s.log("slack socket: who said %q: %v", text, err)
		return false
	}
	who := "user:" + email
	verse := k.as(who)
	noted := false
	for _, a := range asked {
		resp, err := verse.Post("/asks/"+url.PathEscape(a.ID)+"/say", nil, verseapi.GateSay{ID: a.ID, By: who, Say: text})
		if err != nil {
			k.s.log("slack socket: saying on %s: %v", a.ID, err)
			continue
		}
		if resp.StatusCode >= http.StatusBadRequest {
			k.s.log("slack socket: %v", client.StatusError(resp, "saying on "+a.ID))
		} else {
			noted = true
		}
		resp.Body.Close()
	}
	return noted
}

// answer makes the answer as the person who typed it, and says what became of it: whether it
// counted — an answer, or a ballot on a question that takes more than one voice — and whether what
// there is to say belongs in the thread or to them alone.
func (k *Socket) answer(ctx context.Context, userID string, a Asked, option, say string, force bool) (said string, answered, public bool) {
	id := a.ID
	if k.as == nil {
		return "Not answered: this program was not told which verse the question is in", false, false
	}
	email, err := k.s.api.userEmail(ctx, userID)
	if err != nil {
		return "Not answered: " + err.Error(), false, false
	}
	who := "user:" + email
	// THE EDIT THE MESSAGE SHOWED, which is what was consented to: a reply to words since changed
	// is refused by versed rather than counted against the new ones.
	body := verseapi.Resolve{ID: id, Choice: option, By: who, Edit: a.Edit, Force: force}
	if say != "" {
		body.Say = []string{say}
	}
	resp, err := k.as(who).Post("/asks/"+url.PathEscape(id)+"/answer", nil, body)
	if err != nil {
		return "Not answered: " + err.Error(), false, false
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return fmt.Sprintf("Answered `%s`: *%s*, by <@%s>.", id, option, userID), true, true
	case http.StatusAccepted:
		// The server says where the tally stands, and the thread is where the others it waits
		// on will look.
		tally, _ := io.ReadAll(resp.Body)
		return fmt.Sprintf("`%s`: %s (<@%s> said *%s*).", id,
			strings.TrimSpace(string(tally)), userID, option), true, true
	}
	return "Not answered: " + client.StatusError(resp, "`"+id+"`").Error(), false, false
}

// inThread says text where the question was asked, so everyone reading it learns what was decided.
func (k *Socket) inThread(ctx context.Context, channel, thread, text string) {
	if _, _, err := k.s.api.postMessage(ctx, channel, thread, text); err != nil {
		k.s.log("slack socket: saying what became of an answer: %v", err)
	}
}

// aside says text to one person in the thread they typed in, and to nobody else.
func (k *Socket) aside(ctx context.Context, channel, thread, user, text string) {
	if err := k.s.api.ephemeral(ctx, channel, thread, user, text); err != nil {
		k.s.log("slack socket: saying %q to %s: %v", text, user, err)
	}
}

// forcing reads a leading `force`: what remains is the answer, said by somebody answering an ask
// their resolver serves for them.
func forcing(text string) (string, bool) {
	t := strings.TrimSpace(stripMentions(text))
	if rest, ok := strings.CutPrefix(strings.ToLower(t), "force "); ok {
		return strings.TrimSpace(t[len(t)-len(rest):]), true
	}
	return text, false
}

// replying is the one open ask in a thread that takes words, a conversation's turn, where it is the
// only thing open there.
func replying(asked []Asked) (Asked, bool) {
	if len(asked) != 1 || !slices.Contains(asked[0].Options, task.Reply) {
		return Asked{}, false
	}
	return asked[0], true
}

// askedAs is the ask with this id among those a thread is about.
func askedAs(asked []Asked, id string) Asked {
	for _, a := range asked {
		if a.ID == id {
			return a
		}
	}
	return Asked{ID: id}
}
