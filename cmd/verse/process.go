package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/scott-cotton/cli"
)

func processCmd() *cli.Command {
	return cli.NewCommand("process").WithAliases("ps").
		WithSynopsis("process — the process runtime").
		WithSubs(
			procRunCmd(),
			procListCmd(),
			procGetCmd(),
			procInboxCmd(),
			procAttachCmd(),
			procCancelCmd(),
			procSayCmd(),
			procResolveCmd(),
			procDoCmd(),
		)
}

// ── inbox (operator peek at a brokered process's queued worker control) ──────

type procInboxConfig struct{ *cli.Command }

func procInboxCmd() *cli.Command {
	cfg := &procInboxConfig{}
	return cli.NewCommandAt(&cfg.Command, "inbox").
		WithSynopsis("show the control queued for a process's worker: inbox <id>").WithRun(cfg.run)
}

func (cfg *procInboxConfig) run(cc *cli.Context, args []string) error {
	id, err := oneArg(cfg.Command, cc, args, "process inbox wants an id")
	if err != nil {
		return err
	}
	resp, err := httpGet("/processes/"+id+"/inbox", nil)
	if err != nil {
		return fmt.Errorf("process inbox: %w", err)
	}
	return printBody(cc, resp)
}

// ── do (act on a process as its EXECUTOR — the worker leg) ───────────────────
//
// `process do <verb>` is the inside-the-process vantage: the one running the work reports
// to the process's broker (claim → report), as opposed to the operator verbs that drive it
// from outside. The broker face is discovered from the process's dialURL, so no URL is
// typed; --as is required because every claim must be attributable.

type procDoConfig struct {
	*cli.Command
	As string `cli:"name=as desc='identity executing the process (REQUIRED — a claim must be attributable)'"`
}

func procDoCmd() *cli.Command {
	return cli.NewCommand("do").
		WithSynopsis("do — act on a process as its executor: do complete|fail|note|gate <id> --as <identity>").
		WithSubs(procDoVerb("complete"), procDoVerb("fail"), procDoVerb("note"), procDoGateCmd())
}

// ── do gate (executor RAISES a gate up to versed's governance) ───────────────
//
// Mid-execution the worker reaches a decision it isn't authorized to take alone, so it
// raises a gate to versed (reports EvPending). The verse decides who resolves it (ask, a
// reviewer, a vote, or — if the dial allows — the executor); the resolution returns down the
// worker's inbox. --wait blocks for that outcome. The executor never resolves its own gate.

type procDoGateConfig struct {
	*cli.Command
	As     string `cli:"name=as desc='identity executing the process (REQUIRED)'"`
	Reason string `cli:"name=reason desc='what is being decided (shown to resolvers)'"`
	Wait   bool   `cli:"name=wait desc='block until the gate is resolved, then print the outcome'"`
}

func procDoGateCmd() *cli.Command {
	cfg := &procDoGateConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "gate").
		WithSynopsis("raise a gate up to versed for resolution: gate <id> <name> --as <identity> [--reason <t>] [--wait]").
		WithOpts(opts...).WithRun(cfg.run)
}

