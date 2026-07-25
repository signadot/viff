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

// printBody pretty-prints a JSON response. (-o tony-format is a TODO — it needs the go-tony
// formatters; for now this is indented JSON.) A 4xx/5xx is surfaced as an ERROR carrying the
// server's message — which is usually plain text (http.Error), so decoding it as JSON fails
// and it would otherwise be swallowed entirely, leaving a rejected command looking like it
// succeeded. Callers that handle a specific status (404, 201, 204) check it before calling
// this, so they are unaffected.
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
	var v any
	if json.Unmarshal(body, &v) == nil {
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Fprintln(cc.Out, string(b))
	}
	return nil
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

// brokerInbox pulls the worker inbox at base for id — items with cursor > since, carrying
// epoch for the fence — returning the items and the highest cursor seen (to advance since).
func brokerInbox(base, id string, since, epoch int64) ([]map[string]any, int64, error) {
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
		Items []map[string]any `json:"items"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&box)
	max := since
	for _, it := range box.Items {
		if c, ok := it["cursor"].(float64); ok && int64(c) > max {
			max = int64(c)
		}
	}
	return box.Items, max, nil
}

func compactJSON(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
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
	resp, err := httpGet("/watch", q)
	if err != nil {
		return fmt.Errorf("watch: %w (is `verse up` running on %s?)", err, g.Addr)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			fmt.Fprintln(cc.Out, compactJSON(data))
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
		return fmt.Errorf("%w: entity put wants a ref and a payload (JSON for now; tony-format TODO)", cli.ErrUsage)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(args[1]), &payload); err != nil {
		return fmt.Errorf("entity put: payload must be JSON: %w", err)
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
