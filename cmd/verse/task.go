package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/scott-cotton/cli"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/task"
)

// taskSubs is the group's verbs. It is a function rather than a slice so each rootCmd() gets
// its OWN instances: a Command is parented by whoever it is added to, and it carries the flag
// values of the invocation, so a package-level slice would be shared across command trees.
func taskSubs() []*cli.Command {
	return []*cli.Command{
		taskRunCmd(),
		taskGetCmd(),
		taskInboxCmd(),
		taskAttachCmd(),
		taskCancelCmd(),
		taskSayCmd(),
		taskVectorCmd(),
		taskAnswerCmd(true),
		taskAnswerCmd(false),
		taskDoCmd(),
	}
}

// ── inbox (operator peek at a brokered task's queued worker control) ──────

type taskInboxConfig struct{ *cli.Command }

func taskInboxCmd() *cli.Command {
	cfg := &taskInboxConfig{}
	return cli.NewCommandAt(&cfg.Command, "inbox").
		WithSynopsis("inbox — the control queued for a task's worker: inbox <id>").WithRun(cfg.run)
}

func (cfg *taskInboxConfig) run(cc *cli.Context, args []string) error {
	id, err := oneArg(cfg.Command, cc, args, "task inbox wants an id")
	if err != nil {
		return err
	}
	resp, err := httpGet("/tasks/"+id+"/inbox", nil)
	if err != nil {
		return fmt.Errorf("task inbox: %w", err)
	}
	return printBody(cc, resp)
}

// ── do (act on a task as its EXECUTOR — the worker leg) ───────────────────
//
// `task do <verb>` is the inside-the-task vantage: the one running the work reports
// to the task's broker (claim → report), as opposed to the operator verbs that drive it
// from outside. The broker face is discovered from the task's dialURL, so no URL is
// typed; --as is required because every claim must be attributable.

type taskDoConfig struct {
	*cli.Command
	As string `cli:"name=as desc='identity executing the task; the configured user when left off (a claim must be attributable)'"`
}

func taskDoCmd() *cli.Command {
	return cli.NewCommand("do").
		WithSynopsis("do — act on a task as its executor: do complete|fail|note|gate <id> --as <identity>").
		WithSubs(taskDoVerb("complete"), taskDoVerb("fail"), taskDoVerb("note"), taskDoGateCmd())
}

// ── do gate (executor RAISES a gate up to versed's governance) ───────────────
//
// Mid-execution the worker reaches a decision it isn't authorized to take alone, so it
// raises a gate to versed (reports EvPending). The verse decides who resolves it (ask, a
// reviewer, a vote, or — if the dial allows — the executor); the resolution returns down the
// worker's inbox. --wait blocks for that outcome. The executor never resolves its own gate.

type taskDoGateConfig struct {
	*cli.Command
	As     string `cli:"name=as desc='identity executing the task; the configured user when left off (a claim must be attributable)'"`
	Reason string `cli:"name=reason desc='what is being decided (shown to resolvers)'"`
	// The question's shape, as a charter gate would write it: outcomes of its own with a
	// threshold each, and a clock. Empty poses proceed or deny, one ballot each.
	Options  string `cli:"name=options desc='the outcomes, comma-separated, each id[:need] (default: proceed,deny at one each)'"`
	Deadline string `cli:"name=deadline desc='how long the question stands, within[:default] (e.g. 4h:deny)'"`
	Wait     bool   `cli:"name=wait desc='block until the gate is resolved, then print the outcome'"`
}

// parseOptions reads `id[:need],id[:need]` into options. A threshold left out is one.
func parseOptions(spec string) ([]task.Option, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	var out []task.Option
	for _, part := range strings.Split(spec, ",") {
		id, need, hasNeed := strings.Cut(strings.TrimSpace(part), ":")
		if id == "" {
			return nil, fmt.Errorf("%w: --options: an option has no id in %q", cli.ErrUsage, spec)
		}
		o := task.Option{ID: id, Need: 1}
		if hasNeed {
			n, err := strconv.Atoi(need)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("%w: --options: %q wants a threshold of at least one, got %q", cli.ErrUsage, id, need)
			}
			o.Need = n
		}
		out = append(out, o)
	}
	return out, nil
}

// parseDeadline reads `within[:default]`.
func parseDeadline(spec string) (*task.Deadline, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	within, dflt, _ := strings.Cut(strings.TrimSpace(spec), ":")
	d := &task.Deadline{Within: within, Default: dflt}
	if _, err := d.After(); err != nil {
		return nil, fmt.Errorf("%w: --deadline: %v", cli.ErrUsage, err)
	}
	return d, nil
}

