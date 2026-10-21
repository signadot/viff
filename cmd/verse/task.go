package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// taskSubs is the group's verbs. It is a function rather than a slice so each rootCmd() gets
// its OWN instances: a Command is parented by whoever it is added to, and it carries the flag
// values of the invocation, so a package-level slice would be shared across command trees.
func taskSubs() []*cli.Command {
	return []*cli.Command{
		taskRunCmd(),
		taskGetCmd(),
		taskAttachCmd(),
		taskCancelCmd(),
		taskSayCmd(),
		taskAutonomyCmd(),
		taskAnswerCmd(true),
		taskAnswerCmd(false),
	}
}

// ── resolve (answer a task's own tool gate) ──────────────────────────────

// approve/deny rather than `resolve --approve|--deny`, for the reasons in gateAnswerCmd:
// the answer is a verb, so it is the command.
type taskAnswerConfig struct {
	*cli.Command
	Say   string `cli:"name=say aliases=s desc='words to say to the executor on resolution'"`
	As    string `cli:"name=as desc='identity answering the gate; the configured user when left off (a decision names who made it)'"`
	Force bool   `cli:"name=force desc='answer yourself an ask a resolver you granted serves for you'"`
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
	// Same body as `question approve`, --say included: this route applies the answer where the
	// task is held, the local registry or a remote runtime over the wire, which is a difference
	// in WHERE it goes, not in what can be said. The flag was missing here alone, so "yes, but
	// do it this way" was unsayable on a task's own gate.
	body := api.Resolve{ID: did, Choice: choiceOf(approve), By: cfg.As, Force: cfg.Force}
	if cfg.Say != "" {
		body.Say = []string{cfg.Say}
	}
	return noContent(cc, "task "+verb, "/tasks/"+pid+"/resolve", body, did)
}

// ── run ──────────────────────────────────────────────────────────────────────

type taskRunConfig struct {
	*cli.Command
	// File is the spawn: what runs, as a charter's spawn writes it (`prompt` or `script`,
	// `agent`, `environment`, `autonomy`, `answer`, `runner`, `subject`, …), meaning only what it
	// says. The flags beside it are about this invocation, never the work.
	File   string `cli:"name=file aliases=f desc='the spawn to run, as a charter writes one, from FILE (- = stdin); also accepts a positional [file]'"`
	Wait   bool   `cli:"name=wait desc='block until the task completes, then print the result (composes with --attach)'"`
	Attach bool   `cli:"name=attach desc='stream the live transcript (words, actions, questions) until it ends'"`
	// Converse is on by default and means something only with -attach: a person is at the
	// terminal, so when the model goes quiet they are handed the turn (a question on the
	// docket) and the run ends when they end it — Ctrl-D, or `end` from anywhere. Without
	// -attach nobody is there to take a turn, so the run ends when the model stops, whatever
	// this says. -no-converse is an attached run that should end when the model stops.
	Converse bool `cli:"name=converse default=true desc='with -attach: hand me the turn when the model goes quiet, until I end it (Ctrl-D); -no-converse ends the run when the model stops'"`
	// Name is a LABEL, not an address. The id is derived from it and returned, because a
	// caller-chosen id is a caller-chosen prefix on every gate question the run raises
	// (`<id>~d1`) — and answering one recovers the owner by cutting at the first `~`, so a
	// task called `a~b` hands its questions to a task called `a`. The trigger side has
	// always sanitised its own ids (safeSeg) for exactly this; the ad-hoc face never had to,
	// because it never let anyone choose one.
	Name string `cli:"name=name desc='label for the run; the id is derived from it and printed'"`
	// Who is running this — used twice, because it is one fact. A gate that arrives while
	// attached is answered as this (watching needs no identity; answering has needed one
	// since a decision became a durable record), and it rides the spawn as the run's
	// PRINCIPAL, so a runner whose effects leave the verse can say on whose account.
	// Self-asserted, like every other `--as` here.
	As string `cli:"name=as desc='who is running this: the run principal, and the identity for a gate answered while attached'"`
}

func taskRunCmd() *cli.Command {
	cfg := &taskRunConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "run").
		WithSynopsis("run a task: run -f <spawn file> (- = stdin) | [file] [--name <label>] [--attach] [--wait]").
		WithOpts(opts...).
		WithRun(cfg.run)
}

// givesASystem is whether an environment gives some system a step is of, beside the network.
func givesASystem(env systems.Environment) bool {
	for name := range env.Systems {
		if name != "network" {
			return true
		}
	}
	return false
}

