package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scott-cotton/cli"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/verbs"
)

// `verse ask` is asking somebody something, and following what you asked (docs/asking.md).
//
// An ask is a record at `verse.ask.<id>`, its asker's, written by this verb (POST /asks) and posed
// by the system's own rules. The id is derived from the name the asker gives, as `verse task run
// --name` derives a task's, and every command takes the name or the id.
//
//	verse ask                                       your asks
//	verse ask land                                  one of them, and whom it waits on
//	verse ask land -q "Review verse/ask?" --to ani@example.com --due 48h --on @on.tony
//	verse ask land --withdraw                       take it back
//	verse ask --watch                               a line when one settles, or a question waits on you
//	verse ask --watch --notify                      and on the desktop: what needs you (notify.go)
//
// THE NAME IS THE POSITIONAL in every case, and what to do is a flag.
//
// CHANGED WHILE IT WAITS, by its asker: `--due` moves its date and asks nothing again; `--on`
// or `--to` changes what it asks, so it is asked again as its next edit (PATCH /asks/{id}).

type askConfig struct {
	*cli.Command
	Question  string `cli:"name=question aliases=q desc='ask this: creates the ask under the name'"`
	To        string `cli:"name=to desc='who is asked: a name or a comma-separated list, in the grammar a grant uses; absent, anyone may answer'"`
	Due       string `cli:"name=due desc='when it settles if nobody answers: an RFC 3339 date or a duration from now (48h)'"`
	OnSilence string `cli:"name=on-silence desc='with --due: the option it settles on if nobody answers by then, proceed or deny (the default)'"`
	On        string `cli:"name=on desc='what follows its answer, on your authority: a list of rows read in order, each {match: <a tony match over the answer, {verdict: proceed} or {by: deadline}; absent, any>, then: <one key: {spawn: …}, {answer: {ask: <id>, choice: <option>}} or {ask: {…}}; null or {} does nothing>}, the first that matches running its then; - reads it from stdin, @<file> from a file'"`
	About     string `cli:"name=about desc='the path the ask is about, as a kinded path'"`
	Keep      string `cli:"name=keep desc='how long it stands once settled, as a duration (72h); absent, a week'"`
	Label     string `cli:"name=label desc='what the ask says about itself for whoever watches asks: name=value, comma-separated (team=payments,kind=deploy)'"`
	Withdraw  bool   `cli:"name=withdraw desc='take the ask back: it is removed, and its question withdrawn'"`
	Watch     bool   `cli:"name=watch desc='print a line when an ask of yours settles, or a question starts waiting on you, until stopped'"`
	Since     string `cli:"name=since desc='with --watch: replay from this revision first, so nothing between two runs is missed'"`
	Notify    bool   `cli:"name=notify desc='with --watch: also notify on the desktop when an ask of yours is answered (D-Bus on Linux, Notification Center on macOS)'"`
}

func askCmd() *cli.Command {
	cfg := &askConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "ask").
		WithAliases("asks").
		WithSynopsis("ask — asking somebody something: bare lists your asks, <name|id> shows one, " +
			"[<name>] -q <question> [--to <who>] [--due <when>] [--on <rows>] [--keep <duration>] asks, <name|id> --withdraw takes back, --watch follows").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *askConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) > 1 {
		return fmt.Errorf("%w: ask takes one name, and %q is after it", cli.ErrUsage, args[1])
	}
	name := ""
	if len(args) == 1 {
		name = args[0]
	}
	asking := cfg.To != "" || cfg.Due != "" || cfg.On != "" || cfg.About != "" || cfg.Keep != "" || cfg.Label != "" || cfg.OnSilence != ""
	if cfg.Watch {
		if name != "" || cfg.Question != "" || cfg.Withdraw || asking {
			return fmt.Errorf("%w: --watch follows all your asks, and takes nothing else", cli.ErrUsage)
		}
		return cfg.watch(cc)
	}
	if cfg.Since != "" || cfg.Notify {
		return fmt.Errorf("%w: --since and --notify go with --watch", cli.ErrUsage)
	}
	switch {
	case name == "" && cfg.Question != "" && !cfg.Withdraw:
		return cfg.create(cc, "")
	case name == "" && (cfg.Withdraw || asking):
		return fmt.Errorf("%w: a change or a withdrawal names the ask: `verse ask <name|id> --withdraw`", cli.ErrUsage)
	case name == "":
		return listAsks(cc)
	case cfg.Question != "":
		if cfg.Withdraw {
			return fmt.Errorf("%w: --question asks and --withdraw takes back; pass one", cli.ErrUsage)
		}
		return cfg.create(cc, name)
	case cfg.About != "" || cfg.Keep != "" || cfg.Label != "" || cfg.OnSilence != "":
		return fmt.Errorf("%w: --about, --keep, --label and --on-silence say what a new ask is, so they go with --question", cli.ErrUsage)
	case asking:
		if cfg.Withdraw {
			return fmt.Errorf("%w: a change and --withdraw; pass one", cli.ErrUsage)
		}
		return cfg.change(cc, name)
	case cfg.Withdraw:
		return withdrawAsk(cc, name)
	default:
		return showAsk(cc, name)
	}
}

