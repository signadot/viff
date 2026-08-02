package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/scott-cotton/cli"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
)

// ── HTTP helpers (all client commands connect to g.Addr) ─────────────────────

// urlFor builds a request URL, folding in the global --scope so every client
// command operates in the selected scope (baseline by default).
func urlFor(path string, q url.Values) string {
	if g.Scope != "" {
		if q == nil {
			q = url.Values{}
		}
		q.Set("scope", g.Scope)
	}
	// Ask for the output format on every request, as a query parameter rather than an
	// Accept header. An SSE response must answer Accept: text/event-stream, so the header
	// cannot say anything about what is INSIDE the frames — the parameter is the one lever
	// that works the same on a streaming and a non-streaming route.
	if out, err := outFmt(); err == nil {
		if q == nil {
			q = url.Values{}
		}
		q.Set(codec.FormatParam, out.String())
	}
	// Build the URL rather than concatenating it, so the path gets ESCAPED. Ids reach here
	// straight from the command line, so they may contain anything a shell will pass along.
	// A "#" is the one that punishes concatenation: it starts a URL fragment, which silently
	// truncates the path (`/processes/run-x#d1/events` requests `/processes/run-x`) and takes
	// the query string with it, since the "?" would land inside the fragment. The request
	// then succeeds against the WRONG route, with no scope and no format. url.URL.String()
	// escapes it to %23 and the server 404s, which is what a bad id should do.
	//
	// Gate ids no longer carry one — they delimit with process.GateSep, which is unreserved
	// and reaches the route as itself — so this is now a guard against a hostile id rather
	// than the everyday path. It stays because the everyday path is not the dangerous one.
	u := url.URL{Scheme: "http", Host: g.Addr, Path: path}
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func httpGet(path string, q url.Values) (*http.Response, error) {
	return verse.Get(urlFor(path, q))
}

// httpPost sends a document in the wire format (see wireFmt — the input format when verse
// can render it, else tony) and asks for the output format back.
//
// Asking matters even though the client re-renders anyway: it is what lets the server send
// tony once it can, so the round trip stops being lossy — a tagged charter survives as tags
// rather than being flattened to the JSON key form on the way out and back.
func httpPost(path string, body any) (*http.Response, error) {
	f := wireFmt()
	b, err := codec.Marshal(body, f)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, urlFor(path, nil), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	setFormatHeaders(req, f)
	return verse.Do(req)
}

// httpPostNode posts an already-built document. It exists so a command that has parsed a
// file does not have to render it back to bytes in some intermediate format on the way out
// — a tagged charter goes to the server AS tags, rather than being flattened through JSON
// and rebuilt from the key form at the far end.
func httpPostNode(path string, node *ir.Node) (*http.Response, error) {
	f := wireFmt()
	b, err := codec.NodeBytes(node, f)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, urlFor(path, nil), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	setFormatHeaders(req, f)
	return verse.Do(req)
}

// setFormatHeaders declares what is being sent and what is wanted back. The Accept lists
// tony as a fallback so a server that cannot produce the preferred format still answers
// with something this client can read, rather than failing the negotiation.
//
// It used to name JSON there. Doing that now would ask for the one rendering verse
// refuses, and earn a 406 in exactly the situation the fallback exists to survive.
func setFormatHeaders(req *http.Request, in codec.Format) {
	req.Header.Set("Content-Type", codec.MediaType(in))
	out, err := outFmt()
	if err != nil {
		out = codec.Tony
	}
	req.Header.Set("Accept", codec.MediaType(out)+", "+codec.MediaTony+";q=0.5")
}

// printBody renders a response in the format the operator asked for with -o.
//
// It goes through the IR rather than through map[string]any, which is what makes the
// server's FIELD ORDER survive: decoding to a map and re-encoding sorts keys, so the old
// path silently alphabetised every response.
//
// A 4xx/5xx is surfaced as an ERROR carrying the server's message — which is usually plain
// text (http.Error), so parsing it as a document fails and it would otherwise be swallowed
// entirely, leaving a rejected command looking like it succeeded. Callers that handle a
// specific status (404, 201, 204) check it before calling this, so they are unaffected.
func printBody(cc *cli.Context, resp *http.Response) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return errors.New(msg)
	}
	out, err := outFmt()
	if err != nil {
		return err
	}
	in := respFmt(resp)
	node, err := codec.ParseNode(body, in)
	if err != nil {
		return fmt.Errorf("decoding the response: %w", err)
	}
	if in != out {
		// The response's layout (brackets, block literals) describes how the SERVER wrote
		// it, not the value. Asking for tony and getting `[ { name: x } ]` back — JSON's
		// shape in tony's syntax — is not what -o tony means.
		node = codec.StripLayout(node)
	}
	if colorize(cc.Out) {
		return codec.EncodeNodeColor(cc.Out, node, out)
	}
	if err := codec.EncodeNode(cc.Out, node, out); err != nil {
		return err
	}
	return nil
}