func (cfg *taskRunConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	src := cfg.File
	switch {
	case src == "" && len(args) == 1:
		src = args[0]
	case src == "" && len(args) == 0:
		return fmt.Errorf("%w: task run takes the spawn to run: -f <file>, - for stdin", cli.ErrUsage)
	case len(args) > 0:
		return fmt.Errorf("%w: task run takes one spawn file, and got %q", cli.ErrUsage, strings.Join(args, " "))
	}
	// Read here, strictly, so a key the spawn does not have is a message on the terminal of
	// whoever wrote it; the server reads it again, as strictly.
	data, srcFmt, err := codec.ReadSource(cc.In, src, inFmt())
	if err != nil {
		return fmt.Errorf("task run: %w", err)
	}
	var spec launch.Spec
	if err := codec.UnmarshalStrict(data, &spec, srcFmt); err != nil {
		return fmt.Errorf("task run: %s is not a spawn: %w", src, err)
	}
	if err := spec.CheckByHand(); err != nil {
		return fmt.Errorf("task run: %s: %w", src, err)
	}
	// A prompt whose environment gives no system is an agent with no tool: it can read and
	// answer, and every step it tries is refused. Said once, before the run, in what to add.
	if spec.Prompt != "" && !givesASystem(spec.Environment) {
		fmt.Fprintln(cc.Err, "task run: the spawn's environment gives no system, so the agent gets no tool and every "+
			"step is refused; name what it works with under environment.systems (`files: {write: {}}`, "+
			"`commands: {run: {}}`, `git: [{repo: …, fetch: {}}]`, …)")
	}
	by, err := principal(cfg.As)
	if err != nil {
		return err
	}
	resp, err := httpPost("/tasks", api.Spawn{
		Spawn: spec, Name: cfg.Name, Principal: by,
		// A conversation is an agent's: a script has no turn to hand anybody.
		Converse: cfg.Attach && cfg.Converse && spec.Script == "",
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
		why := strings.TrimSpace(string(msg))
		// The refusal a first run most likely meets: a spawn means only what it says, and one
		// that names no network has none, which a runner building only open refuses.
		if _, named := spec.Environment.Systems["network"]; !named && strings.Contains(why, "environment.systems.network") {
			why += "\n(a spawn that names no network has none; to give it the host's, write " +
				"`environment: {systems: {network: {mode: open}}}`)"
		}
		return fmt.Errorf("task run: %s: %s", resp.Status, why)
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
		// From the start. A live attach joins the stream at its next frame, and the task was
		// running before this request reached it, so its first frames were already gone — a
		// run that answered in one turn printed from cursor 2. This command spawned it, so
		// everything it has said is what was asked for.
		if err := streamEvents(cc, sp.ID, by, "0"); err != nil {
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
	return waitForResult(cc, sp.ID)
}

// taskSnapshot reads a task's snapshot into v, for a command that goes on to act on what it
// says. An answer that is not a snapshot — the task is not in the asker's view, the store did
// not answer — is the server's own words as an error (client.Error), so no command describes a
// task it did not read.
func taskSnapshot(id string, v any) error {
	r, err := httpGet("/tasks/"+id, nil)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode >= http.StatusBadRequest {
		return client.StatusError(r, "reading task "+id)
	}
	return client.Decode(r, v)
}

// waitPoll is how often a wait asks again.
const waitPoll = 50 * time.Millisecond

// waitForResult polls a task until it leaves `running`, then prints its status and the
// result it produced.
//
// IT WAITS UNTIL THE TASK ENDS OR THE COMMAND IS INTERRUPTED, and gives up at no time of its
// own: a task waits on a model or on a person, and neither has a length this command knows.
//
// A read that did not happen is not the end of the wait — the store answers again in a moment,
// and the task has gone on meanwhile — so it asks again, and says what failed if it is
// interrupted before a read succeeds. A task that is not in the asker's view, or that they are
// refused, is the end of it: asking again changes nothing.
func waitForResult(cc *cli.Context, id string) error {
	var last error
	for {
		var snap struct {
			Status string `tony:"field=status"`
			Result string `tony:"field=result"`
		}
		switch err := taskSnapshot(id, &snap); {
		case err == nil:
			last = nil
			if snap.Status != "" && snap.Status != "running" {
				fmt.Fprintln(cc.Out, id, snap.Status)
				if snap.Result != "" { // the agent's final answer
					fmt.Fprintf(cc.Out, "\n%s\n", snap.Result)
				}
				return nil
			}
		case client.Unavailable(err):
			last = err
		default:
			return fmt.Errorf("task run --wait: %w", err)
		}
		select {
		case <-cc.Go.Done():
			if last != nil {
				return fmt.Errorf("task run --wait: interrupted, and the last read failed: %w", last)
			}
			return fmt.Errorf("task run --wait: interrupted while %s was still running", id)
		case <-time.After(waitPoll):
		}
	}
}

// ── list / get ───────────────────────────────────────────────────────────────

type tasksConfig struct {
	Runner string `cli:"name=runner desc='only the tasks on this runner (verse.runners.<name>, or local for the ones that named none)'"`
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
	// `question` prints question ids, so reaching for one here is the natural mistake — and
	// the id it wants is right there in the same record, under `task`. Say so, rather
	// than making a 404 stand in for the explanation.
	if pid, _, ok := strings.Cut(id, task.GateSep); ok {
		return fmt.Errorf("task attach takes a task id, but %q is a gate id — did you mean %q?", id, pid)
	}
	by, err := principal(cfg.As)
	if err != nil {
		return err
	}
	return streamEvents(cc, id, by, "")
}

// streamEvents streams a task's read plane (SSE) to stdout — the agent's words, each
// action, and any gate it blocks on. On an interactive terminal it also PROMPTS you to
// approve/deny each gate as it blocks and resolves it inline (the agent unblocks and the
// stream continues); piped/non-tty it just streams. Runs until the stream ends or you ^C.
//
// since is the cursor to read from — "" joins live, "0" replays everything retained — and it is
// the caller's to say: `task run` wants the whole run it just started, `task attach` the run
// from here on.
func streamEvents(cc *cli.Context, id, as, since string) error {
	var q url.Values
	if since != "" {
		q = url.Values{"since": {since}}
	}
	resp, err := httpGet("/tasks/"+id+"/events", q)
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
	// Only offer to answer if there is a name to answer AS. A question still shows either way —
	// attaching is watching, and watching never required an identity.
	interactive := as != ""
	// Frames and typed lines both arrive on channels, so a prompt never stops the stream: a
	// question answered elsewhere while its prompt is up closes the prompt here, and a task
	// that ends while somebody is deciding is seen to end.
	frames := scanFrames(resp.Body)
	lines := readLines(cc.In)
	// Lines typed while no prompt is up are for the task's NEXT question. A line cannot answer
	// a question it was typed before — on a yes-or-no gate it would deny an action the person
	// never saw — so it waits, and a `reply` prompt takes everything waiting as one message,
	// the way a person's paragraphs are one message.
	var queued []string
	var open *prompt
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				lines = nil // EOF: nothing more will be typed; the stream goes on
				// …except a conversation, which EOF ends: Ctrl-D at the turn is the
				// person leaving, and the turn is answered `end` on their behalf.
				if open != nil && open.turn {
					answerPrompt(cc, open, task.End, nil, as)
					open = nil
				}
				continue
			}
			if open == nil {
				queued = append(queued, line)
				fmt.Fprintln(cc.Out, "  (queued for the task's next question)")
				continue
			}
			choice, say, ok := open.take(line)
			if !ok {
				open.show(cc.Out) // not an answer: ask again
				continue
			}
			answerPrompt(cc, open, choice, say, as)
			open = nil

		case data, ok := <-frames:
			if !ok {
				return nil
			}
			// Wire, always: a transcript is a running commentary, and the loop below reads
			// each frame as a task.Event to know when the task is over — so one frame per
			// line is what the person watching and the code watching both want.
			fmt.Fprintln(cc.Out, renderFrame(data, in, out, color, true))
			// task.Event, not a hand-copied subset of it: the six fields this loop reads
			// were re-declared here with their tony tags, so a rename on the emitting side
			// would have gone unnoticed and simply stopped populating them.
			var ev task.Event
			_ = codec.Unmarshal([]byte(data), &ev, in)
			// The task reached a terminal status — the stream won't emit more, so detach
			// (else attach would hang waiting on a closed-but-not-EOF stream).
			if ev.Kind == task.EventStatus && isTerminalStatus(ev.Text) {
				return nil
			}
			if ev.Kind == task.EventDecision && open != nil && ev.ID == open.id {
				fmt.Fprintf(cc.Out, "  answered by %s\n", ev.By)
				open = nil
				continue
			}
			if ev.Kind != task.EventPending || ev.ID == "" {
				continue
			}
			pr := newPrompt(ev)
			// A question is worth naming even when this attach cannot answer it: the run is
			// stopped, and a reader watching a stream go quiet should be told why and what
			// would move it. That is also the case once stdin is closed and nothing is
			// waiting to be said — except that a `reply` still takes what was typed before.
			if pr.reply && len(queued) > 0 && interactive {
				if !pr.turn {
					fmt.Fprintf(cc.Out, "  ❓ %s\n", ev.Text)
				}
				answerPrompt(cc, pr, task.Reply, []string{strings.Join(queued, "\n")}, as)
				queued = nil
				continue
			}
			if pr.turn && interactive && lines == nil {
				answerPrompt(cc, pr, task.End, nil, as) // stdin already closed: the person has left
				continue
			}
			if !interactive || lines == nil {
				fmt.Fprintln(cc.Out, "  ⏸ "+pr.hint(as))
				continue
			}
			open = pr
			open.show(cc.Out)
		}
	}
}