// ── asking ───────────────────────────────────────────────────────────────────

func (cfg *askConfig) create(cc *cli.Context, name string) error {
	var on entity.Payload
	var err error
	if cfg.On != "" {
		if on, err = onOf(cc, cfg.On); err != nil {
			return err
		}
	}
	body, err := newAsk(api.AskSpec{Name: name, Question: cfg.Question, To: cfg.To, Due: cfg.Due,
		OnSilence: cfg.OnSilence, About: cfg.About, Keep: cfg.Keep, Label: cfg.Label, On: on})
	if err != nil {
		return err
	}
	said, err := createAsk(apiClient(), body)
	if err != nil {
		return err
	}
	fmt.Fprintln(cc.Out, said)
	return nil
}

// newAsk is the body POST /asks takes for s, as of now; a word it refuses is a usage error naming
// its flag.
func newAsk(s api.AskSpec) (api.Ask, error) {
	body, err := api.NewAsk(s, time.Now())
	return body, usage(err)
}

// usage is err, a refusal of a word naming its field (api.NewAsk), as a usage error naming its
// flag; nil for nil.
func usage(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: --%w", cli.ErrUsage, err)
}

// createAsk asks it, and says what it was asked as.
func createAsk(c *client.Client, body api.Ask) (string, error) {
	resp, err := c.Post("/asks", nil, body)
	if err != nil {
		return "", fmt.Errorf("ask: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", statusError(resp, "ask")
	}
	var out api.Asked
	decodeBody(resp, &out)
	return api.AskRaised(out.ID, body.Question), nil
}

// answerersOf reads --to (api.AnswerersOf).
func answerersOf(s string) ([]string, error) {
	to, err := api.AnswerersOf(s)
	return to, usage(err)
}

// dateOf reads --due (api.DueOf).
func dateOf(s string, now time.Time) (string, error) {
	due, err := api.DueOf(s, now)
	return due, usage(err)
}

// onOf reads --on as a document in the input format: inline, `-` for stdin, or `@<file>`. What it
// may say is the route's to check, which answers with what an ask runs.
func onOf(cc *cli.Context, s string) (entity.Payload, error) {
	src := []byte(s)
	switch {
	case s == "-":
		b, err := io.ReadAll(cc.In)
		if err != nil {
			return nil, fmt.Errorf("--on: %w", err)
		}
		src = b
	case strings.HasPrefix(s, "@"):
		b, err := os.ReadFile(s[1:])
		if err != nil {
			return nil, fmt.Errorf("--on: %w", err)
		}
		src = b
	}
	return onDoc(src, inFmt())
}

// onDoc reads what follows an answer from a document in the format in (verbs.OnDoc).
func onDoc(src []byte, in codec.Format) (entity.Payload, error) {
	n, err := verbs.OnDoc(src, in)
	if err != nil {
		return nil, fmt.Errorf("%w: --on: %v", cli.ErrUsage, err)
	}
	return n, nil
}

// ── reading ──────────────────────────────────────────────────────────────────

// questionQuery is what `verse question` lists: THE QUESTIONS PUT TO YOU, with those a resolver
// serves for you where served, unless all asks for every pending ask you may read.
func questionQuery(all, served bool) url.Values {
	if all {
		return url.Values{"all": {"true"}}
	}
	q := url.Values{"waiting": {"me"}}
	if served {
		q.Set("served", "true")
	}
	return q
}

// asksAt reads GET /asks under q, each record as the ask it is: bare, the caller's own asks;
// `waiting=me`, the questions waiting on them; `all=true`, the docket.
func asksAt(q url.Values) ([]entity.Entity, error) { return asksFrom(apiClient(), q) }

// asksFrom is asksAt through c.
func asksFrom(c *client.Client, q url.Values) ([]entity.Entity, error) {
	resp, err := c.Get("/asks", q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return nil, errors.New(msg)
	}
	node, err := codec.ParseNode(body, respFmt(resp))
	if err != nil {
		return nil, fmt.Errorf("decoding the asks: %w", err)
	}
	return mirror.AsksIn(node), nil
}

// resolve finds the ask a name or an id names: the id itself, or the one ask of the caller's
// under that name, read alone (GET /asks?ask=). Several under one name are listed rather than
// guessed between.
func resolve(arg string) (entity.Entity, error) { return resolveAsk(apiClient(), arg) }

// resolveAsk is resolve through c.
func resolveAsk(c *client.Client, arg string) (entity.Entity, error) {
	asks, err := asksFrom(c, api.AskQuery(arg))
	if err != nil {
		return entity.Entity{}, fmt.Errorf("ask: %w", err)
	}
	e, err := mirror.AskNamed(asks, arg)
	if err != nil {
		return entity.Entity{}, fmt.Errorf("ask: %w", err)
	}
	return e, nil
}

func listAsks(cc *cli.Context) error {
	me, err := principal("")
	if err != nil {
		return err
	}
	return printAsk(cc, func() (*ir.Node, string, error) { return askList(apiClient(), me) })
}

func showAsk(cc *cli.Context, arg string) error {
	return printAsk(cc, func() (*ir.Node, string, error) { return askShown(apiClient(), arg) })
}

// printAsk prints what an ask read answered: a sentence, or the document.
func printAsk(cc *cli.Context, read func() (*ir.Node, string, error)) error {
	node, said, err := read()
	if err != nil {
		return err
	}
	if node == nil {
		fmt.Fprintln(cc.Out, said)
		return nil
	}
	return printIR(cc, node)
}

// askList is me's asks as the listing shows them, or the sentence that says there are none.
func askList(c *client.Client, me string) (*ir.Node, string, error) {
	asks, err := asksFrom(c, nil)
	if err != nil {
		return nil, "", fmt.Errorf("ask: %w", err)
	}
	if len(asks) == 0 {
		return nil, fmt.Sprintf("%s has no asks standing", me), nil
	}
	return mirror.AskRows(asks), "", nil
}

// askShown is one ask, named or numbered, as `verse ask <name|id>` shows it.
func askShown(c *client.Client, arg string) (*ir.Node, string, error) {
	e, err := resolveAsk(c, arg)
	if err != nil {
		return nil, "", err
	}
	row := mirror.AskShown(e)
	return row.Payload(), "", nil
}

// ── changing ─────────────────────────────────────────────────────────────────

// change is `verse ask <name|id> --due …`, `--on …` or `--to …`: the asker changing an ask while
// it waits. It names the edit it read, so a change to an ask that moved on meanwhile is refused.
func (cfg *askConfig) change(cc *cli.Context, arg string) error {
	var ch askEdit
	var err error
	if cfg.Due != "" {
		if ch.Due, err = dateOf(cfg.Due, time.Now()); err != nil {
			return err
		}
	}
	if cfg.To != "" {
		if ch.To, err = answerersOf(cfg.To); err != nil {
			return err
		}
	}
	if cfg.On != "" {
		if ch.On, err = onOf(cc, cfg.On); err != nil {
			return err
		}
	}
	said, err := changeAsk(apiClient(), arg, ch)
	if err != nil {
		return err
	}
	fmt.Fprintln(cc.Out, said)
	return nil
}

// askEdit is a change to a waiting ask, read: a date, answerers in the grammar, what follows.
type askEdit struct {
	Due string
	To  []string
	On  entity.Payload
}

// changeAsk changes the ask arg names, as the edit it read, and says what became of it.
func changeAsk(c *client.Client, arg string, ch askEdit) (string, error) {
	e, err := resolveAsk(c, arg)
	if err != nil {
		return "", err
	}
	body := api.AskChange{Edit: mirror.EditOf(e.Payload), Due: ch.Due, To: ch.To, On: ch.On}
	resp, err := c.Patch("/asks/"+url.PathEscape(e.Ref.Name()), body)
	if err != nil {
		return "", fmt.Errorf("ask: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", statusError(resp, "ask")
	}
	var out api.Asked
	decodeBody(resp, &out)
	if out.Edit != body.Edit {
		return fmt.Sprintf("asked %s again, as edit %d", out.ID, out.Edit), nil
	}
	return fmt.Sprintf("moved %s to %s", out.ID, body.Due), nil
}

// ── withdrawing ──────────────────────────────────────────────────────────────

func withdrawAsk(cc *cli.Context, arg string) error {
	said, err := withdrawAskVia(apiClient(), arg)
	if err != nil {
		return err
	}
	fmt.Fprintln(cc.Out, said)
	return nil
}

// withdrawAskVia takes back the ask arg names, and says so.
func withdrawAskVia(c *client.Client, arg string) (string, error) {
	e, err := resolveAsk(c, arg)
	if err != nil {
		return "", err
	}
	resp, err := c.Delete("/asks/"+url.PathEscape(e.Ref.Name()), nil)
	if err != nil {
		return "", fmt.Errorf("ask: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return "", statusError(resp, "ask --withdraw")
	}
	return "withdrew " + e.Ref.Name(), nil
}

// ── following ────────────────────────────────────────────────────────────────

// watch prints a line when an ask of the caller's is made, asked again, answered or goes. What
// waits on them is `verse question --watch`'s: each side follows its own.
func (cfg *askConfig) watch(cc *cli.Context) error {
	return watchAsks(cc, cfg.Since, cfg.Notify, true, false)
}

// watchAsks is the one subscription both sides follow, over the docket, where every ask stands
// whoever raised it: asks says what becomes of the caller's asks, questions what starts waiting on
// them and settles. It runs until stopped, with no time limit of its own. notify also says what
// needs the caller on the desktop (notify.go).
func watchAsks(cc *cli.Context, since string, notify, asks, questions bool) error {
	me, err := principal("")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(cc.Go)
	defer cancel()
	var mu sync.Mutex
	say := func(rev, line string) {
		mu.Lock()
		defer mu.Unlock()
		if rev == "" {
			fmt.Fprintln(cc.Out, line)
			return
		}
		fmt.Fprintf(cc.Out, "@%s %s\n", rev, line)
	}
	w := newAskWatch(apiClient(), me, asks, questions)
	if notify {
		send, stop, err := desktopAlerts(say)
		if err != nil {
			return err
		}
		defer stop()
		w.alert = send
	}
	return w.follow(ctx, since, say)
}

// askRetryMax is the longest the watch waits between two tries at a verse that stopped answering.
const askRetryMax = 30 * time.Second

// follow reads the watch route until ctx ends. A stream that ends is followed again from the last
// revision it carried, so a restarted verse costs the watch a line and nothing it would have said.
//
// FROM NOW, THE WATCH OPENS WITH WHAT STANDS (`init`). A frame's `to` is the record as the
// watch's own copy holds it, and a watch opened without what stands holds only what was written
// since, so a record met that way is missing every field written before. With `init` the copy
// starts whole, and so is every record it answers. A cursor says what comes first instead, and the
// route takes one or the other.
func (w *askWatch) follow(ctx context.Context, since string, say func(rev, line string)) error {
	w.since = since
	cursor, opened, down := since, false, false
	backoff := time.Second
	for {
		q := url.Values{"at": {entity.VerseRef(mirror.AskSystem).KPath() + ".*"}, "wait-if-absent": {"true"}}
		if cursor != "" {
			q.Set("since", cursor)
		} else {
			q.Set("init", "true")
		}
		w.mu.Lock()
		w.fromNow = cursor == ""
		w.mu.Unlock()
		w.catchUp()
		carried := false
		began, err := followFrames(ctx, w.c, q, w.opened, func(f askFrame) { carried = true; w.frame(f, say) })
		if ctx.Err() != nil {
			return nil
		}
		// What never opened is the verse refusing or not answering: said as an error the first
		// time, and waited out once a stream has been followed, when it is a verse coming back.
		var refused *watchRefused
		switch {
		case !began && !opened:
			return err
		case !began && errors.As(err, &refused) && refused.status < http.StatusInternalServerError:
			return err
		case !began:
			if !down {
				say("", fmt.Sprintf("the verse is not answering (%v); trying again until it does", err))
				down = true
			}
			if !waitOut(ctx, &backoff) {
				return nil
			}
			continue
		}
		if down {
			say("", "the verse answers again")
			down = false
		}
		opened = true
		// A stream that ends having carried nothing is waited on before the next, so a verse that
		// answers and closes at once is not asked again as fast as it can answer.
		if carried {
			backoff = time.Second
		} else if !waitOut(ctx, &backoff) {
			return nil
		}
		why := "the verse closed it"
		if err != nil {
			why = err.Error()
		}
		w.mu.Lock()
		last, hasLast := w.last, w.hasLast
		w.mu.Unlock()
		switch {
		case hasLast:
			// FROM THE COMMIT IT MET LAST, AGAIN: the stream may have ended between two frames of
			// it, and a frame met twice says nothing twice (frame says what changed from the
			// record it holds).
			say(fmt.Sprint(last), fmt.Sprintf("the stream ended (%s); following from here", why))
			cursor = fmt.Sprint(max(last-1, 0))
		case cursor != "":
			say("", fmt.Sprintf("the stream ended (%s); following from --since %s again", why, cursor))
		default:
			// No revision to follow from: the watch had met nothing, so it opens from now again,
			// and what happens before it does is not said.
			say("", fmt.Sprintf("the stream ended (%s) before it carried a revision; following from now, "+
				"so what happens meanwhile is not said", why))
		}
	}
}

// waitOut waits out the backoff, doubling it up to askRetryMax, and says whether ctx is still live.
func waitOut(ctx context.Context, backoff *time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(*backoff):
	}
	*backoff = min(*backoff*2, askRetryMax)
	return true
}

// askFrame is one change as the watch route streams it, or a gap: commits the route did not
// deliver before rev.
type askFrame struct {
	at, op, rev string
	to          entity.Payload
	gap         int
}

// watchRefused is the watch route answering with a status rather than a stream.
type watchRefused struct {
	status int
	err    error
}

func (e *watchRefused) Error() string { return e.err.Error() }
func (e *watchRefused) Unwrap() error { return e.err }

// followFrames reads the watch route under q and hands each frame to fn, until the stream ends or
// ctx does. opened, where set, is called once the route has answered with a stream, before its
// first frame. began says whether a stream was answered at all: a verse refusing the watch, or not
// answering, is not a stream that ended.
func followFrames(ctx context.Context, c *client.Client, q url.Values, opened func(), fn func(askFrame)) (began bool, err error) {
	resp, err := c.With(ctx).Get("/watch", q)
	if err != nil {
		if ctx.Err() != nil {
			return false, nil
		}
		return false, fmt.Errorf("ask --watch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return false, &watchRefused{status: resp.StatusCode, err: statusError(resp, "ask --watch")}
	}
	if opened != nil {
		opened()
	}
	in := streamFmt(resp)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		n, err := codec.ParseNode([]byte(data), in)
		if err != nil {
			continue
		}
		f := askFrame{at: entity.Str(n, "at"), op: entity.Str(n, "op"), to: entity.Get(n, "to")}
		if r := entity.Get(n, "rev"); r != nil && r.Int64 != nil {
			f.rev = fmt.Sprint(*r.Int64)
		}
		if g := entity.Get(n, "gap"); g != nil && g.Int64 != nil {
			f.gap = int(*g.Int64)
		}
		fn(f)
	}
	if ctx.Err() != nil {
		return true, nil
	}
	return true, sc.Err()
}

// askWatch turns frames into lines. It holds each ask as its frames say it stands, and says what
// changed from the record it held before: a frame is a level, and the same one met twice says
// nothing the second time.
type askWatch struct {
	// c is the verse the watch follows, and asks what a frame cannot say.
	c  *client.Client
	me string
	// asks and questions are the two sides a watch says: what becomes of the caller's asks, and
	// what waits on them.
	asks, questions bool
	// askVerse says whether a pending question waits on the caller, for a waitingOn the watch
	// cannot read itself (waitsOnMe).
	askVerse func(id string) bool
	mu       sync.Mutex
	// recs is each ask as its last frame said it stands: kept so that a change is told from the
	// record before it, and so that a removal, which carries no record, is known as the caller's.
	recs map[string]entity.Payload
	// answered is, per ask of the caller's, the edit its answer was said for: an ask is answered
	// once per asking, whatever is written on it after.
	answered map[string]int
	seen     map[string]bool // questions said to be waiting on the caller
	// fromNow is whether the stream opened with what stands rather than from a cursor: an ask it
	// meets first is then either standing when it opened or just asked, and an answer on it is not
	// news.
	fromNow bool
	// last is the revision of the last frame met, which a stream that ends is followed again from;
	// since is the cursor the watch opened at, until it meets one.
	last    int64
	hasLast bool
	since   string
	now     func() time.Time
	// alert, where the watch was asked to notify, is told what needs the caller: a question
	// waiting on them, an ask of theirs answered; and a question settling, to close its
	// notification.
	alert func(alert)
	// catching is whether the stream is still delivering what stood when it opened, or the history
	// a cursor replays: frames that arrive back to back, until the stream, once open, has been quiet
	// for quietFor, or has been open for catchUpMax on a docket that is never quiet. What it meets
	// then is counted in standing and answeredSince, and said on the desktop as one summary once it
	// is caught up, not as a burst of notifications, urgent ones among them. catchGen tells one
	// stream's timers from the next's.
	catching      bool
	catchGen      int
	quiet, capped *time.Timer
	quietFor      time.Duration
	catchUpMax    time.Duration
	standing      map[string]bool
	// summarized are the questions the last summary counted as waiting, still waiting: the summary
	// is closed once the last of them settles, as a question's own notification is.
	summarized    map[string]bool
	answeredSince int
}

func newAskWatch(c *client.Client, me string, asks, questions bool) *askWatch {
	return &askWatch{c: c, me: me, asks: asks, questions: questions, now: time.Now,
		askVerse: func(id string) bool { return waitingOnMe(c, id) },
		recs:     map[string]entity.Payload{}, answered: map[string]int{}, seen: map[string]bool{},
		quietFor: askCaughtUp, catchUpMax: askCatchUpMax, standing: map[string]bool{}, summarized: map[string]bool{}}
}

// askCaughtUp is how long a stream is quiet before the watch takes it to have delivered what
// stood, or what it replays: those frames are sent back to back, and what follows is news.
const askCaughtUp = time.Second

// catchUpKey keys the catching up's summary, which no question's id can be: a later summary
// replaces it, and it is closed once every question it counted has settled.
const catchUpKey = "#caught-up"

// askCatchUpMax is the longest catching up lasts once a stream has opened: on a docket that
// changes more often than askCaughtUp, the stream is never quiet, and what follows is news all the
// same.
const askCatchUpMax = 10 * time.Second

// catchUp starts the catching up a stream about to be opened does, where the watch notifies:
// what it meets is summed, not notified. Its clocks start when the stream opens (opened), so the
// connect and the verse assembling what stands are not counted as quiet.
func (w *askWatch) catchUp() {
	if w.alert == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.catching = true
	w.catchGen++
	w.stopCatchClocks()
}

// opened starts the catching up's clocks, as the stream answers: quiet for quietFor, or open for
// catchUpMax, ends it.
func (w *askWatch) opened() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.catching {
		return
	}
	gen := w.catchGen
	end := func() { w.caughtUpFrom(gen) }
	w.quiet = time.AfterFunc(w.quietFor, end)
	w.capped = time.AfterFunc(w.catchUpMax, end)
}

func (w *askWatch) stopCatchClocks() {
	for _, t := range []*time.Timer{w.quiet, w.capped} {
		if t != nil {
			t.Stop()
		}
	}
	w.quiet, w.capped = nil, nil
}

// caughtUpFrom ends the catching up the stream gen began, and not a later one's.
func (w *askWatch) caughtUpFrom(gen int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if gen == w.catchGen {
		w.caughtUpLocked()
	}
}

// caughtUp ends the catching up, and says on the desktop, once and quietly, what it met: the
// questions still waiting on the caller, and the asks of theirs answered.
func (w *askWatch) caughtUp() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.caughtUpLocked()
}