func taskDoGateCmd() *cli.Command {
	cfg := &taskDoGateConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "gate").
		WithSynopsis("gate — raise a gate up to versed for resolution: gate <id> <name> --as <identity> [--reason <t>] [--wait]").
		WithOpts(opts...).WithRun(cfg.run)
}

func (cfg *taskDoGateConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("%w: task do gate wants a task id and a gate name", cli.ErrUsage)
	}
	as, err := requireAs(cfg.As, "identity", "a claim must be attributable")
	if err != nil {
		return err
	}
	cfg.As = as
	id, name := args[0], args[1]
	r, err := httpGet("/tasks/"+id, nil)
	if err != nil {
		return fmt.Errorf("task do gate: %w", err)
	}
	var snap struct {
		DialURL string `tony:"field=dialURL"`
		Runner  string `tony:"field=runner"`
	}
	decodeClose(r, &snap)
	if snap.DialURL == "" {
		return fmt.Errorf("task do gate: %s is not a brokered task (runner %q)", id, snap.Runner)
	}
	base := strings.TrimRight(snap.DialURL, "/")
	epoch, cCursor, err := brokerClaim(base, id, cfg.As)
	if err != nil {
		return fmt.Errorf("task do gate: claim %s: %w", id, err)
	}
	options, err := parseOptions(cfg.Options)
	if err != nil {
		return err
	}
	clock, err := parseDeadline(cfg.Deadline)
	if err != nil {
		return err
	}
	decision := name + "-" + shortID()
	ev := map[string]any{"kind": "pending", "id": decision, "gate": name}
	if cfg.Reason != "" {
		ev["text"] = cfg.Reason
	}
	if len(options) > 0 {
		ev["options"] = options
	}
	if clock != nil {
		ev["deadline"] = clock
	}
	resp, err := httpPostURL(base+"/tasks/"+id+"/report",
		map[string]any{"epoch": epoch, "events": []any{ev}})
	if err != nil {
		return fmt.Errorf("task do gate: report: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("task do gate: report: %s", resp.Status)
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
			return fmt.Errorf("task do gate: inbox: %w", err)
		}
		since = max
		for _, it := range items {
			if it.Kind == "resolve" && it.Decision == decision {
				switch it.Choice {
				case task.Proceed, "":
					if it.Proceed {
						fmt.Fprintln(cc.Out, decision, "approved")
					} else {
						fmt.Fprintln(cc.Out, decision, "denied")
					}
				case task.Deny:
					fmt.Fprintln(cc.Out, decision, "denied")
				default:
					fmt.Fprintln(cc.Out, decision, "settled on", it.Choice)
				}
				// TAKEN, said so. An answer is delivered when a poll asks for what comes AFTER
				// it — that is the broker's acknowledgement, and the poll that first showed the
				// item is not it. Without this the record read `delivered: false` for an answer
				// the worker had just printed. Best effort: the outcome is known either way.
				_, _, _ = brokerInbox(base, id, max, epoch)
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("task do gate: %s not resolved in time", decision)
}

// shortID is a short random suffix that keeps generated decision ids unique. Hex, so the id
// stays free of anything the surfaces it crosses read as punctuation — a URL's "#" or "?",
// a shell's globs, and task.GateSep, which would make a resolve split the id in the
// wrong place.
func shortID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func taskDoVerb(verb string) *cli.Command {
	cfg := &taskDoConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	syn := map[string]string{
		"complete": "complete a task you're executing: complete <id> [result] --as <identity>",
		"fail":     "fail a task you're executing: fail <id> [reason] --as <identity>",
		"note":     "note — narrate onto a task you're executing: note <id> <text…> --as <identity>",
	}[verb]
	return cli.NewCommandAt(&cfg.Command, verb).WithSynopsis(syn).WithOpts(opts...).
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			if len(args) < 1 {
				return fmt.Errorf("%w: task do %s wants a task id", cli.ErrUsage, verb)
			}
			id, rest := args[0], strings.Join(args[1:], " ")
			switch verb {
			case "complete":
				return execReport(cc, cfg.As, id, []map[string]any{{"kind": "status", "text": "done"}}, rest)
			case "fail":
				return execReport(cc, cfg.As, id, []map[string]any{{"kind": "status", "text": "failed"}}, rest)
			default: // note
				if rest == "" {
					return fmt.Errorf("%w: task do note wants text", cli.ErrUsage)
				}
				return execReport(cc, cfg.As, id, []map[string]any{{"kind": "say", "text": rest}}, "")
			}
		})
}