// scanFrames hands the stream's frames over a channel, closed when the stream ends.
func scanFrames(body io.Reader) <-chan string {
	ch := make(chan string, 16)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				ch <- data
			}
		}
	}()
	return ch
}

// readLines hands what is typed over a channel, line by line, closed at EOF. A nil reader is
// a closed channel: nothing will be typed.
func readLines(in io.Reader) <-chan string {
	ch := make(chan string)
	if in == nil {
		close(ch)
		return ch
	}
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(in)
		for sc.Scan() {
			ch <- sc.Text()
		}
	}()
	return ch
}

// prompt is one question put to the person at the terminal: which task, which question, and
// what an answer to it looks like.
type prompt struct {
	pid, id string
	text    string // the question, in the asker's words
	gate    string
	tool    string
	action  string // the action in one line, when the frame carried one
	opts    []task.Option
	reply   bool // an answer in words
	turn    bool // the person's turn in a conversation: words, or Ctrl-D to end it
	yesno   bool // the default two options
}

func newPrompt(ev task.Event) *prompt {
	pid := ev.Task
	if pid == "" {
		pid, _, _ = strings.Cut(ev.ID, task.GateSep)
	}
	opts := task.GateQuestion{Options: ev.Options}.OptionsOrDefault()
	pr := &prompt{pid: pid, id: ev.ID, text: ev.Text, gate: ev.Gate, tool: ev.Tool, action: ev.Action, opts: opts}
	for _, o := range opts {
		if o.ID == task.Reply {
			pr.reply = true
		}
	}
	pr.turn = ev.Gate == "turn"
	pr.yesno = yesOrNo(opts)
	return pr
}