func (w *askWatch) caughtUpLocked() {
	if !w.catching {
		return
	}
	w.catching = false
	w.stopCatchClocks()
	var parts, poses []string
	if n := len(w.standing); n > 0 {
		parts = append(parts, plural(n, "question waits", "questions wait")+" on you")
		for id := range w.standing {
			w.summarized[id] = true
			poses = append(poses, poseOf(w.recs[id]))
		}
		slices.Sort(poses)
		if len(poses) > 3 {
			poses = append(poses[:3], "…")
		}
	}
	if w.answeredSince > 0 {
		parts = append(parts, plural(w.answeredSince, "ask of yours was", "asks of yours were")+" answered")
	}
	clear(w.standing)
	w.answeredSince = 0
	if len(parts) == 0 {
		return
	}
	w.notify(alert{title: "verse: " + strings.Join(parts, "; "), body: strings.Join(poses, "\n"), key: catchUpKey})
}

// plural is n with one or many, as n says.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (w *askWatch) frame(f askFrame, say func(rev, line string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.catching && w.quiet != nil {
		w.quiet.Reset(w.quietFor)
	}
	// A GAP is commits the route dropped for a watcher that fell behind: what they changed is not
	// here, so the watch says so, and from where to replay them.
	if f.gap > 0 {
		line := fmt.Sprintf("gap: %d commits were not delivered", f.gap)
		switch {
		case w.hasLast:
			line += fmt.Sprintf("; replay them with --since %d", w.last)
		case w.since != "":
			line += "; replay them with --since " + w.since
		}
		say(f.rev, line)
		return
	}
	if r, err := strconv.ParseInt(f.rev, 10, 64); err == nil {
		w.last, w.hasLast = r, true
	}
	segs, err := entity.PathSegs(f.at)
	if err != nil || len(segs) != 3 || segs[1] != mirror.AskSystem {
		return
	}
	id := segs[2]
	prev := w.recs[id]
	gone := f.op == "delete" || f.to == nil || f.to.Type == 0
	rec := f.to
	if gone {
		rec = nil
		delete(w.recs, id)
	} else {
		w.recs[id] = rec
	}
	if w.asks {
		w.saidOfMine(id, prev, rec, f.rev, say)
	}
	// An ask waiting on the caller, whoever raised it: when it starts to, and when it settles.
	if gone || !w.questions {
		return
	}
	status := entity.Str(rec, "status")
	switch {
	case status == mirror.QuestionPending && !w.seen[id] && w.waits(id, rec):
		w.seen[id] = true
		line := fmt.Sprintf("waiting on you %s: %s", id, poseOf(rec))
		if s := entity.Str(rec, "subject"); s != "" {
			line += " (about " + s + ")"
		}
		if by := servedBy(w.c, id); by != "" {
			line += " (served by " + by + ")"
		}
		say(f.rev, line)
		if w.catching {
			w.standing[id] = true
		} else {
			w.notify(alert{title: "verse: a question waits on you", body: poseOf(rec) + "\n" + id, urgent: true, key: id})
		}
	case status != "" && status != mirror.QuestionPending && w.seen[id]:
		delete(w.seen, id)
		delete(w.standing, id)
		say(f.rev, fmt.Sprintf("settled %s: %s", id, status))
		// It no longer needs the caller: its notification is closed, not followed by another.
		w.notify(alert{key: id, withdraw: true})
		if w.summarized[id] {
			delete(w.summarized, id)
			if len(w.summarized) == 0 {
				w.notify(alert{key: catchUpKey, withdraw: true})
			}
		}
	}
}

