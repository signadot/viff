package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// verse flow: the work in flight and the work held from now on, through one set of flags. Bare,
// it lists live tasks and suspensions; its verbs act on what the same flags name — one run with
// --task, or a charter and a subject with --charter and --subject. Nothing is "every" by being
// left out: a verb that names nothing prints its usage and does nothing, and every charter or
// every subject is '*', said.

// flowFlags is the selection, declared once on `flow` and read by its verbs: the options are
// resolved along the command's path, so `verse flow suspend --task x` sets them here.
type flowFlags struct {
	Task      string `cli:"name=task desc='one run, by its task id — combines with neither --charter nor --subject'"`
	Charter   string `cli:"name=charter desc='<address>[:<rule>]: an installed charter and one rule of it; :<rule> names a baseline rule, * every charter'"`
	Subject   string `cli:"name=subject desc='an address, with * open in any segment; * alone is every subject'"`
	Recursive bool   `cli:"name=recursive aliases=r desc='the charter at --charter and every charter beneath it'"`
	Args      bool   `cli:"name=args desc='print each row as the flags that name it, one per line, for xargs -L1 verse flow <verb>'"`
}

const flowSynopsis = "flow [--task <id> | --charter <address>[:<rule>] [-r] --subject <address>] [--args] — " +
	"list live tasks and suspensions; verbs: suspend, resume, say, cancel, show"

func flowCmd() *cli.Command {
	sel := &flowFlags{}
	cfg := &struct{ *cli.Command }{}
	return cli.NewCommandAt(&cfg.Command, "flow").
		WithAliases("flows").
		WithSynopsis(flowSynopsis).
		WithOpts(append(mustOpts(sel), filterOpts()...)...).
		WithSubs(
			flowSuspendCmd(sel),
			flowResumeCmd(sel),
			flowSayCmd(sel),
			flowCancelCmd(sel),
			flowShowCmd(),
		).
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			if handled, err := dispatch(cfg.Command, cc, args); handled {
				return err
			}
			if err := noArgs(cfg.Command, "flow", args); err != nil {
				return err
			}
			return sel.list(cc)
		})
}

// query is the selection as `GET /flows` takes it.
func (sel *flowFlags) query() url.Values {
	q := url.Values{}
	for k, v := range map[string]string{"task": sel.Task, "charter": sel.Charter, "subject": sel.Subject} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if sel.Recursive {
		q.Set("recursive", "true")
	}
	return q
}

// body is the selection as a verb's request carries it.
func (sel *flowFlags) body() api.FlowSelect {
	return api.FlowSelect{Task: sel.Task, Charter: sel.Charter, Subject: sel.Subject, Recursive: sel.Recursive}
}

func (sel *flowFlags) list(cc *cli.Context) error {
	resp, err := httpGet("/flows", sel.query())
	if err != nil {
		return fmt.Errorf("flow: %w", err)
	}
	if !sel.Args {
		return printBody(cc, resp)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp, "flow")
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var rows []api.FlowRow
	if err := codec.Unmarshal(raw, &rows, respFmt(resp)); err != nil {
		return fmt.Errorf("flow: %w", err)
	}
	for _, row := range rows {
		fmt.Fprintln(cc.Out, rowArgs(row, g.Scope))
	}
	return nil
}

// rowArgs is a row as the flags that name it, each value a word a shell and xargs both read
// back: `--task <id>` for a live task, `--charter <c> [--recursive] --subject <s>` for a
// suspension, and the scope the listing was asked in.
func rowArgs(row api.FlowRow, scope string) string {
	var words []string
	flag := func(name, value string) { words = append(words, "--"+name, entity.ShellWord(value)) }
	switch row.Kind {
	case api.FlowRowTask:
		flag("task", row.Task)
	default:
		flag("charter", row.Charter)
		if row.Recursive {
			words = append(words, "--recursive")
		}
		flag("subject", row.Subject)
	}
	if scope != "" {
		flag("scope", scope)
	}
	return strings.Join(words, " ")
}

// narrowed refuses a verb whose selection does not say what it acts on, with the verb's usage —
// before anything is sent, so a verb typed to see its usage changes nothing.
func (sel *flowFlags) narrowed(verb string) error {
	usage := func(want string) error {
		return fmt.Errorf("%w: verse flow %s: %s", cli.ErrUsage, verb, want)
	}
	if sel.Args {
		return usage("--args prints the listing's rows; a verb takes the flags one row printed")
	}
	if sel.Task != "" && (sel.Charter != "" || sel.Subject != "" || sel.Recursive) {
		return usage("--task names one run, and combines with neither --charter nor --subject")
	}
	switch verb {
	case "suspend", "resume":
		if sel.Task == "" && (sel.Charter == "" || sel.Subject == "") {
			return usage("--task <id>, or --charter <address>[:<rule>] and --subject <address>; " +
				"every charter and every subject are '*', said")
		}
	case "say":
		if sel.Task == "" && sel.Charter == "" && sel.Subject == "" {
			return usage("--task, --charter or --subject: what to say it to")
		}
	case "cancel":
		if sel.Task == "" {
			return usage("--task <id>: cancel names one run; suspend a charter or a subject, then " +
				"cancel its runs one by one")
		}
	}
	return nil
}

type flowSuspendConfig struct {
	*cli.Command
	Why string `cli:"name=why desc='what a suspension is for — required, since nothing ends it but somebody reading this'"`
	As  string `cli:"name=as desc='who holds it; the configured user when left off'"`
}