// show puts the question in front of the person, in the form its options take.
func (pr *prompt) show(w io.Writer) {
	switch {
	case pr.turn:
		fmt.Fprint(w, "  > ")
	case pr.reply:
		fmt.Fprintf(w, "  ❓ %s\n  > ", pr.text)
	case pr.yesno:
		fmt.Fprintf(w, "  ⏸ gate %q (%s) — approve? [y/n] ", pr.gate, pr.what())
	default:
		fmt.Fprintf(w, "  ⏸ gate %q (%s) — choose:\n", pr.gate, pr.what())
		for i, o := range pr.opts {
			need := ""
			if o.Need > 1 {
				need = fmt.Sprintf(" (needs %d)", o.Need)
			}
			says := ""
			if o.Says != "" {
				says = " — " + o.Says
			}
			fmt.Fprintf(w, "    %d. %s%s%s\n", i+1, o.ID, need, says)
		}
		fmt.Fprint(w, "  > ")
	}
}

// take reads a typed line as an answer. ok is false when it is not one: a yes-or-no question
// wants a yes or a no, and anything else is asked again rather than read as a refusal.
func (pr *prompt) take(line string) (choice string, say []string, ok bool) {
	line = strings.TrimSpace(line)
	if pr.reply {
		return task.Reply, []string{line}, true
	}
	if pr.yesno {
		switch strings.ToLower(line) {
		case "y", "yes":
			return task.Proceed, nil, true
		case "n", "no":
			return task.Deny, nil, true
		}
		return "", nil, false
	}
	if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(pr.opts) {
		return pr.opts[n-1].ID, nil, true
	}
	for _, o := range pr.opts {
		if o.ID == line {
			return o.ID, nil, true
		}
	}
	return "", nil, false
}