func (cfg *procDoGateConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("%w: process do gate wants a process id and a gate name", cli.ErrUsage)
	}
	if cfg.As == "" {
		return fmt.Errorf("%w: --as <identity> is required (a claim must be attributable)", cli.ErrUsage)
	}
	id, name := args[0], args[1]
	r, err := httpGet("/processes/"+id, nil)
	if err != nil {
		return fmt.Errorf("process do gate: %w", err)
	}
	var snap struct{ DialURL, Policy string }
	decodeClose(r, &snap)
	if snap.DialURL == "" {
		return fmt.Errorf("process do gate: %s is not a brokered process (policy %q)", id, snap.Policy)
	}
	base := strings.TrimRight(snap.DialURL, "/")
	epoch, cCursor, err := brokerClaim(base, id, cfg.As)
	if err != nil {
		return fmt.Errorf("process do gate: claim %s: %w", id, err)
	}
	decision := name + "-" + shortID()
	ev := map[string]any{"kind": "pending", "id": decision, "gate": name}
	if cfg.Reason != "" {
		ev["text"] = cfg.Reason
	}
	resp, err := httpPostURL(base+"/processes/"+id+"/report",
		map[string]any{"epoch": epoch, "events": []any{ev}})
	if err != nil {
		return fmt.Errorf("process do gate: report: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("process do gate: report: %s", resp.Status)
	}
	fmt.Fprintln(cc.Out, "gate raised:", decision)
	if !cfg.Wait {
		return nil
	}

	fmt.Fprintln(cc.Out, "awaiting resolution…")
	since := cCursor
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		items, max, err := brokerInbox(base, id, since, epoch)
		if err != nil {
			return fmt.Errorf("process do gate: inbox: %w", err)
		}
		since = max
		for _, it := range items {
			if it["kind"] == "resolve" && it["decision"] == decision {
				if proceed, _ := it["proceed"].(bool); proceed {
					fmt.Fprintln(cc.Out, decision, "approved")
				} else {
					fmt.Fprintln(cc.Out, decision, "denied")
				}
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("process do gate: %s not resolved in time", decision)
}

// shortID is a short random suffix that keeps generated decision ids unique (and free of
// shell-hostile characters like #).
func shortID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func procDoVerb(verb string) *cli.Command {
	cfg := &procDoConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	syn := map[string]string{
		"complete": "complete a process you're executing: complete <id> [result] --as <identity>",
		"fail":     "fail a process you're executing: fail <id> [reason] --as <identity>",
		"note":     "narrate onto a process you're executing: note <id> <text…> --as <identity>",
	}[verb]
	return cli.NewCommandAt(&cfg.Command, verb).WithSynopsis(syn).WithOpts(opts...).
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			if len(args) < 1 {
				return fmt.Errorf("%w: process do %s wants a process id", cli.ErrUsage, verb)
			}
			id, rest := args[0], strings.Join(args[1:], " ")
			switch verb {
			case "complete":
				return execReport(cc, cfg.As, id, []map[string]any{{"kind": "status", "text": "completed"}}, rest)
			case "fail":
				return execReport(cc, cfg.As, id, []map[string]any{{"kind": "status", "text": "failed"}}, rest)
			default: // note
				if rest == "" {
					return fmt.Errorf("%w: process do note wants text", cli.ErrUsage)
				}
				return execReport(cc, cfg.As, id, []map[string]any{{"kind": "say", "text": rest}}, "")
			}
		})
}

// execReport is the executor's report action: resolve the process's broker (its dialURL),
// claim it as `as`, and report events onto the merged log verse observes.
func execReport(cc *cli.Context, as, id string, events []map[string]any, result string) error {
	if as == "" {
		return fmt.Errorf("%w: --as <identity> is required (a claim must be attributable)", cli.ErrUsage)
	}
	r, err := httpGet("/processes/"+id, nil)
	if err != nil {
		return fmt.Errorf("process do: %w", err)
	}
	var snap struct{ DialURL, Policy string }
	decodeClose(r, &snap)
	if snap.DialURL == "" {
		return fmt.Errorf("process do: %s is not a brokered process (policy %q) — no broker to dial in to", id, snap.Policy)
	}
	base := strings.TrimRight(snap.DialURL, "/")
	epoch, _, err := brokerClaim(base, id, as)
	if err != nil {
		return fmt.Errorf("process do: claim %s: %w", id, err)
	}
	body := map[string]any{"epoch": epoch, "events": events}
	if result != "" {
		body["result"] = result
	}
	resp, err := httpPostURL(base+"/processes/"+id+"/report", body)
	if err != nil {
		return fmt.Errorf("process do: report: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("process do: report: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	fmt.Fprintln(cc.Out, "process do", id, "ok")
	return nil
}

// ── resolve (answer a process's own tool gate) ──────────────────────────────

type procResolveConfig struct {
	*cli.Command
	Approve bool `cli:"name=approve desc='approve the decision'"`
	Deny    bool `cli:"name=deny desc='deny the decision'"`
}

func procResolveCmd() *cli.Command {
	cfg := &procResolveConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "resolve").
		WithSynopsis("resolve a process gate: resolve <process-id> <decision-id> --approve|--deny").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *procResolveConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("%w: process resolve wants a process id and a decision id", cli.ErrUsage)
	}
	if cfg.Approve == cfg.Deny {
		return fmt.Errorf("%w: pass exactly one of --approve / --deny", cli.ErrUsage)
	}
	pid, did := args[0], args[1]
	return noContent(cc, "process resolve", "/processes/"+pid+"/resolve",
		map[string]any{"decisionId": did, "proceed": cfg.Approve}, did)
}