// execReport is the executor's report action: resolve the task's broker (its dialURL),
// claim it as `as`, and report events onto the merged log verse observes.
func execReport(cc *cli.Context, as, id string, events []map[string]any, result string) error {
	as, err := requireAs(as, "identity", "a claim must be attributable")
	if err != nil {
		return err
	}
	r, err := httpGet("/tasks/"+id, nil)
	if err != nil {
		return fmt.Errorf("task do: %w", err)
	}
	var snap struct {
		DialURL string `tony:"field=dialURL"`
		Runner  string `tony:"field=runner"`
	}
	decodeClose(r, &snap)
	if snap.DialURL == "" {
		return fmt.Errorf("task do: %s is not a brokered task (runner %q) — no broker to dial in to", id, snap.Runner)
	}
	base := strings.TrimRight(snap.DialURL, "/")
	epoch, _, err := brokerClaim(base, id, as)
	if err != nil {
		return fmt.Errorf("task do: claim %s: %w", id, err)
	}
	body := map[string]any{"epoch": epoch, "events": events}
	if result != "" {
		body["result"] = result
	}
	resp, err := httpPostURL(base+"/tasks/"+id+"/report", body)
	if err != nil {
		return fmt.Errorf("task do: report: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("task do: report: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	fmt.Fprintln(cc.Out, "task do", id, "ok")
	return nil
}

// ── resolve (answer a task's own tool gate) ──────────────────────────────

// approve/deny rather than `resolve --approve|--deny`, for the reasons in gateAnswerCmd:
// the answer is a verb, so it is the command.
type taskAnswerConfig struct {
	*cli.Command
	Say string `cli:"name=say aliases=s desc='words to say to the executor on resolution'"`
	As  string `cli:"name=as desc='identity answering the gate; the configured user when left off (a decision names who made it)'"`
	// Not a bypass so much as an admission: a resolver that answers exclusively can always
	// be answered over by whoever holds this API, so the flag makes that an act with a name
	// on it rather than a silent one.
	Override bool `cli:"name=override desc='answer over an exclusive resolver; the record names you both'"`
}

func taskAnswerCmd(approve bool) *cli.Command {
	cfg := &taskAnswerConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	name := "approve"
	syn := "approve a task gate (also: y, yes): approve <task-id> <decision-id> --as <who> [--say <text>]"
	aliases := []string{"y", "yes"}
	if !approve {
		name = "deny"
		syn = "deny a task gate (also: n, no): deny <task-id> <decision-id> --as <who> [--say <text>]"
		aliases = []string{"n", "no"}
	}
	return cli.NewCommandAt(&cfg.Command, name).WithAliases(aliases...).
		WithSynopsis(syn).
		WithOpts(opts...).
		WithRun(func(cc *cli.Context, args []string) error { return cfg.run(cc, args, name, approve) })
}

func (cfg *taskAnswerConfig) run(cc *cli.Context, args []string, verb string, approve bool) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("%w: task %s wants a task id and a decision id", cli.ErrUsage, verb)
	}
	as, err := requireAs(cfg.As, "identity", "a decision names who made it")
	if err != nil {
		return err
	}
	cfg.As = as
	pid, did := args[0], args[1]
	// Same body as `gate approve`, --say included: this route relays the answer to a remote
	// worker's inbox, which is a difference in WHERE it goes, not in what can be said. The
	// flag was missing here alone, so "yes, but do it this way" was unsayable to the one
	// executor that is a person reading their inbox.
	body := api.Resolve{ID: did, Choice: choiceOf(approve), By: cfg.As, Override: cfg.Override}
	if cfg.Say != "" {
		body.Say = []string{cfg.Say}
	}
	return noContent(cc, "task "+verb, "/tasks/"+pid+"/resolve", body, did)
}

// ── run ──────────────────────────────────────────────────────────────────────