// respFmt reads the format a response declares — a fact about the server, not a
// preference of ours, which is why it is read off the reply rather than assumed.
//
// An UNLABELLED body is read as tony, and that covers the old JSON-speaking server too:
// JSON is parsed by the tony parser (codec.readAs), so the one guess that used to be
// necessary here is no longer a guess about anything.
func respFmt(resp *http.Response) codec.Format {
	if f, ok := codec.FormatForMedia(resp.Header.Get("Content-Type")); ok {
		return f
	}
	return codec.Tony
}

func httpPut(path string, body any) (*http.Response, error) {
	f := wireFmt()
	b, err := codec.Marshal(body, f)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPut, urlFor(path, nil), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	setFormatHeaders(req, f)
	return verse.Do(req)
}

func httpDelete(path string, q url.Values) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodDelete, urlFor(path, q), nil)
	if err != nil {
		return nil, err
	}
	return verse.Do(req)
}

// decodeClose decodes a response into v, reading it in the format the response declared,
// and closes the body.
func decodeClose(resp *http.Response, v any) {
	defer resp.Body.Close()
	decodeBody(resp, v)
}

// decodeBody decodes a response into v without closing it, for callers already deferring
// their own Close.
func decodeBody(resp *http.Response, v any) {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	_ = codec.Unmarshal(data, v, respFmt(resp))
}

// httpPostURL posts to an ABSOLUTE url — used for a process's broker face (its dialURL),
// which lives on a sibling port, not the operator API at g.Addr.
func httpPostURL(u string, body any) (*http.Response, error) {
	f := wireFmt()
	b, err := codec.Marshal(body, f)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	setFormatHeaders(req, f)
	return verse.Do(req)
}

// brokerClaim claims a process at its broker face as identity, returning the epoch (the
// fencing token to carry on report/inbox) and the control cursor (cCursor, where to read the
// inbox from). Same-identity re-claim is idempotent, so an executor can claim afresh.
func brokerClaim(base, id, identity string) (epoch, cCursor int64, err error) {
	resp, err := httpPostURL(base+"/processes/"+id+"/claim", map[string]any{"identity": identity})
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return 0, 0, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var c struct {
		Epoch   int64 `tony:"field=epoch"`
		CCursor int64 `tony:"field=cCursor"`
	}
	decodeBody(resp, &c)
	return c.Epoch, c.CCursor, nil
}

