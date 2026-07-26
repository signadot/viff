package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/scott-cotton/cli"

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
	u := "http://" + g.Addr + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func httpGet(path string, q url.Values) (*http.Response, error) {
	return http.Get(urlFor(path, q))
}

func httpPost(path string, body any) (*http.Response, error) {
	b, _ := json.Marshal(body)
	return http.Post(urlFor(path, nil), "application/json", bytes.NewReader(b))
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
	if err := codec.EncodeNode(cc.Out, node, out); err != nil {
		return err
	}
	return nil
}

// respFmt reads the format a response declares. The server speaks JSON until it learns to
// negotiate; an unlabelled body is read as JSON rather than as the configured default,
// because what came back is a fact about the server, not a preference of ours.
func respFmt(resp *http.Response) codec.Format {
	if f, ok := codec.FormatForMedia(resp.Header.Get("Content-Type")); ok {
		return f
	}
	return codec.JSON
}

func httpPut(path string, body any) (*http.Response, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPut, urlFor(path, nil), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(req)
}

func httpDelete(path string, q url.Values) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodDelete, urlFor(path, q), nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

func decodeClose(resp *http.Response, v any) {
	defer resp.Body.Close()
	_ = json.NewDecoder(resp.Body).Decode(v)
}

// httpPostURL posts JSON to an ABSOLUTE url — used for a process's broker face (its
// dialURL), which lives on a sibling port, not the operator API at g.Addr.
func httpPostURL(u string, body any) (*http.Response, error) {
	b, _ := json.Marshal(body)
	return http.Post(u, "application/json", bytes.NewReader(b))
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
		Epoch   int64 `json:"epoch"`
		CCursor int64 `json:"cCursor"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&c)
	return c.Epoch, c.CCursor, nil
}

// inboxItem is the client's view of one broker inbox entry — the subset an executor acts
// on. It is decoded into a struct rather than a map[string]any on purpose: a map forces
// every read through a type assertion on the decoder's choice of Go type for a number,
// which differs between encodings (encoding/json always says float64; tony says int64 for
// an integer). Naming the type here makes the cursor an int64 whatever decoded it. Fields
// the broker sends but no executor reads are simply ignored.
type inboxItem struct {
	Cursor   int64  `json:"cursor"`
	Kind     string `json:"kind"`     // spec | say | cancel | pause | resume | vector | resolve
	Decision string `json:"decision"` // resolve: the decision id being answered
	Proceed  bool   `json:"proceed"`  // resolve: approve (true) or deny (false)
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
		Items []inboxItem `json:"items"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&box)
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
func compactFrame(s string, out codec.Format) string {
	return codec.Recompact(s, codec.JSON, out)
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
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			fmt.Fprintln(cc.Out, compactFrame(data, out))
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
	body := map[string]any{"id": args[0], "approve": cfg.Approve}
	if cfg.Say != "" {
		body["say"] = []string{cfg.Say}
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
	resp, err := httpPost("/entity", map[string]any{"ref": args[0], "payload": payload})
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