func flowSuspendCmd(sel *flowFlags) *cli.Command {
	cfg := &flowSuspendConfig{}
	return cli.NewCommandAt(&cfg.Command, "suspend").
		WithSynopsis("suspend --task <id> | --charter <address>[:<rule>] [-r] --subject <address> --why <text> — " +
			"hold one run at its next boundary, or suspend a charter's work about a subject until resumed").
		WithOpts(mustOpts(cfg)...).
		WithRun(func(cc *cli.Context, args []string) error {
			if err := noVerbArgs(cfg.Command, cc, args, "suspend"); err != nil {
				return err
			}
			if err := sel.narrowed("suspend"); err != nil {
				return err
			}
			if sel.Task == "" && cfg.Why == "" {
				return fmt.Errorf("%w: verse flow suspend wants --why: a suspension says what it is for", cli.ErrUsage)
			}
			if _, err := principal(cfg.As); err != nil {
				return err
			}
			body := sel.body()
			body.Why = cfg.Why
			return postFlow(cc, "suspend", body)
		})
}

type flowResumeConfig struct {
	*cli.Command
	As string `cli:"name=as desc='who is lifting it; the configured user when left off'"`
}

func flowResumeCmd(sel *flowFlags) *cli.Command {
	cfg := &flowResumeConfig{}
	return cli.NewCommandAt(&cfg.Command, "resume").
		WithSynopsis("resume --task <id> | --charter <address>[:<rule>] [-r] --subject <address> — " +
			"lift the hold or the suspension the same flags made").
		WithOpts(mustOpts(cfg)...).
		WithRun(func(cc *cli.Context, args []string) error {
			if err := noVerbArgs(cfg.Command, cc, args, "resume"); err != nil {
				return err
			}
			if err := sel.narrowed("resume"); err != nil {
				return err
			}
			if _, err := principal(cfg.As); err != nil {
				return err
			}
			return postFlow(cc, "resume", sel.body())
		})
}

type flowSayConfig struct {
	*cli.Command
	As string `cli:"name=as desc='who is saying it; the configured user when left off'"`
}

func flowSayCmd(sel *flowFlags) *cli.Command {
	cfg := &flowSayConfig{}
	return cli.NewCommandAt(&cfg.Command, "say").
		WithSynopsis("say --task <id> | --charter <address>[:<rule>] | --subject <address> <text…> — " +
			"say an instruction into every live task the flags name").
		WithOpts(mustOpts(cfg)...).
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			if err := sel.narrowed("say"); err != nil {
				return err
			}
			if len(args) == 0 {
				return fmt.Errorf("%w: verse flow say wants the text to say", cli.ErrUsage)
			}
			by, err := principal(cfg.As)
			if err != nil {
				return err
			}
			body := sel.body()
			body.Text, body.By = strings.Join(args, " "), by
			return postFlow(cc, "say", body)
		})
}

type flowCancelConfig struct {
	*cli.Command
	As string `cli:"name=as desc='who is cancelling it; the configured user when left off'"`
}

func flowCancelCmd(sel *flowFlags) *cli.Command {
	cfg := &flowCancelConfig{}
	return cli.NewCommandAt(&cfg.Command, "cancel").
		WithSynopsis("cancel --task <id> — cancel one run (destructive)").
		WithOpts(mustOpts(cfg)...).
		WithRun(func(cc *cli.Context, args []string) error {
			if err := noVerbArgs(cfg.Command, cc, args, "cancel"); err != nil {
				return err
			}
			if err := sel.narrowed("cancel"); err != nil {
				return err
			}
			if _, err := principal(cfg.As); err != nil {
				return err
			}
			return postFlow(cc, "cancel", sel.body())
		})
}

// noVerbArgs parses a verb's flags and refuses a positional: a verb is addressed by flags, and a
// word left over is an address typed where a flag was meant.
func noVerbArgs(cmd *cli.Command, cc *cli.Context, args []string, verb string) error {
	args, err := cmd.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) > 0 {
		return fmt.Errorf("%w: verse flow %s is addressed by flags, not %q: --task, --charter, --subject",
			cli.ErrUsage, verb, strings.Join(args, " "))
	}
	return nil
}

// postFlow sends a verb and prints what it answers.
func postFlow(cc *cli.Context, verb string, body api.FlowSelect) error {
	resp, err := httpPost("/flow/"+verb, body)
	if err != nil {
		return fmt.Errorf("flow %s: %w", verb, err)
	}
	return printBody(cc, resp)
}

type flowShowConfig struct{ *cli.Command }

// flow show renders a subject's work as a shape rather than a list: which trigger acted on
// it, what that spawned, and the completion branches hanging off it — including branches the
// charter declares but nothing has reached yet. Bare `flow` answers "what is running and what is
// held"; this answers "what is happening to this one, and what comes next".
func flowShowCmd() *cli.Command {
	cfg := &flowShowConfig{}
	return cli.NewCommandAt(&cfg.Command, "show").
		WithSynopsis("show a subject's flow as a tree: show <subject|demo.pr.*>").WithRun(cfg.run)
}

func (cfg *flowShowConfig) run(cc *cli.Context, args []string) error {
	subject, err := oneArg(cfg.Command, cc, args, "flow show wants a subject address (e.g. demo.pr.1 or demo.pr.*)")
	if err != nil {
		return err
	}
	resp, err := httpGet("/flow/tree", url.Values{"subject": {subject}})
	if err != nil {
		return fmt.Errorf("flow show: %w", err)
	}
	return printBody(cc, resp)
}
