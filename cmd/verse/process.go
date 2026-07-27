package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/process"
)

// ps moved off this group and onto `processes`: as a group alias `verse ps` was an error
// ("no command provided"), which is not what anyone typing it expects. On the listing it
// means what it means everywhere else.
func processCmd() *cli.Command {
	return cli.NewCommand("process").
		WithSynopsis("process — one process in the runtime (list them with `verse processes`)").
		WithSubs(
			procRunCmd(),
			procGetCmd(),
			procInboxCmd(),
			procAttachCmd(),
			procCancelCmd(),
			procSayCmd(),
			procVectorCmd(),
			procAnswerCmd(true),
			procAnswerCmd(false),
			procDoCmd(),
		)
}

// ── inbox (operator peek at a brokered process's queued worker control) ──────

type procInboxConfig struct{ *cli.Command }

func procInboxCmd() *cli.Command {
	cfg := &procInboxConfig{}
	return cli.NewCommandAt(&cfg.Command, "inbox").
		WithSynopsis("inbox — the control queued for a process's worker: inbox <id>").WithRun(cfg.run)
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
	As     string `cli:"name=as desc='identity executing the process (REQUIRED — a claim must be attributable)'"`
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
		WithSynopsis("gate — raise a gate up to versed for resolution: gate <id> <name> --as <identity> [--reason <t>] [--wait]").
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
	if err := requireAs(cfg.As, "identity", "a claim must be attributable"); err != nil {
		return err
	}
	id, name := args[0], args[1]
	r, err := httpGet("/processes/"+id, nil)
	if err != nil {
		return fmt.Errorf("process do gate: %w", err)
	}
	var snap struct {
		DialURL string `tony:"field=dialURL"`
		Runner  string `tony:"field=runner"`
	}
	decodeClose(r, &snap)
	if snap.DialURL == "" {
		return fmt.Errorf("process do gate: %s is not a brokered process (runner %q)", id, snap.Runner)
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
			if it.Kind == "resolve" && it.Decision == decision {
				if it.Proceed {
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

// shortID is a short random suffix that keeps generated decision ids unique. Hex, so the id
// stays free of anything the surfaces it crosses read as punctuation — a URL's "#" or "?",
// a shell's globs, and process.GateSep, which would make a resolve split the id in the
// wrong place.
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
		"note":     "note — narrate onto a process you're executing: note <id> <text…> --as <identity>",
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
				return execReport(cc, cfg.As, id, []map[string]any{{"kind": "status", "text": "done"}}, rest)
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
	if err := requireAs(as, "identity", "a claim must be attributable"); err != nil {
		return err
	}
	r, err := httpGet("/processes/"+id, nil)
	if err != nil {
		return fmt.Errorf("process do: %w", err)
	}
	var snap struct {
		DialURL string `tony:"field=dialURL"`
		Runner  string `tony:"field=runner"`
	}
	decodeClose(r, &snap)
	if snap.DialURL == "" {
		return fmt.Errorf("process do: %s is not a brokered process (runner %q) — no broker to dial in to", id, snap.Runner)
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

// approve/deny rather than `resolve --approve|--deny`, for the reasons in gateAnswerCmd:
// the answer is a verb, so it is the command.
type procAnswerConfig struct {
	*cli.Command
	Say string `cli:"name=say aliases=s desc='words to say to the executor on resolution'"`
}

func procAnswerCmd(approve bool) *cli.Command {
	cfg := &procAnswerConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	name := "approve"
	syn := "approve a process gate (also: y, yes): approve <process-id> <decision-id> [--say <text>]"
	aliases := []string{"y", "yes"}
	if !approve {
		name = "deny"
		syn = "deny a process gate (also: n, no): deny <process-id> <decision-id> [--say <text>]"
		aliases = []string{"n", "no"}
	}
	return cli.NewCommandAt(&cfg.Command, name).WithAliases(aliases...).
		WithSynopsis(syn).
		WithOpts(opts...).
		WithRun(func(cc *cli.Context, args []string) error { return cfg.run(cc, args, name, approve) })
}

func (cfg *procAnswerConfig) run(cc *cli.Context, args []string, verb string, approve bool) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("%w: process %s wants a process id and a decision id", cli.ErrUsage, verb)
	}
	pid, did := args[0], args[1]
	// Same body as `gate approve`, --say included: this route relays the answer to a remote
	// worker's inbox, which is a difference in WHERE it goes, not in what can be said. The
	// flag was missing here alone, so "yes, but do it this way" was unsayable to the one
	// executor that is a person reading their inbox.
	body := api.Resolve{ID: did, Approve: approve}
	if cfg.Say != "" {
		body.Say = []string{cfg.Say}
	}
	return noContent(cc, "process "+verb, "/processes/"+pid+"/resolve", body, did)
}

// ── run ──────────────────────────────────────────────────────────────────────

type procRunConfig struct {
	*cli.Command
	Wait   bool   `cli:"name=wait desc='block until the process completes, then print the result (composes with --attach)'"`
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
	resp, err := httpPost("/processes", api.Spawn{Task: task, Driver: cfg.Driver})
	if err != nil {
		return fmt.Errorf("process run: %w", err)
	}
	var sp api.Spawned
	decodeClose(resp, &sp)
	if sp.ID == "" {
		return fmt.Errorf("process run: no id returned")
	}
	fmt.Fprintln(cc.Out, sp.ID)
	if cfg.Attach {
		// Spawn + watch in one command: the transcript streams live, so you SEE the agent
		// hit a gate. It blocks there until you resolve it (another terminal) — then the
		// stream continues to completion. Ctrl-c to detach; the process keeps running.
		if err := streamEvents(cc, sp.ID); err != nil {
			return err
		}
	}
	// --wait is honored WITH --attach, not swallowed by it. The two ask for different
	// things — the transcript as it happens, and the final result — and passing both used
	// to silently drop the second, so the answer the agent produced never got printed. The
	// stream has already reached a terminal status by here, so the poll returns at once.
	if !cfg.Wait {
		return nil
	}
	return waitForResult(cc, sp.ID, 30*time.Second)
}

// waitForResult polls a process until it leaves `running`, then prints its status and the
// result it produced.
func waitForResult(cc *cli.Context, id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var snap struct {
			Status string `tony:"field=status"`
			Result string `tony:"field=result"`
		}
		r, err := httpGet("/processes/"+id, nil)
		if err != nil {
			return fmt.Errorf("process run --wait: %w", err)
		}
		decodeClose(r, &snap)
		if snap.Status != "" && snap.Status != "running" {
			fmt.Fprintln(cc.Out, id, snap.Status)
			if snap.Result != "" { // the agent's final answer
				fmt.Fprintf(cc.Out, "\n%s\n", snap.Result)
			}
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("process run --wait: %s did not complete in %s", id, timeout)
}

// ── list / get ───────────────────────────────────────────────────────────────

func processesCmd() *cli.Command {
	return collection("processes", "/processes",
		"processes — list processes (also: ps; act on one with `verse process`)", "process").
		WithAliases("ps")
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
	// `gate list` prints gate ids, so reaching for one here is the natural mistake — and
	// the id it wants is right there in the same record, under `process`. Say so, rather
	// than making a 404 stand in for the explanation.
	if pid, _, ok := strings.Cut(id, process.GateSep); ok {
		return fmt.Errorf("process attach takes a process id, but %q is a gate id — did you mean %q?", id, pid)
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
	// Every non-streaming route checks its status (printBody does it centrally); this one
	// did not, and a stream is exactly where the omission hides. The reader below keeps
	// only "data: " lines, so a 404 — or any body that is not an event stream — scans to
	// zero output and returns nil: a process that does not exist looks identical to one
	// with nothing to say, and attach exits 0 having told you nothing.
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("process attach %s: %s", id, msg)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		return fmt.Errorf("process attach %s: not an event stream (Content-Type %q)", id, ct)
	}
	out, err := outFmt()
	if err != nil {
		return err
	}
	in := streamFmt(resp)
	interactive := isTTY()
	stdin := bufio.NewReader(os.Stdin)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		fmt.Fprintln(cc.Out, compactFrame(data, in, out))
		// process.Frame, not a hand-copied subset of it: the six fields this loop reads
		// were re-declared here with their tony tags, so a rename on the emitting side
		// would have gone unnoticed and simply stopped populating them.
		var ev process.Frame
		_ = codec.Unmarshal([]byte(data), &ev, in)
		// The process reached a terminal status — the stream won't emit more, so detach
		// (else attach would hang waiting on a closed-but-not-EOF stream).
		if ev.Kind == "status" && isTerminalStatus(ev.Text) {
			return nil
		}
		// A pending gate blocks the process HERE until answered — prompt and resolve inline.
		if interactive && ev.Kind == "pending" {
			pid := ev.Process
			if pid == "" {
				pid, _, _ = strings.Cut(ev.ID, process.GateSep)
			}
			approve := promptGate(cc, stdin, ev.Gate, ev.Tool)
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
	case "done", "cancelled", "failed", "orphaned":
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
		api.Resolve{ID: decisionID, Approve: approve})
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
		api.Say{Instruction: text}, id)
}

// ── vector (slide autonomy on a running process — the re-vector surface) ──────

type procVectorConfig struct {
	*cli.Command
	// Not "from JSON": codec.ReadFile takes the dialect from the suffix, so a .tony vector
	// is as readable as a .json one — and tony is what the rest of the CLI writes.
	File string `cli:"name=file aliases=f desc='load a full gate vector from FILE (gates + a default; .tony/.json/.yaml); overrides the mode arg'"`
	As   string `cli:"name=as desc='operator identity to attribute the slide to (recorded on the read plane)'"`
}

func procVectorCmd() *cli.Command {
	cfg := &procVectorConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "vector").
		WithSynopsis("vector <id> <auto|ask|park> [--file <vector.tony>] — slide autonomy on a running process").
		WithOpts(opts...).WithRun(cfg.run)
}

// run re-vectors a process: the common form sets the default autonomy (auto|ask|park); --file
// supplies a full gate vector. The vector rides a PUT to /processes/{id}/vector, which is
// runner-agnostic — the runtime slides a local process, the broker relays it to a remote one.
func (cfg *procVectorConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("%w: process vector wants a process id", cli.ErrUsage)
	}
	id := args[0]
	var vec process.Vector
	switch {
	case cfg.File != "":
		// The file's suffix names its dialect; an extensionless file uses --ifmt or the
		// configured default.
		if err := codec.ReadFile(cfg.File, &vec); err != nil {
			return fmt.Errorf("process vector: %s is not a valid vector: %w", cfg.File, err)
		}
	case len(args) >= 2:
		if err := vec.Default.UnmarshalText([]byte(args[1])); err != nil {
			return fmt.Errorf("%w: process vector mode must be auto|ask|park", cli.ErrUsage)
		}
	default:
		return fmt.Errorf("%w: process vector wants a mode (auto|ask|park) or --file", cli.ErrUsage)
	}
	resp, err := httpPut("/processes/"+id+"/vector", api.SetVector{Vector: &vec, By: cfg.As})
	if err != nil {
		return fmt.Errorf("process vector: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("process vector: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	fmt.Fprintln(cc.Out, "process vector", id, strings.ToLower(vec.Default.String()))
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// requireAs refuses an unattributed action. A claim, a ballot and a report all name who
// made them, and the refusal is a USAGE error in each — `vote` used to return a plain one,
// so the one command whose --as is most obviously load-bearing printed no usage.
func requireAs(as, noun, why string) error {
	if as == "" {
		return fmt.Errorf("%w: --as <%s> is required (%s)", cli.ErrUsage, noun, why)
	}
	return nil
}

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