// brokerInbox pulls the worker inbox at base for id — items with cursor > since, carrying
// epoch for the fence — returning the items and the highest cursor seen (to advance since).
func brokerInbox(base, id string, since, epoch int64) ([]api.InboxItem, int64, error) {
	u := base + "/processes/" + id + "/inbox?since=" +
		strconv.FormatInt(since, 10) + "&epoch=" + strconv.FormatInt(epoch, 10)
	resp, err := verse.Get(u)
	if err != nil {
		return nil, since, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, since, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var box struct {
		Items []api.InboxItem `tony:"field=items"`
	}
	decodeBody(resp, &box)
	max := since
	for _, it := range box.Items {
		if it.Cursor > max {
			max = it.Cursor
		}
	}
	return box.Items, max, nil
}

// compactFrame re-renders one SSE frame in the requested format, on a single line so the
// stream stays one-record-per-line. A frame it cannot parse passes through unchanged —
// on a live stream, showing a surprising line beats swallowing it.
func compactFrame(s string, in, out codec.Format) string {
	if in == out {
		return s // already what was asked for; re-rendering would only risk mangling it
	}
	return codec.Recompact(s, in, out)
}

// streamFmt reads the format an SSE stream declares its FRAMES are in. Content-Type is
// spoken for by text/event-stream, so the server says it in a header of its own; a stream
// that does not say is read as tony, which also reads the JSON frames verse sent before it
// could say (valid JSON is valid tony).
func streamFmt(resp *http.Response) codec.Format {
	if f, err := codec.Parse(resp.Header.Get(codec.HeaderFormat)); err == nil &&
		resp.Header.Get(codec.HeaderFormat) != "" {
		return f
	}
	return codec.Tony
}

// ── the payload filter (--if / --if-at) ──────────────────────────────────────

// filterBy renders --if / --if-at into a read-plane query.
//
// Two commands select entities by what they SAY rather than only by where they live: `watch`
// streams the changes, `entities` lists the state. They mean exactly the same thing by the
// pair, so it is rendered in one place here and read back in one place at the far end
// (versed.queryPattern) — a filter that named different sets depending on which command you
// reached for would be worse than not having it.
//
// Each command declares the two fields itself rather than embedding a shared struct, because
// cli.StructOpts skips embedded fields (that is how it ignores *cli.Command): a shared struct
// would carry the tags and declare no flags at all. What is shared is the meaning, which is
// this function.
//
// The predicate is parsed HERE, in --ifmt, and re-rendered as tony for the wire. Parsing it
// first is what makes a typo a message about the pattern rather than a request that silently
// matches nothing; re-rendering is what lets `-i json` and `-i yaml` say the same predicate,
// since a GET has no body and so no Content-Type to declare a dialect in. Tags survive the
// round trip — `!glob "hotfix-*"` reaches the server as itself — which is the whole reason
// the canonical form is tony rather than JSON.
func filterBy(q url.Values, pattern, at string) error {
	if at != "" && pattern == "" {
		return fmt.Errorf("%w: --if-at roots --if; pass a pattern to root", cli.ErrUsage)
	}
	if pattern == "" {
		return nil
	}
	node, err := codec.ParseNode([]byte(pattern), inFmt())
	if err != nil {
		return fmt.Errorf("--if: %w", err)
	}
	b, err := codec.NodeBytesWire(node, codec.Tony)
	if err != nil {
		return fmt.Errorf("--if: %w", err)
	}
	q.Set("if", string(b))
	// Segment per parameter, as `entity rm --at` sends its path: the wire takes the address
	// as a list, so a field name is never re-split at the far end.
	for _, f := range fieldPath(at) {
		q.Add("if-at", f)
	}
	return nil
}

// ── watch ────────────────────────────────────────────────────────────────────

type watchConfig struct {
	*cli.Command
	System string `cli:"name=system desc='filter by entity system'"`
	Kind   string `cli:"name=kind desc='filter by entity kind'"`
	// default= in the tag rather than a struct literal, so `verse watch --help` SAYS where
	// the stream starts. A literal sets the same value and tells the reader nothing.
	Since string `cli:"name=since default=0 desc='start from store revision (cursor); 0 = from the beginning'"`
	// --if is the same predicate a charter's `when` carries in `value`, and means the same
	// thing: a tony match over the payload, so it takes operators (`{stage: !or [open, merged]}`)
	// and matches a payload that is not an object at all. A DELETE never matches one — there is
	// no payload left to ask about — so a filtered watch is a stream of states, not of removals.
	If   string `cli:"name=if desc='only changes whose payload matches this tony pattern (a delete never matches)'"`
	IfAt string `cli:"name=if-at desc='root --if at this dot-separated field path INSIDE the payload (e.g. labels)'"`
}

func watchCmd() *cli.Command {
	cfg := &watchConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "watch").
		WithSynopsis("watch — stream store changes (cursored)").
		WithOpts(opts...).
		WithRun(cfg.run)
}