type taskRunConfig struct {
	*cli.Command
	Wait   bool   `cli:"name=wait desc='block until the task completes, then print the result (composes with --attach)'"`
	Attach bool   `cli:"name=attach desc='stream the live transcript (words, actions, gates) until it ends'"`
	Driver string `cli:"name=driver desc='task driver: scripted | calls | llm (default: the runtime default)'"`
	// Name is a LABEL, not an address. The id is derived from it and returned, because a
	// caller-chosen id is a caller-chosen prefix on every gate question the run raises
	// (`<id>~d1`) — and answering one recovers the owner by cutting at the first `~`, so a
	// task called `a~b` hands its questions to a task called `a`. The trigger side has
	// always sanitised its own ids (safeSeg) for exactly this; the ad-hoc face never had to,
	// because it never let anyone choose one.
	Name string `cli:"name=name desc='label for the run; the id is derived from it and printed'"`
	// The two fields a spawn has carried all along with no way to type them.
	Subject string `cli:"name=subject desc='the entity this run is about — it joins that flow'"`
	// Without this a scripted run is `auto` and asks nothing (see scriptedVector), so a
	// --resolver bound to it would have no question to answer.
	Vector string `cli:"name=vector desc='autonomy for this run: auto | ask | park (default: the runtime default)'"`
	// The run's own gates over that default, from a file — the ad-hoc face of a spawn's
	// `gates:`. A gate carries the question it poses (need, options, deadline), which a name
	// cannot, and this is how a run gets one: `--gates gates.tony`.
	Gates string `cli:"name=gates desc='file holding the gates for this run, over --vector (a list of gates; .tony/.json/.yaml)'"`
	// Who is running this — used twice, because it is one fact. A gate that arrives while
	// attached is answered as this (watching needs no identity; answering has needed one
	// since a decision became a durable record), and it rides the spawn as the run's
	// PRINCIPAL, so a runner whose effects leave the verse can say on whose account.
	// Self-asserted, like every other `--as` here.
	As       string `cli:"name=as desc='who is running this: the run principal, and the identity for a gate answered while attached'"`
	Resolver string `cli:"name=resolver desc='binding that answers the gates this run raises (verse.resolvers.<kind>.<name>)'"`
	Runner   string `cli:"name=runner desc='where this run runs: an installed runner (verse.runners.<kind>.<name>, or the bare <kind>.<name>); default: the verse default'"`
	// The shape this run's final message must have — a tony match pattern in a file, the
	// ad-hoc face of a charter's `answer:`. Without one, "finished" means only that the model
	// stopped calling tools, which is also what a model narrating its next step looks like.
	Answer string `cli:"name=answer desc='file holding the answer contract for this run (a tony match pattern)'"`
	// Which workspace the runner should build — a repo at a base, a toolchain, a
	// containment. Resolved by the runner against its own config; verse carries the name.
	Environment string `cli:"name=environment desc='workspace the runner should run this in (a name the runner knows)'"`
}

func taskRunCmd() *cli.Command {
	cfg := &taskRunConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "run").
		WithSynopsis("run a task: run <prompt…> [--name <label>] [--subject <ref>] " +
			"[--resolver <ref>] [--vector auto|ask|park] [--gates <file>] [--attach] [--wait] " +
			"[--driver scripted|calls|llm]").
		WithOpts(opts...).
		WithRun(cfg.run)
}

// scriptedVector is the typed-it-yourself exemption, and the only place it holds: a scripted
// prompt IS the shell command, so whoever typed `--driver scripted "rm -rf x"` has already
// authorized the one action the task will take, and asking them again asks nothing they have
// not just answered.
//
// It lives on the CLI path rather than in the driver because that is the whole of the
// argument — it is about who addressed the request, not about what the driver is. The driver
// used to carry it, so a charter that spawns `driver: scripted` inherited an exemption it
// never typed anything to earn; see versed.NewScriptedTask. An explicit --vector still wins,
// including `--vector ask`, and the llm driver is untouched: it has its own.
func scriptedVector(driver string) string {
	if driver == "scripted" {
		return "auto"
	}
	return ""
}