// hint is the command that answers this question from anywhere, for a reader who cannot here.
func (pr *prompt) hint(as string) string {
	who := as
	if who == "" {
		who = "<who>"
	}
	switch {
	case pr.reply:
		return fmt.Sprintf("%s — answer it: verse question %s --answer %s --say \"…\" --as %s",
			pr.text, pr.id, task.Reply, who)
	case pr.yesno:
		return fmt.Sprintf("gate %q (%s) — answer it: verse question approve %s --as %s", pr.gate, pr.what(), pr.id, who)
	default:
		return fmt.Sprintf("gate %q (%s) — answer it: verse question %s --answer <%s> --as %s",
			pr.gate, pr.what(), pr.id, strings.Join(task.OptionIDs(pr.opts), "|"), who)
	}
}

// what names the action: the tool, and what it would do when the frame said.
func (pr *prompt) what() string {
	if pr.action == "" {
		return pr.tool
	}
	return pr.tool + ": " + pr.action
}

// answerPrompt sends the answer and says what happened.
func answerPrompt(cc *cli.Context, pr *prompt, choice string, say []string, as string) {
	if err := resolveTaskGate(pr.pid, pr.id, choice, say, as); err != nil {
		fmt.Fprintf(cc.Err, "  resolve %s: %v\n", pr.id, err)
		return
	}
	switch choice {
	case task.Proceed:
		fmt.Fprintf(cc.Out, "  → approved %s\n", pr.id)
	case task.Deny:
		fmt.Fprintf(cc.Out, "  → denied %s\n", pr.id)
	case task.Reply:
		fmt.Fprintf(cc.Out, "  → sent\n")
	case task.End:
		fmt.Fprintf(cc.Out, "  → ended\n")
	default:
		fmt.Fprintf(cc.Out, "  → %s: %s\n", pr.id, choice)
	}
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
	var snap api.Task
	decodeBody(resp, &snap)
	if !snap.Status.Terminal() || snap.Err == "" {
		return ""
	}
	return snap.Err
}

// isTerminalStatus reports whether a status EventStatus text is an end state — covering the
// runtime (completed | cancelled | "driver error") vocabularies.
func isTerminalStatus(text string) bool {
	switch text {
	case "done", "cancelled", "failed", "orphaned":
		return true
	}
	return strings.Contains(text, "error")
}

// yesOrNo reports whether a question's options are the default two, in which case the
// prompt is the one an operator already knows.
func yesOrNo(opts []task.Option) bool {
	return len(opts) == 2 && opts[0].ID == task.Proceed && opts[1].ID == task.Deny
}

// resolveTaskGate answers a task's pending decision by option, with what the answerer said.
func resolveTaskGate(pid, decisionID, choice string, say []string, by string) error {
	resp, err := httpPost("/tasks/"+pid+"/resolve",
		api.Resolve{ID: decisionID, Choice: choice, Say: say, By: by})
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
		WithSynopsis("say something to a running task, as an ask it hears at its next boundary: say <id> <text…>").WithRun(cfg.run)
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
	by, err := principal("")
	if err != nil {
		return err
	}
	resp, err := httpPost("/tasks/"+id+"/say", api.Say{Instruction: text, By: by})
	if err != nil {
		return fmt.Errorf("task say: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return statusError(resp, "task say")
	}
	// The words are an ask to the task (9ce3bva0, stage 6): `verse ask <id>` shows when it hears them.
	var out api.Asked
	decodeClose(resp, &out)
	fmt.Fprintf(cc.Out, "said to %s: %s\n", id, out.ID)
	return nil
}

// ── autonomy (replace the autonomy of a running task) ──────────────────────────

type taskAutonomyConfig struct {
	*cli.Command
	// Not "from JSON": codec.ReadFile takes the dialect from the suffix, so a .tony policy is
	// as readable as a .json one — and tony is what the rest of the CLI writes.
	File string `cli:"name=file aliases=f desc='load the autonomy from FILE (a list of rows; .tony/.json/.yaml)'"`
	As   string `cli:"name=as desc='operator identity to attribute the change to (recorded on the read plane)'"`
}

func taskAutonomyCmd() *cli.Command {
	cfg := &taskAutonomyConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "autonomy").
		WithSynopsis("autonomy <id> (<rows> | --file <autonomy.tony>) — replace the autonomy of a running task").
		WithOpts(opts...).WithRun(cfg.run)
}