// saidOfMine says what became of an ask of the caller's, from the record it stood as before (nil
// where the watch had not met it) and as it stands now (nil once it is gone): asked, asked again,
// answered, gone.
func (w *askWatch) saidOfMine(id string, prev, rec entity.Payload, rev string, say func(rev, line string)) {
	mine := func(p entity.Payload) bool { return p != nil && mirror.PrincipalFrom(p) == w.me }
	switch {
	case rec == nil:
		if mine(prev) {
			delete(w.answered, id)
			say(rev, fmt.Sprintf("%s %s", w.removal(prev), id))
		}
		return
	case !mine(rec):
		return
	}
	edit := mirror.EditOf(rec)
	switch {
	case prev != nil && mirror.EditOf(prev) != 0 && edit > mirror.EditOf(prev):
		say(rev, fmt.Sprintf("asked again %s, as edit %d: %s", id, edit, entity.Str(rec, "question")))
	case prev == nil && entity.Str(rec, "status") == "":
		say(rev, api.AskRaised(id, entity.Str(rec, "question")))
	}
	answer := entity.Str(rec, "answer")
	if said, ok := w.answered[id]; answer == "" || (ok && said == edit) {
		return
	}
	w.answered[id] = edit
	if prev != nil || !w.fromNow {
		say(rev, fmt.Sprintf("answered %s %s: %s", id, answer, entity.Str(rec, "question")))
		if w.catching {
			w.answeredSince++
		} else {
			w.notify(alert{title: "verse: answered " + answer, body: entity.Str(rec, "question") + "\n" + id})
		}
	}
}