// query renders the filters as the watch route's parameters. The cursor is watch's own; the
// rest is the selector every read-plane route takes.
func (cfg *watchConfig) query() (url.Values, error) {
	q := url.Values{"since": {cfg.Since}}
	if cfg.System != "" {
		q.Set("system", cfg.System)
	}
	if cfg.Kind != "" {
		q.Set("kind", cfg.Kind)
	}
	if err := filterBy(q, cfg.If, cfg.IfAt); err != nil {
		return nil, err
	}
	return q, nil
}

func (cfg *watchConfig) run(cc *cli.Context, args []string) error {
	if _, err := cfg.Parse(cc, args); err != nil {
		return err
	}
	q, err := cfg.query()
	if err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	out, err := outFmt()
	if err != nil {
		return err
	}
	resp, err := httpGet("/watch", q)
	if err != nil {
		return fmt.Errorf("watch: %w (is `verse up` running on %s?)", err, g.Addr)
	}
	defer resp.Body.Close()
	// A rejected watch must not read as a quiet verse. The loop below keeps only "data: "
	// lines, so an error body scans to nothing and watch blocks-then-exits 0 exactly as if
	// the store had no changes to report.
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("watch: %s", msg)
	}
	in := streamFmt(resp)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			fmt.Fprintln(cc.Out, compactFrame(data, in, out))
		}
	}
	return nil
}

// ── gate ─────────────────────────────────────────────────────────────────────

// gateSubs is the group's verbs, built fresh per call: the singular and the plural each
// hold their OWN instances (a Command is parented by whoever it is added to, and each
// carries the flag values of the invocation), so the one thing shared is this list. That
// is deliberate — a verb added here reaches both spellings, which is what
// TestPluralsTakeTheSingularsVerbs pins. Same for the five below.
func gateSubs() []*cli.Command {
	return []*cli.Command{gateAnswerCmd(true), gateAnswerCmd(false)}
}

func gatesCmd() *cli.Command {
	return collection("gates", "/gates",
		"gates — list pending decisions across the fleet (answer one with `verse gate`)", "gate",
		gateSubs()...)
}

func gateCmd() *cli.Command {
	return cli.NewCommand("gate").
		WithSynopsis("gate — answer one human-in-the-loop decision (list them with `verse gates`)").
		WithSubs(gateSubs()...)
}

// The answer to a gate is a VERB, so it is a command — `gate approve <id>` — rather than
// `gate resolve <id> --approve|--deny`, a flag pair that could be left empty or set both
// ways and so needed a runtime check to police two states the grammar should never have
// allowed. Every other verb in this CLI is already a subcommand (flow pause/resume/cancel,
// goals pause/resume/cancel, process cancel/say); the gate's answer was the one that had
// been demoted into a flag, mirroring api.Resolve's `Approve bool` rather than the operator.
//
// `vote <motion> <choice>` deliberately stays a positional: a select motion's choice is an
// arbitrary option id (a provider name), an OPEN set, which is data. A gate's answer is a
// closed binary, which is grammar.
// --as is optional here, where `vote --as` is required, and the difference is real rather
// than an oversight: a ballot is meaningless without its voter (a tally of anonymous votes
// governs nothing), while an approval is an act that happens whether or not it is signed.
// Unset, the server records "human:cli" — which is what it recorded for EVERY approval
// before this flag existed, so the dial only ever adds attribution.
type gateAnswerConfig struct {
	*cli.Command
	Say string `cli:"name=say aliases=s desc='words to say to the agent on resolution'"`
	As  string `cli:"name=as desc='identity answering the gate (REQUIRED — a decision names who made it)'"`
}

func gateAnswerCmd(approve bool) *cli.Command {
	cfg := &gateAnswerConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	name, syn := "approve", "approve a decision (also: y, yes): approve <id> --as <who> [--say <text>]"
	aliases, done := []string{"y", "yes"}, "approved"
	if !approve {
		name, syn = "deny", "deny a decision (also: n, no): deny <id> --as <who> [--say <text>]"
		aliases, done = []string{"n", "no"}, "denied"
	}
	// The aliases are named in the SYNOPSIS because cli's usage never prints them — it
	// renders a child as (first word of synopsis, synopsis) and never reads Aliases, which
	// is why `ps`, `e` and `goal` are invisible in the help today.
	return cli.NewCommandAt(&cfg.Command, name).WithAliases(aliases...).
		WithSynopsis(syn).
		WithOpts(opts...).
		WithRun(func(cc *cli.Context, args []string) error { return cfg.run(cc, args, name, done, approve) })
}