func (cfg *taskRunConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("%w: task run wants a prompt", cli.ErrUsage)
	}
	prompt := strings.Join(args, " ")
	var contract *ir.Node
	if cfg.Answer != "" {
		n, _, err := codec.ReadFileNode(cfg.Answer)
		if err != nil {
			return fmt.Errorf("task run: answer contract: %w", err)
		}
		contract = n
	}
	vector := cfg.Vector
	if vector == "" {
		vector = scriptedVector(cfg.Driver)
	}
	// Read here, so a gate that will not parse is a message on the terminal of whoever wrote
	// it; what goes over the wire is the field, the same one a charter's spawn carries.
	var gates []task.Gate
	if cfg.Gates != "" {
		if err := codec.ReadFile(cfg.Gates, &gates); err != nil {
			return fmt.Errorf("task run: %s is not a list of gates: %w", cfg.Gates, err)
		}
	}
	// `--driver calls` takes the actions themselves, as a tony document. It is parsed HERE, so
	// a list that will not read is a message on the terminal of whoever typed it rather than a
	// task that failed somewhere else — and what goes over the wire is the field, which is the
	// only form a call list travels in.
	var calls []task.Call
	if cfg.Driver == task.DriverCalls {
		cs, err := task.ParseCalls(prompt)
		if err != nil {
			return fmt.Errorf("task run: %w", err)
		}
		calls, prompt = cs, ""
	}
	resp, err := httpPost("/tasks", api.Spawn{
		Prompt: prompt, Driver: cfg.Driver, Name: cfg.Name,
		Subject: cfg.Subject, Resolver: cfg.Resolver, Vector: vector, Gates: gates, Calls: calls,
		Runner: cfg.Runner, Environment: cfg.Environment, Principal: principal(cfg.As),
		Answer: contract,
	})
	if err != nil {
		return fmt.Errorf("task run: %w", err)
	}
	// The server's reason, not a guess at it. A refused spawn — a resolver that is not
	// installed, an id that would break the surfaces an id crosses — says exactly what is
	// wrong, and "no id returned" threw that away and sent the reader looking at the wrong
	// thing.
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return fmt.Errorf("task run: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var sp api.Spawned
	decodeClose(resp, &sp)
	if sp.ID == "" {
		return fmt.Errorf("task run: no id returned")
	}
	fmt.Fprintln(cc.Out, sp.ID)
	if cfg.Attach {
		// Spawn + watch in one command: the transcript streams live, so you SEE the agent
		// hit a gate. It blocks there until you resolve it (another terminal) — then the
		// stream continues to completion. Ctrl-c to detach; the task keeps running.
		if err := streamEvents(cc, sp.ID, principal(cfg.As)); err != nil {
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

// waitForResult polls a task until it leaves `running`, then prints its status and the
// result it produced.
func waitForResult(cc *cli.Context, id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var snap struct {
			Status string `tony:"field=status"`
			Result string `tony:"field=result"`
		}
		r, err := httpGet("/tasks/"+id, nil)
		if err != nil {
			return fmt.Errorf("task run --wait: %w", err)
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
	return fmt.Errorf("task run --wait: %s did not complete in %s", id, timeout)
}

// ── list / get ───────────────────────────────────────────────────────────────

type tasksConfig struct {
	Runner string `cli:"name=runner desc='only the tasks on this runner (verse.runners.<kind>.<name>, or local for the ones that named none)'"`
}

var psFlags = &tasksConfig{}

// `ps` is an alias like `tasks` is: bare it lists, which is what anyone typing it expects.
// It could not be one while `task` was a group with no listing of its own — `verse ps` was
// an error ("no command provided") — so it lived on the plural instead.
func taskCmd() *cli.Command {
	opts, err := cli.StructOpts(psFlags)
	if err != nil {
		panic(err)
	}
	return filteredCollection("task", "tasks", "/tasks",
		"task — the tasks in the runtime (also: ps) [--runner <ref>]; act on one by id",
		opts, func() url.Values {
			// The name a spawn WROTE, which is what an operator remembers and what keeps
			// answering after the runner entity is gone.
			q := url.Values{}
			if psFlags.Runner != "" {
				q.Set("runner", psFlags.Runner)
			}
			return q
		}, taskSubs()...).
		WithAliases("ps")
}

type taskGetConfig struct{ *cli.Command }

func taskGetCmd() *cli.Command {
	cfg := &taskGetConfig{}
	return cli.NewCommandAt(&cfg.Command, "get").
		WithSynopsis("get a task: get <id>").WithRun(cfg.run)
}

func (cfg *taskGetConfig) run(cc *cli.Context, args []string) error {
	id, err := oneArg(cfg.Command, cc, args, "task get wants an id")
	if err != nil {
		return err
	}
	resp, err := httpGet("/tasks/"+id, nil)
	if err != nil {
		return fmt.Errorf("task get: %w", err)
	}
	return printBody(cc, resp)
}

// ── attach (stream the task read plane) ──────────────────────────────────

type taskAttachConfig struct {
	*cli.Command
	// Who answers a gate that arrives while attached. Without it the prompt is not offered:
	// answering has required attribution since a decision became a durable record, and this
	// path was still posting without it — so it asked, you said yes, and it failed with "by is
	// required". Prompting into a refusal is worse than not prompting.
	As string `cli:"name=as desc='identity for a gate answered while attached (a decision names who made it)'"`
}

func taskAttachCmd() *cli.Command {
	cfg := &taskAttachConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "attach").
		WithSynopsis("attach to a task's read plane: attach <id> [--as <who>]").
		WithOpts(opts...).WithRun(cfg.run)
}

func (cfg *taskAttachConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	id, err := oneArg(cfg.Command, cc, args, "task attach wants an id")
	if err != nil {
		return err
	}
	// `gate list` prints gate ids, so reaching for one here is the natural mistake — and
	// the id it wants is right there in the same record, under `task`. Say so, rather
	// than making a 404 stand in for the explanation.
	if pid, _, ok := strings.Cut(id, task.GateSep); ok {
		return fmt.Errorf("task attach takes a task id, but %q is a gate id — did you mean %q?", id, pid)
	}
	return streamEvents(cc, id, principal(cfg.As))
}

// streamEvents streams a task's read plane (SSE) to stdout — the agent's words, each
// action, and any gate it blocks on. On an interactive terminal it also PROMPTS you to
// approve/deny each gate as it blocks and resolves it inline (the agent unblocks and the
// stream continues); piped/non-tty it just streams. Runs until the stream ends or you ^C.
func streamEvents(cc *cli.Context, id, as string) error {
	resp, err := httpGet("/tasks/"+id+"/events", nil)
	if err != nil {
		return fmt.Errorf("task attach: %w", err)
	}
	defer resp.Body.Close()
	// Every non-streaming route checks its status (printBody does it centrally); this one
	// did not, and a stream is exactly where the omission hides. The reader below keeps
	// only "data: " lines, so a 404 — or any body that is not an event stream — scans to
	// zero output and returns nil: a task that does not exist looks identical to one
	// with nothing to say, and attach exits 0 having told you nothing.
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		// A 404 from the events route and a 404 about the id are different facts. A task
		// that failed at spawn has no stream to attach to, so the transport is telling the
		// truth about the question it was asked and nothing about the run: reported as-is
		// it says "404 page not found" while the record says which runner refused it and
		// why. Ask the record first (git-issue ykpt9g3eh12kss9wfhn0).
		if why := recordedFailure(id); why != "" {
			return fmt.Errorf("task %s: %s", id, why)
		}
		return fmt.Errorf("task attach %s: %s", id, msg)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		return fmt.Errorf("task attach %s: not an event stream (Content-Type %q)", id, ct)
	}
	out, err := outFmt()
	if err != nil {
		return err
	}
	in := streamFmt(resp)
	color := colorize(cc.Out)
	// Only offer to answer if there is a name to answer AS. A gate still shows either way —
	// attaching is watching, and watching never required an identity.
	interactive := isTTY() && as != ""
	stdin := bufio.NewReader(os.Stdin)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		// Wire, always: a transcript is a running commentary, and the loop below reads each
		// frame as a task.Frame to know when the task is over — so one frame per line is what
		// the person watching and the code watching both want.
		fmt.Fprintln(cc.Out, renderFrame(data, in, out, color, true))
		// task.Frame, not a hand-copied subset of it: the six fields this loop reads
		// were re-declared here with their tony tags, so a rename on the emitting side
		// would have gone unnoticed and simply stopped populating them.
		var ev task.Frame
		_ = codec.Unmarshal([]byte(data), &ev, in)
		// The task reached a terminal status — the stream won't emit more, so detach
		// (else attach would hang waiting on a closed-but-not-EOF stream).
		if ev.Kind == "status" && isTerminalStatus(ev.Text) {
			return nil
		}
		// A gate is worth naming even when this attach cannot answer it: the run is stopped,
		// and a reader watching a stream go quiet should be told why and what would move it.
		if !interactive && ev.Kind == "pending" && ev.ID != "" {
			if opts := (task.GateQuestion{Options: ev.Options}).OptionsOrDefault(); yesOrNo(opts) {
				fmt.Fprintf(cc.Out, "  ⏸ gate %q (%s) — answer it: verse gate approve %s --as <who>\n",
					ev.Gate, ev.Tool, ev.ID)
			} else {
				fmt.Fprintf(cc.Out, "  ⏸ gate %q (%s) — answer it: verse gate %s --answer <%s> --as <who>\n",
					ev.Gate, ev.Tool, ev.ID, strings.Join(task.OptionIDs(opts), "|"))
			}
		}
		// A pending gate blocks the task HERE until answered — prompt and resolve inline.
		if interactive && ev.Kind == "pending" {
			pid := ev.Task
			if pid == "" {
				pid, _, _ = strings.Cut(ev.ID, task.GateSep)
			}
			choice := promptGate(cc, stdin, ev)
			if choice == "" {
				fmt.Fprintf(cc.Out, "  left standing — answer it: verse gate %s --answer <option> --as %s\n", ev.ID, as)
				continue
			}
			if err := resolveTaskGate(pid, ev.ID, choice, as); err != nil {
				fmt.Fprintf(cc.Err, "  resolve %s: %v\n", ev.ID, err)
			} else if choice != task.Proceed && choice != task.Deny {
				fmt.Fprintf(cc.Out, "  → %s: %s\n", ev.ID, choice)
			} else if choice == task.Proceed {
				fmt.Fprintf(cc.Out, "  → approved %s\n", ev.ID)
			} else {
				fmt.Fprintf(cc.Out, "  → denied %s\n", ev.ID)
			}
		}
	}
	return nil
}

// recordedFailure asks the server what it knows about a task that could not be attached to:
// its own account, in one line, or empty when it has none.
//
// Empty covers three different situations on purpose — the task is unknown, it is alive, or
// it ended without a reason — because the caller does the same thing in all three: report
// the transport error it already has. Only a terminal status WITH a reason displaces that,
// since only then is there something better to say.
func recordedFailure(id string) string {
	resp, err := httpGet("/tasks/"+id, nil)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return "" // the id is unknown here too: the 404 the caller has is the whole story
	}
	var snap task.Instance
	decodeBody(resp, &snap)
	if !snap.Status.Terminal() || snap.Err == "" {
		return ""
	}
	return snap.Err
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

// promptGate asks how to answer a blocked gate and returns the option chosen, or "" for
// none. A two-option question is the yes-or-no it has always been, default deny; one with
// options of its own lists them, and a number or an id picks one. An empty or unknown line
// on a listed question chooses nothing: the question stands, and the caller says how to
// answer it later, rather than posting a deny the record may not even carry.
func promptGate(cc *cli.Context, in *bufio.Reader, ev task.Frame) string {
	opts := task.GateQuestion{Options: ev.Options}.OptionsOrDefault()
	if yesOrNo(opts) {
		fmt.Fprintf(cc.Out, "  ⏸ gate %q (%s) — approve? [y/N] ", ev.Gate, ev.Tool)
		line, _ := in.ReadString('\n')
		line = strings.TrimSpace(strings.ToLower(line))
		if line == "y" || line == "yes" {
			return task.Proceed
		}
		return task.Deny
	}
	fmt.Fprintf(cc.Out, "  ⏸ gate %q (%s) — choose:\n", ev.Gate, ev.Tool)
	for i, o := range opts {
		need := ""
		if o.Need > 1 {
			need = fmt.Sprintf(" (needs %d)", o.Need)
		}
		fmt.Fprintf(cc.Out, "    %d. %s%s\n", i+1, o.ID, need)
	}
	fmt.Fprint(cc.Out, "  > ")
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(opts) {
		return opts[n-1].ID
	}
	for _, o := range opts {
		if o.ID == line {
			return o.ID
		}
	}
	return ""
}

// yesOrNo reports whether a question's options are the default two, in which case the
// prompt is the one an operator already knows.
func yesOrNo(opts []task.Option) bool {
	return len(opts) == 2 && opts[0].ID == task.Proceed && opts[1].ID == task.Deny
}

// resolveTaskGate answers a task's pending decision by option.
func resolveTaskGate(pid, decisionID, choice, by string) error {
	resp, err := httpPost("/tasks/"+pid+"/resolve",
		api.Resolve{ID: decisionID, Choice: choice, By: by})
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

type taskCancelConfig struct{ *cli.Command }

func taskCancelCmd() *cli.Command {
	cfg := &taskCancelConfig{}
	return cli.NewCommandAt(&cfg.Command, "cancel").
		WithSynopsis("cancel a task: cancel <id>").WithRun(cfg.run)
}

func (cfg *taskCancelConfig) run(cc *cli.Context, args []string) error {
	id, err := oneArg(cfg.Command, cc, args, "task cancel wants an id")
	if err != nil {
		return err
	}
	return noContent(cc, "task cancel", "/tasks/"+id+"/cancel", nil, id)
}

type taskSayConfig struct{ *cli.Command }

func taskSayCmd() *cli.Command {
	cfg := &taskSayConfig{}
	return cli.NewCommandAt(&cfg.Command, "say").
		WithSynopsis("say something to a running task (re-prompt): say <id> <text…>").WithRun(cfg.run)
}

func (cfg *taskSayConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("%w: task say wants an id and text", cli.ErrUsage)
	}
	id, text := args[0], strings.Join(args[1:], " ")
	return noContent(cc, "task say", "/tasks/"+id+"/say",
		api.Say{Instruction: text}, id)
}

// ── vector (slide autonomy on a running task — the re-vector surface) ──────

type taskVectorConfig struct {
	*cli.Command
	// Not "from JSON": codec.ReadFile takes the dialect from the suffix, so a .tony vector
	// is as readable as a .json one — and tony is what the rest of the CLI writes.
	File string `cli:"name=file aliases=f desc='load a full gate vector from FILE (gates + a default; .tony/.json/.yaml); overrides the mode arg'"`
	As   string `cli:"name=as desc='operator identity to attribute the slide to (recorded on the read plane)'"`
}

func taskVectorCmd() *cli.Command {
	cfg := &taskVectorConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "vector").
		WithSynopsis("vector <id> <auto|ask|park> [--file <vector.tony>] — slide autonomy on a running task").
		WithOpts(opts...).WithRun(cfg.run)
}