// ── run ──────────────────────────────────────────────────────────────────────

type procRunConfig struct {
	*cli.Command
	Wait   bool   `cli:"name=wait desc='block until the process completes, then print the result'"`
	Attach bool   `cli:"name=attach desc='stream the live transcript (words, actions, gates) until it ends'"`
	Driver string `cli:"name=driver desc='process driver: scripted | llm (default: the runtime default)'"`
}

func procRunCmd() *cli.Command {
	cfg := &procRunConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "run").
		WithSynopsis("run a process: run <task…> [--attach] [--wait] [--driver scripted|llm]").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *procRunConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("%w: process run wants a task", cli.ErrUsage)
	}
	task := strings.Join(args, " ")
	resp, err := httpPost("/processes", map[string]any{"task": task, "driver": cfg.Driver})
	if err != nil {
		return fmt.Errorf("process run: %w", err)
	}
	var sp struct{ ID string }
	decodeClose(resp, &sp)
	if sp.ID == "" {
		return fmt.Errorf("process run: no id returned")
	}
	fmt.Fprintln(cc.Out, sp.ID)
	if cfg.Attach {
		// Spawn + watch in one command: the transcript streams live, so you SEE the agent
		// hit a gate. It blocks there until you resolve it (another terminal) — then the
		// stream continues to completion. Ctrl-c to detach; the process keeps running.
		return streamEvents(cc, sp.ID)
	}
	if !cfg.Wait {
		return nil
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var snap struct{ Status, Result string }
		r, err := httpGet("/processes/"+sp.ID, nil)
		if err != nil {
			return fmt.Errorf("process run --wait: %w", err)
		}
		decodeClose(r, &snap)
		if snap.Status != "" && snap.Status != "running" {
			fmt.Fprintln(cc.Out, sp.ID, snap.Status)
			if snap.Result != "" { // the agent's final answer
				fmt.Fprintf(cc.Out, "\n%s\n", snap.Result)
			}
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("process run --wait: %s did not complete in time", sp.ID)
}

// ── list / get ───────────────────────────────────────────────────────────────

func procListCmd() *cli.Command {
	return leaf("list", "list processes",
		func(cc *cli.Context, args []string) error {
			resp, err := httpGet("/processes", nil)
			if err != nil {
				return fmt.Errorf("process list: %w", err)
			}
			return printBody(cc, resp)
		})
}

type procGetConfig struct{ *cli.Command }

func procGetCmd() *cli.Command {
	cfg := &procGetConfig{}
	return cli.NewCommandAt(&cfg.Command, "get").
		WithSynopsis("get a process: get <id>").WithRun(cfg.run)
}

func (cfg *procGetConfig) run(cc *cli.Context, args []string) error {
	id, err := oneArg(cfg.Command, cc, args, "process get wants an id")
	if err != nil {
		return err
	}
	resp, err := httpGet("/processes/"+id, nil)
	if err != nil {
		return fmt.Errorf("process get: %w", err)
	}
	return printBody(cc, resp)
}

// ── attach (stream the process read plane) ──────────────────────────────────

type procAttachConfig struct{ *cli.Command }

func procAttachCmd() *cli.Command {
	cfg := &procAttachConfig{}
	return cli.NewCommandAt(&cfg.Command, "attach").
		WithSynopsis("attach to a process's read plane: attach <id>").WithRun(cfg.run)
}

func (cfg *procAttachConfig) run(cc *cli.Context, args []string) error {
	id, err := oneArg(cfg.Command, cc, args, "process attach wants an id")
	if err != nil {
		return err
	}
	return streamEvents(cc, id)
}

// streamEvents streams a process's read plane (SSE) to stdout — the agent's words, each
// action, and any gate it blocks on. On an interactive terminal it also PROMPTS you to
// approve/deny each gate as it blocks and resolves it inline (the agent unblocks and the
// stream continues); piped/non-tty it just streams. Runs until the stream ends or you ^C.
func streamEvents(cc *cli.Context, id string) error {
	resp, err := httpGet("/processes/"+id+"/events", nil)
	if err != nil {
		return fmt.Errorf("process attach: %w", err)
	}
	defer resp.Body.Close()
	interactive := isTTY()
	in := bufio.NewReader(os.Stdin)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		fmt.Fprintln(cc.Out, compactJSON(data))
		var ev struct{ Kind, Text, ID, Gate, Tool, Process string }
		_ = json.Unmarshal([]byte(data), &ev)
		// The process reached a terminal status — the stream won't emit more, so detach
		// (else attach would hang waiting on a closed-but-not-EOF stream).
		if ev.Kind == "status" && isTerminalStatus(ev.Text) {
			return nil
		}
		// A pending gate blocks the process HERE until answered — prompt and resolve inline.
		if interactive && ev.Kind == "pending" {
			pid := ev.Process
			if pid == "" {
				pid, _, _ = strings.Cut(ev.ID, "#")
			}
			approve := promptGate(cc, in, ev.Gate, ev.Tool)
			if err := resolveProcessGate(pid, ev.ID, approve); err != nil {
				fmt.Fprintf(cc.Err, "  resolve %s: %v\n", ev.ID, err)
			} else if approve {
				fmt.Fprintf(cc.Out, "  → approved %s\n", ev.ID)
			} else {
				fmt.Fprintf(cc.Out, "  → denied %s\n", ev.ID)
			}
		}
	}
	return nil
}