// notify hands a to the desktop, where the watch was asked to.
func (w *askWatch) notify(a alert) {
	if w.alert != nil {
		w.alert(a)
	}
}

// askExpirySkew is how far the watcher's clock may stand behind the verse's, which removes an ask
// at its expiry by its own.
const askExpirySkew = time.Minute

// removal is what an ask's going was, from the record it last stood as: a settled ask goes at the
// expiry written on it (versed/askttl.go), and one taken away before then, or while it waited, was
// withdrawn. Replayed long after, a settled ask withdrawn before its expiry reads as expired: the
// removal carries no date, and the watch has only its own clock to read.
func (w *askWatch) removal(prev entity.Payload) string {
	at, err := time.Parse(time.RFC3339, entity.Str(prev, "expires"))
	if err == nil && entity.Str(prev, "status") != mirror.QuestionPending && !w.now().Add(askExpirySkew).Before(at) {
		return "expired"
	}
	return "withdrawn"
}

// servedBy is the resolver serving an ask for the caller, "" where none does or the verse did not
// say (9ce3bva0, stage 5): the watch shows such an ask, and says it is served.
func servedBy(c *client.Client, id string) string {
	resp, err := c.Get("/asks/"+url.PathEscape(id)+"/served", nil)
	if err != nil {
		return ""
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return ""
	}
	var out api.AskServed
	decodeClose(resp, &out)
	return out.Resolver
}