// run replaces a task's autonomy with a whole one, given inline as tony or by --file. The runner
// takes it when each row reaches the task's environment, which is fixed for the run and lies
// within the runner's ceilings (runnerd.CheckAutonomy). It rides a PUT to /tasks/{id}/autonomy,
// which is runner-agnostic — the handle relays it, in process or over the wire — and applies to
// the task's later steps.
func (cfg *taskAutonomyConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("%w: task autonomy wants a task id", cli.ErrUsage)
	}
	id := args[0]
	var body api.SetAutonomy
	switch {
	case cfg.File != "" && len(args) >= 2:
		return fmt.Errorf("%w: task autonomy takes the rows or --file, not both", cli.ErrUsage)
	case cfg.File != "":
		if err := codec.ReadFileStrict(cfg.File, &body.Autonomy); err != nil {
			return fmt.Errorf("task autonomy: %s is not an autonomy: %w", cfg.File, err)
		}
	case len(args) >= 2:
		if err := codec.UnmarshalStrict([]byte(strings.Join(args[1:], " ")), &body.Autonomy, codec.Tony); err != nil {
			return fmt.Errorf("%w: task autonomy: %q is not an autonomy, a list of rows: %v", cli.ErrUsage, strings.Join(args[1:], " "), err)
		}
	default:
		return fmt.Errorf("%w: task autonomy wants the whole autonomy: its rows, or --file", cli.ErrUsage)
	}
	if err := body.Autonomy.Check(); err != nil {
		return fmt.Errorf("%w: task autonomy: autonomy%v", cli.ErrUsage, err)
	}
	if body.By, err = principal(cfg.As); err != nil {
		return err
	}
	resp, err := httpPut("/tasks/"+id+"/autonomy", body)
	if err != nil {
		return fmt.Errorf("task autonomy: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("task autonomy: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	fmt.Fprintln(cc.Out, "task autonomy", id, body.Autonomy.Summary())
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// asOverride is the principal a command's --as named, once it has: from then on every request
// this invocation makes is made as them, and not as the config's user. One command runs per
// process, so a variable is the whole of the plumbing a per-request field would need.
var asOverride string

// principal resolves who a command acts as, spelled in the grammar (entity.Grammar): its --as
// where it took one, else the config's `agent:` as `agent:<name>`, else whoever is logged in
// toward this verse (`verse account login`), else the config's `user:`, else the local user when the verse
// is on this machine (localUser) and nobody when it is not. A login comes before `user:` because
// its token is what the request carries, and the verse makes the request as whoever that proves. --as and
// `user:` take a principal or a bare email, which is `user:<email>`; a bare word is refused
// rather than guessed at, so `--as verse-runner` is an error and `--as runner:verse-runner` a
// worker. Recording the answer is what puts it on the wire (see skewCheck), so a ballot cast
// `--as bob@example.com` is also a request MADE by bob, and the two cannot disagree.
func principal(as string) (string, error) {
	if as != "" {
		g, err := entity.ParsePrincipal(as)
		if err != nil {
			return "", fmt.Errorf("--as %w", err)
		}
		asOverride = g.String()
	}
	if asOverride != "" {
		return asOverride, nil
	}
	if appCfg == nil {
		return "", nil
	}
	if appCfg.Agent != "" {
		name := appCfg.Agent
		if entity.KindOf(name) == "" {
			name = entity.AgentKind + ":" + name
		}
		g, err := entity.ParsePrincipal(name)
		if err != nil || g.Kind != entity.AgentKind {
			return "", fmt.Errorf("config agent: %q is not an agent's name — %s", appCfg.Agent, entity.Grammar())
		}
		return g.String(), nil
	}
	if who := loginPrincipal(); who != "" {
		return who, nil
	}
	if appCfg.User != "" {
		g, err := entity.ParsePrincipal(appCfg.User)
		if err != nil {
			return "", fmt.Errorf("config user: %w", err)
		}
		return g.String(), nil
	}
	return localUser(g.Addr), nil
}

// requireAs refuses an unattributed action. A claim, a ballot and a report all name who
// made them, and the refusal is a USAGE error in each — so the command whose --as is most
// obviously load-bearing prints the usage rather than a bare line. What it answers is the
// principal to act as: --as, or whoever the CLI acts as when the flag was left off (principal).
// Toward a verse on this machine that is always somebody, so the refusal is one a shared verse
// gives.
func requireAs(as, noun, why string) (string, error) {
	if p, err := principal(as); err != nil || p != "" {
		return p, err
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