// run re-vectors a task: the common form sets the default autonomy (auto|ask|park); --file
// supplies a full gate vector. The vector rides a PUT to /tasks/{id}/vector, which is
// runner-agnostic — the runtime slides a local task, the broker relays it to a remote one.
func (cfg *taskVectorConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("%w: task vector wants a task id", cli.ErrUsage)
	}
	id := args[0]
	var vec task.Vector
	switch {
	case cfg.File != "":
		// The file's suffix names its dialect; an extensionless file uses --ifmt or the
		// configured default.
		if err := codec.ReadFile(cfg.File, &vec); err != nil {
			return fmt.Errorf("task vector: %s is not a valid vector: %w", cfg.File, err)
		}
	case len(args) >= 2:
		if err := vec.Default.UnmarshalText([]byte(args[1])); err != nil {
			return fmt.Errorf("%w: task vector mode must be auto|ask|park", cli.ErrUsage)
		}
	default:
		return fmt.Errorf("%w: task vector wants a mode (auto|ask|park) or --file", cli.ErrUsage)
	}
	resp, err := httpPut("/tasks/"+id+"/vector", api.SetVector{Vector: &vec, By: principal(cfg.As)})
	if err != nil {
		return fmt.Errorf("task vector: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("task vector: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	fmt.Fprintln(cc.Out, "task vector", id, strings.ToLower(vec.Default.String()))
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// asOverride is the principal a command's --as named, once it has: from then on every request
// this invocation makes is made as them, and not as the config's user. One command runs per
// process, so a variable is the whole of the plumbing a per-request field would need.
var asOverride string

// principal resolves who a command acts as: its --as where it took one, else the config's
// `user:`. Recording the answer is what puts it on the wire (see skewCheck), so a ballot cast
// `--as bob` is also a request MADE by bob, and the two cannot disagree.
func principal(as string) string {
	if as != "" {
		asOverride = as
	}
	if asOverride != "" {
		return asOverride
	}
	if appCfg != nil {
		if appCfg.Agent != "" {
			return appCfg.Agent
		}
		return appCfg.User
	}
	return ""
}

// principalKind is the kind the principal acts under: `agent` when the CLI is configured as an
// operator agent and no --as named a person instead, and "" — a user — otherwise.
func principalKind() string {
	if asOverride == "" && appCfg != nil && appCfg.Agent != "" {
		return "agent"
	}
	return ""
}

// requireAs refuses an unattributed action. A claim, a ballot and a report all name who
// made them, and the refusal is a USAGE error in each — so the command whose --as is most
// obviously load-bearing prints the usage rather than a bare line. What it answers is the
// principal to act as: --as, or the config's user when the flag was left off.
func requireAs(as, noun, why string) (string, error) {
	if p := principal(as); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("%w: --as <%s> is required (%s): pass it, or set `user:` in the verse config", cli.ErrUsage, noun, why)
}

// oneArg parses flags (anywhere) and returns the single required positional.
// oneArg is the single positional a command takes — an id, a name, a ref, a subject address.
//
// A SECOND POSITIONAL IS REFUSED rather than ignored, and that is the whole of what this adds
// over reading args[0]. These commands take ONE TOKEN where `state` takes a vector of steps, so
// somebody typing the spelling that works next door gets it wrong here in the worst way: `verse
// flow show demo seq 1` used to send the subject `demo` and answer, truthfully and about
// something else, with every flow in the `demo` system. A wrong answer that looks right is worse
// than an error, and the error can say which spelling this command takes.
func oneArg(cmd *cli.Command, cc *cli.Context, args []string, want string) (string, error) {
	args, err := cmd.Parse(cc, args)
	if err != nil {
		return "", err
	}
	if len(args) < 1 {
		return "", fmt.Errorf("%w: %s", cli.ErrUsage, want)
	}
	return theArg(args, want)
}

// optionalArg is oneArg for a command that means something without it: a collection that lists
// bare and acts on one member when named. The second positional is refused for oneArg's reason
// — it is the `data` spelling typed at a command that takes one token, and reading only the
// first word would answer about something else.
func optionalArg(cmd *cli.Command, cc *cli.Context, args []string, want string) (string, error) {
	args, err := cmd.Parse(cc, args)
	if err != nil {
		return "", err
	}
	if len(args) == 0 {
		return "", nil
	}
	return theArg(args, want)
}

func theArg(args []string, want string) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("%w: %s — and this takes ONE token, so %q is after it. (An address "+
			"as separate steps is `verse data`'s; here it is one argument.)",
			cli.ErrUsage, want, args[1])
	}
	return args[0], nil
}

func noContent(cc *cli.Context, what, path string, body any, id string) error {
	resp, err := httpPost(path, body)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
	case http.StatusAccepted:
		// Recorded, not settled: an answer to a question that takes more than one voice.
		msg, _ := io.ReadAll(resp.Body)
		fmt.Fprintln(cc.Out, what, id+":", strings.TrimSpace(string(msg)))
		return nil
	default:
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