func (cfg *gateAnswerConfig) run(cc *cli.Context, args []string, verb, done string, approve bool) error {
	args, err := cfg.Parse(cc, args) // leaf: flags parse even after the <id> positional
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("%w: gate %s wants a decision id", cli.ErrUsage, verb)
	}
	if err := requireAs(cfg.As, "identity", "a decision names who made it"); err != nil {
		return err
	}
	body := api.Resolve{ID: args[0], Approve: approve, By: cfg.As}
	if cfg.Say != "" {
		body.Say = []string{cfg.Say}
	}
	resp, err := httpPost("/gate/resolve", body)
	if err != nil {
		return fmt.Errorf("gate %s: %w", verb, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gate %s: %s: %s", verb, resp.Status, strings.TrimSpace(string(msg)))
	}
	fmt.Fprintln(cc.Out, done, args[0])
	return nil
}

// ── entity ───────────────────────────────────────────────────────────────────

func entitySubs() []*cli.Command {
	return []*cli.Command{entityGetCmd(), entityPutCmd(), entityRmCmd()}
}

func entityCmd() *cli.Command {
	return cli.NewCommand("entity").WithAliases("e").
		WithSynopsis("entity — one entity in the store: get|put|rm <ref> (list them with `verse entities`)").
		WithSubs(entitySubs()...)
}

type entityGetConfig struct{ *cli.Command }

func entityGetCmd() *cli.Command {
	cfg := &entityGetConfig{}
	return cli.NewCommandAt(&cfg.Command, "get").
		WithSynopsis("get an entity: get <ref>").
		WithRun(cfg.run)
}

func (cfg *entityGetConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("%w: entity get wants a ref (system:kind:id)", cli.ErrUsage)
	}
	resp, err := httpGet("/entity", url.Values{"ref": {args[0]}})
	if err != nil {
		return fmt.Errorf("entity get: %w", err)
	}
	return printBody(cc, resp)
}

type entityPutConfig struct {
	*cli.Command
	At     string `cli:"name=at desc='write INSIDE the payload, at this dot-separated field path (e.g. labels.urgent)'"`
	Create bool   `cli:"name=create desc='only if nothing is there yet — the write loses a race rather than clobbering'"`
	Update bool   `cli:"name=update desc='only if something is already there'"`
	If     string `cli:"name=if desc='only if what is there matches this tony pattern'"`
}

func entityPutCmd() *cli.Command {
	cfg := &entityPutConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "put").
		WithSynopsis("put an entity: put <ref> <payload> [--at <field.path>] [--create|--update] [--if <pattern>]").
		WithOpts(opts...).
		WithRun(cfg.run)
}

// conditions renders the guard flags as the wire's preconditions. --create and --update are
// opposite answers to the same question, so asking both is a contradiction rather than a
// pair of conditions that can never hold — say so from the flags, before anything is sent.
func (cfg *entityPutConfig) conditions() ([]api.Condition, error) {
	var out []api.Condition
	if cfg.Create && cfg.Update {
		return nil, fmt.Errorf("%w: --create and --update are opposites; pass one", cli.ErrUsage)
	}
	if cfg.Create || cfg.Update {
		exists := cfg.Update
		out = append(out, api.Condition{Exists: &exists})
	}
	if cfg.If != "" {
		pattern, err := codec.ParseNode([]byte(cfg.If), inFmt())
		if err != nil {
			return nil, fmt.Errorf("--if: %w", err)
		}
		out = append(out, api.Condition{Pattern: pattern})
	}
	return out, nil
}

// fieldPath splits a dot-separated --at into the segments the wire takes. A field name
// containing a dot is not addressable this way; the wire takes the segments as a list, so
// nothing but this spelling is in the way if one ever needs to be.
func fieldPath(at string) []string {
	if at == "" {
		return nil
	}
	return strings.Split(at, ".")
}