// waits says whether a pending question waits on the caller: from the frame, as it stood at its
// revision, where waitsOnMe can tell, and otherwise by asking the verse as it stands now.
func (w *askWatch) waits(id string, q entity.Payload) bool {
	waits, decided := w.waitsOnMe(q)
	if !decided && w.askVerse != nil {
		return w.askVerse(id)
	}
	return waits
}

// waitsOnMe says whether a question waits on the caller: its waitingOn names them, or names every
// principal of their kind (`user:*`) or everyone, which is how a vote, a conversation's turn and a
// task's own question wait on whoever may answer (mirror.WaitingOnField, which has expanded any
// group already). A wildcard does not reach the asker, who answers their own ask only where they
// named themselves. Who may write a place (`audience:`) is the verse's to say: where the line
// names one and nothing here matched, decided is false.
func (w *askWatch) waitsOnMe(q entity.Payload) (waits, decided bool) {
	names := entity.Strs(q, "waitingOn")
	if s := entity.Str(q, "waitingOn"); s != "" {
		names = append(names, s)
	}
	if slices.Contains(names, w.me) {
		return true, true
	}
	me, err := entity.ParsePrincipal(w.me)
	if err != nil || mirror.PrincipalFrom(q) == w.me {
		return false, true
	}
	decided = true
	for _, n := range names {
		g, err := entity.ParseGrantee(n)
		switch {
		case err != nil:
		case g.Kind == entity.Everyone, g.Kind == me.Kind && g.ID == entity.Everyone:
			return true, true
		case g.Kind == entity.AudienceKind:
			decided = false
		}
	}
	return false, decided
}

// waitingOnMe asks the verse whether a pending question waits on the caller: whether it is among
// the questions put to them (GET /asks?waiting=me), served ones included.
func waitingOnMe(c *client.Client, id string) bool {
	qs, err := asksFrom(c, url.Values{"waiting": {"me"}, "served": {"true"}})
	if err != nil {
		return false
	}
	for _, q := range qs {
		if q.Ref.Name() == id {
			return true
		}
	}
	return false
}

func poseOf(q entity.Payload) string {
	if p := entity.Str(q, "pose"); p != "" {
		return p
	}
	return entity.Str(q, "reason")
}