// isTerminalStatus reports whether a status EvStatus text is an end state — covering the
// runtime (completed | cancelled | "driver error") and the broker (failed) vocabularies.
func isTerminalStatus(text string) bool {
	switch text {
	case "completed", "cancelled", "failed", "orphaned":
		return true
	}
	return strings.Contains(text, "error")
}

// isTTY reports whether stdin is an interactive terminal (so we prompt) vs a pipe (stream only).
func isTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// promptGate asks whether to allow a blocked gate; default (empty/anything-but-yes) is deny.
func promptGate(cc *cli.Context, in *bufio.Reader, gate, tool string) bool {
	fmt.Fprintf(cc.Out, "  ⏸ gate %q (%s) — approve? [y/N] ", gate, tool)
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}

// resolveProcessGate answers a process's pending decision by id.
func resolveProcessGate(pid, decisionID string, approve bool) error {
	resp, err := httpPost("/processes/"+pid+"/resolve",
		map[string]any{"decisionId": decisionID, "proceed": approve})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// ── cancel / say ─────────────────────────────────────────────────────────────

type procCancelConfig struct{ *cli.Command }

func procCancelCmd() *cli.Command {
	cfg := &procCancelConfig{}
	return cli.NewCommandAt(&cfg.Command, "cancel").
		WithSynopsis("cancel a process: cancel <id>").WithRun(cfg.run)
}

func (cfg *procCancelConfig) run(cc *cli.Context, args []string) error {
	id, err := oneArg(cfg.Command, cc, args, "process cancel wants an id")
	if err != nil {
		return err
	}
	return noContent(cc, "process cancel", "/processes/"+id+"/cancel", nil, id)
}

type procSayConfig struct{ *cli.Command }

func procSayCmd() *cli.Command {
	cfg := &procSayConfig{}
	return cli.NewCommandAt(&cfg.Command, "say").
		WithSynopsis("say something to a running process (re-prompt): say <id> <text…>").WithRun(cfg.run)
}

func (cfg *procSayConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("%w: process say wants an id and text", cli.ErrUsage)
	}
	id, text := args[0], strings.Join(args[1:], " ")
	return noContent(cc, "process say", "/processes/"+id+"/say",
		map[string]any{"instruction": text}, id)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// oneArg parses flags (anywhere) and returns the single required positional.
func oneArg(cmd *cli.Command, cc *cli.Context, args []string, want string) (string, error) {
	args, err := cmd.Parse(cc, args)
	if err != nil {
		return "", err
	}
	if len(args) < 1 {
		return "", fmt.Errorf("%w: %s", cli.ErrUsage, want)
	}
	return args[0], nil
}

func noContent(cc *cli.Context, what, path string, body any, id string) error {
	resp, err := httpPost(path, body)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s: %s", what, resp.Status, strings.TrimSpace(string(msg)))
	}
	if id == "" {
		fmt.Fprintln(cc.Out, what, "ok")
	} else {
		fmt.Fprintln(cc.Out, what, id)
	}
	return nil
}
