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
	return http.Get(urlFor(path, q))
}

// httpPost sends a document in the input format and asks for the output format back.
//
// Asking matters even though the client re-renders anyway: it is what lets the server send
// tony once it can, so the round trip stops being lossy — a tagged charter survives as tags
// rather than being flattened to the JSON key form on the way out and back.
func httpPost(path string, body any) (*http.Response, error) {
	f := inFmt()
	b, err := codec.Marshal(body, f)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, urlFor(path, nil), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	setFormatHeaders(req, f)
	return http.DefaultClient.Do(req)
}

// httpPostNode posts an already-built document. It exists so a command that has parsed a
// file does not have to render it back to bytes in some intermediate format on the way out
// — a tagged charter goes to the server AS tags, rather than being flattened through JSON
// and rebuilt from the key form at the far end.
func httpPostNode(path string, node *ir.Node) (*http.Response, error) {
	f := inFmt()
	b, err := codec.NodeBytes(node, f)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, urlFor(path, nil), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	setFormatHeaders(req, f)
	return http.DefaultClient.Do(req)
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
	f := inFmt()
	b, err := codec.Marshal(body, f)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPut, urlFor(path, nil), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	setFormatHeaders(req, f)
	return http.DefaultClient.Do(req)
}

func httpDelete(path string, q url.Values) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodDelete, urlFor(path, q), nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
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
	f := inFmt()
	b, err := codec.Marshal(body, f)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	setFormatHeaders(req, f)
	return http.DefaultClient.Do(req)
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

// inboxItem is the client's view of one broker inbox entry — the subset an executor acts
// on. It is decoded into a struct rather than a map[string]any on purpose: a map forces
// every read through a type assertion on the decoder's choice of Go type for a number,
// which differs between encodings (encoding/json always says float64; tony says int64 for
// an integer). Naming the type here makes the cursor an int64 whatever decoded it. Fields
// the broker sends but no executor reads are simply ignored.
type inboxItem struct {
	Cursor   int64  `tony:"field=cursor"`
	Kind     string `tony:"field=kind"`     // spec | say | cancel | pause | resume | vector | resolve
	Decision string `tony:"field=decision"` // resolve: the decision id being answered
	Proceed  bool   `tony:"field=proceed"`  // resolve: approve (true) or deny (false)
}

// brokerInbox pulls the worker inbox at base for id — items with cursor > since, carrying
// epoch for the fence — returning the items and the highest cursor seen (to advance since).
func brokerInbox(base, id string, since, epoch int64) ([]inboxItem, int64, error) {
	u := base + "/processes/" + id + "/inbox?since=" +
		strconv.FormatInt(since, 10) + "&epoch=" + strconv.FormatInt(epoch, 10)
	resp, err := http.Get(u)
	if err != nil {
		return nil, since, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, since, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var box struct {
		Items []inboxItem `tony:"field=items"`
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

// ── watch ────────────────────────────────────────────────────────────────────

type watchConfig struct {
	*cli.Command
	System string `cli:"name=system desc='filter by entity system'"`
	Kind   string `cli:"name=kind desc='filter by entity kind'"`
	Since  string `cli:"name=since desc='start from store revision (cursor)'"`
}

func watchCmd() *cli.Command {
	cfg := &watchConfig{Since: "0"}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "watch").
		WithSynopsis("watch — stream store changes (cursored)").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *watchConfig) run(cc *cli.Context, args []string) error {
	if _, err := cfg.Parse(cc, args); err != nil {
		return err
	}
	q := url.Values{"since": {cfg.Since}}
	if cfg.System != "" {
		q.Set("system", cfg.System)
	}
	if cfg.Kind != "" {
		q.Set("kind", cfg.Kind)
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

func gateCmd() *cli.Command {
	return cli.NewCommand("gate").
		WithSynopsis("gate — human-in-the-loop decisions").
		WithSubs(gateListCmd(), gateResolveCmd())
}

func gateListCmd() *cli.Command {
	return leaf("list", "list pending decisions across the fleet",
		func(cc *cli.Context, args []string) error {
			resp, err := httpGet("/gates", nil)
			if err != nil {
				return fmt.Errorf("gate list: %w", err)
			}
			return printBody(cc, resp)
		})
}

type gateResolveConfig struct {
	*cli.Command
	Approve bool   `cli:"name=approve desc='approve the decision'"`
	Deny    bool   `cli:"name=deny desc='deny the decision'"`
	Say     string `cli:"name=say desc='words to say to the agent on resolution'"`
}

func gateResolveCmd() *cli.Command {
	cfg := &gateResolveConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "resolve").
		WithSynopsis("resolve a decision: resolve <id> --approve|--deny [--say <text>]").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *gateResolveConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args) // leaf: flags parse even after the <id> positional
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("%w: gate resolve wants a decision id", cli.ErrUsage)
	}
	if cfg.Approve == cfg.Deny {
		return fmt.Errorf("%w: pass exactly one of --approve / --deny", cli.ErrUsage)
	}
	body := api.Resolve{ID: args[0], Approve: cfg.Approve}
	if cfg.Say != "" {
		body.Say = []string{cfg.Say}
	}
	resp, err := httpPost("/gate/resolve", body)
	if err != nil {
		return fmt.Errorf("gate resolve: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gate resolve: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	fmt.Fprintln(cc.Out, "resolved", args[0])
	return nil
}

// ── entity ───────────────────────────────────────────────────────────────────

func entityCmd() *cli.Command {
	return cli.NewCommand("entity").WithAliases("e").
		WithSynopsis("entity — the store").
		WithSubs(entityGetCmd(), entityPutCmd(), entityListCmd())
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

type entityPutConfig struct{ *cli.Command }

func entityPutCmd() *cli.Command {
	cfg := &entityPutConfig{}
	return cli.NewCommandAt(&cfg.Command, "put").
		WithSynopsis("put an entity: put <ref> <payload-json>").
		WithRun(cfg.run)
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
	var payload map[string]any
	if err := codec.Unmarshal([]byte(args[1]), &payload, inFmt()); err != nil {
		return fmt.Errorf("entity put: payload: %w", err)
	}
	resp, err := httpPost("/entity", api.PutEntity{Ref: args[0], Payload: payload})
	if err != nil {
		return fmt.Errorf("entity put: %w", err)
	}
	return printBody(cc, resp)
}

type entityListConfig struct {
	*cli.Command
	System string `cli:"name=system desc='filter by system'"`
	Kind   string `cli:"name=kind desc='filter by kind'"`
}

func entityListCmd() *cli.Command {
	cfg := &entityListConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "list").
		WithSynopsis("list entities").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *entityListConfig) run(cc *cli.Context, args []string) error {
	if _, err := cfg.Parse(cc, args); err != nil {
		return err
	}
	q := url.Values{}
	if cfg.System != "" {
		q.Set("system", cfg.System)
	}
	if cfg.Kind != "" {
		q.Set("kind", cfg.Kind)
	}
	resp, err := httpGet("/entities", q)
	if err != nil {
		return fmt.Errorf("entity list: %w", err)
	}
	return printBody(cc, resp)
}