func (cfg *entityPutConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("%w: entity put wants a ref and a payload", cli.ErrUsage)
	}
	// A payload typed on the command line names no format of its own, so it is read as
	// --ifmt / the configured default. Valid JSON is also valid tony, so `{"stage":"open"}`
	// keeps working whichever the default is.
	//
	// Parsed to a NODE rather than a map: a payload is any tony value, so `42`, `true` and
	// `[a, b]` are payloads too, and decoding into a map would reject them at the CLI even
	// though the store and the wire both accept them.
	payload, err := codec.ParseNode([]byte(args[1]), inFmt())
	if err != nil {
		return fmt.Errorf("entity put: payload: %w", err)
	}
	when, err := cfg.conditions()
	if err != nil {
		return fmt.Errorf("entity put: %w", err)
	}
	resp, err := httpPost("/entity", api.PutEntity{
		Ref:     args[0],
		Field:   fieldPath(cfg.At),
		Payload: payload,
		When:    when,
	})
	if err != nil {
		return fmt.Errorf("entity put: %w", err)
	}
	return printBody(cc, resp)
}

type entityRmConfig struct {
	*cli.Command
	At string `cli:"name=at desc='remove one field INSIDE the payload, at this dot-separated path'"`
}

func entityRmCmd() *cli.Command {
	cfg := &entityRmConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "rm").
		WithSynopsis("rm — remove an entity, or one field of it: rm <ref> [--at <field.path>]").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *entityRmConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("%w: entity rm wants a ref (system:kind:id)", cli.ErrUsage)
	}
	q := url.Values{"ref": {args[0]}}
	for _, f := range fieldPath(cfg.At) {
		q.Add("field", f)
	}
	resp, err := httpDelete("/entity", q)
	if err != nil {
		return fmt.Errorf("entity rm: %w", err)
	}
	defer resp.Body.Close()
	// Nothing stands there now — which is all there is to say, and is equally true of a ref
	// that held nothing to begin with.
	what := args[0]
	if cfg.At != "" {
		what += " " + cfg.At
	}
	fmt.Fprintf(cc.Out, "removed %s\n", what)
	return nil
}

// entities is the one collection with filters of its own, so it does not go through
// collection() — the shape is the same, the query is not.
type entitiesConfig struct {
	*cli.Command
	System string `cli:"name=system desc='filter by entity system'"`
	Kind   string `cli:"name=kind desc='filter by entity kind'"`
	// The same pair `watch` takes, meaning the same thing (filterBy) — the listing and the
	// stream are one selector asked at two times.
	//
	// `entity put --if` is a DIFFERENT --if, and they do not collide: a parent stops parsing
	// flags at its first positional, so `entities put <ref> <payload> --if <pattern>` hands
	// the flag to `put`, where it is a precondition rather than a filter. Pinned by
	// TestEntitiesPutKeepsItsOwnIf.
	If   string `cli:"name=if desc='only entities whose payload matches this tony pattern'"`
	IfAt string `cli:"name=if-at desc='root --if at this dot-separated field path INSIDE the payload (e.g. labels)'"`
}

func entitiesCmd() *cli.Command {
	cfg := &entitiesConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "entities").
		WithSynopsis("entities — list the store [--system <s>] [--kind <k>] [--if <pattern>] (get or put one with `verse entity`)").
		WithOpts(opts...).
		WithSubs(entitySubs()...).
		WithRun(cfg.run)
}

func (cfg *entitiesConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if handled, err := dispatch(cfg.Command, cc, args); handled {
		return err
	}
	if err := noArgs(cfg.Command, "entities", "entity", args); err != nil {
		return err
	}
	q := url.Values{}
	if cfg.System != "" {
		q.Set("system", cfg.System)
	}
	if cfg.Kind != "" {
		q.Set("kind", cfg.Kind)
	}
	if err := filterBy(q, cfg.If, cfg.IfAt); err != nil {
		return fmt.Errorf("entities: %w", err)
	}
	resp, err := httpGet("/entities", q)
	if err != nil {
		return fmt.Errorf("entities: %w", err)
	}
	return printBody(cc, resp)
}
