package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/client"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
)

// verse flow: the work in flight and the work held from now on, through one set of flags. Bare,
// it lists live tasks and suspensions; its verbs act on what the same flags name — one run with
// --task, or a charter and a subject with --charter and --subject, the one left out being '*'. A
// verb that names nothing prints its usage and does nothing.

// flowFlags is the selection, declared once on `flow` and read by its verbs: the options are
// resolved along the command's path, so `verse flow suspend --task x` sets them here.
type flowFlags struct {
	Task      string `cli:"name=task desc='one run, by its task id — combines with neither --charter nor --subject'"`
	Charter   string `cli:"name=charter desc='<address>[:<rule>]: an installed charter and one rule of it; :<rule> names a baseline rule, * every charter'"`
	Subject   string `cli:"name=subject desc='an address, with * open in any segment; * alone is every subject'"`
	Recursive bool   `cli:"name=recursive aliases=r desc='the charter at --charter and every charter beneath it'"`
	Args      bool   `cli:"name=args desc='print each row as the flags that name it, one per line, for xargs -L1 verse flow <verb>'"`
}

const flowSynopsis = "flow [--task <id> | [--charter <address>[:<rule>] [-r]] [--subject <address>]] [--args] — " +
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
// back: `-task <id>` for a live task, `-charter <c> [-recursive] -subject <s>` for a
// suspension, and the scope the listing was asked in.
func rowArgs(row api.FlowRow, scope string) string {
	var words []string
	flag := func(name, value string) { words = append(words, "-"+name, entity.ShellWord(value)) }
	switch row.Kind {
	case api.FlowRowTask:
		flag("task", row.Task)
	default:
		flag("charter", row.Charter)
		if row.Recursive {
			words = append(words, "-recursive")
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
	if sel.Args {
		return fmt.Errorf("%w: verse flow %s: --args prints the listing's rows; a verb takes the "+
			"flags one row printed", cli.ErrUsage, verb)
	}
	if want := sel.unnarrowed(verb, func(name string) string { return "--" + name }); want != "" {
		return fmt.Errorf("%w: verse flow %s: %s", cli.ErrUsage, verb, want)
	}
	return nil
}

// unnarrowed is what a verb's selection lacks to say what it acts on, or "" when it says: the
// command's usage and the tool's refusal, with name spelling a selection's part as each reader
// gives it.
func (sel *flowFlags) unnarrowed(verb string, name func(string) string) string {
	task, charter, subject := name("task"), name("charter"), name("subject")
	if sel.Task != "" && (sel.Charter != "" || sel.Subject != "" || sel.Recursive) {
		return fmt.Sprintf("%s names one run, and combines with neither %s nor %s", task, charter, subject)
	}
	switch verb {
	case "suspend", "resume":
		if sel.Task == "" && sel.Charter == "" && sel.Subject == "" {
			return fmt.Sprintf("%s <id>, or %s <address>[:<rule>], %s <address> or both; "+
				"the one left out is '*'", task, charter, subject)
		}
	case "say":
		if sel.Task == "" && sel.Charter == "" && sel.Subject == "" {
			return fmt.Sprintf("%s, %s or %s: what to say it to", task, charter, subject)
		}
	case "cancel":
		if sel.Task == "" {
			return fmt.Sprintf("%s <id>: cancel names one run; suspend a charter or a subject, then "+
				"cancel its runs one by one", task)
		}
	}
	return ""
}

type flowSuspendConfig struct {
	*cli.Command
	Why string `cli:"name=why desc='what a suspension is for — required, since nothing ends it but somebody reading this'"`
	As  string `cli:"name=as desc='who holds it; the configured user when left off'"`
}

func flowSuspendCmd(sel *flowFlags) *cli.Command {
	cfg := &flowSuspendConfig{}
	return cli.NewCommandAt(&cfg.Command, "suspend").
		WithSynopsis("suspend --task <id> | [--charter <address>[:<rule>] [-r]] [--subject <address>] --why <text> — " +
			"hold one run at its next boundary, or suspend a charter's work about a subject until resumed; " +
			"the one of --charter and --subject left out is '*'").
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
		WithSynopsis("resume --task <id> | [--charter <address>[:<rule>] [-r]] [--subject <address>] — " +
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
	resp, err := postFlowVia(apiClient(), verb, body)
	if err != nil {
		return err
	}
	return printBody(cc, resp)
}

// postFlowVia sends a verb through c, answering the response for the caller to render.
func postFlowVia(c *client.Client, verb string, body api.FlowSelect) (*http.Response, error) {
	resp, err := c.Post("/flow/"+verb, nil, body)
	if err != nil {
		return nil, fmt.Errorf("flow %s: %w", verb, err)
	}
	return resp, nil
}

type flowShowConfig struct {
	*cli.Command
	Active bool `cli:"name=active desc='only subjects with a live task; --no-active only those without one; neither, every subject'"`
}

// flow show renders a subject's work as a shape rather than a list: which trigger acted on
// it, what that spawned, and the completion branches hanging off it — including branches the
// charter declares but nothing has reached yet — and, at the top, what suspends it. A subject
// with no work that a suspension would hold is shown too, with no branches. Bare `flow` answers
// "what is running and what is held"; this answers "what is happening to this one, and what
// comes next".
func flowShowCmd() *cli.Command {
	cfg := &flowShowConfig{}
	opts := mustOpts(cfg)
	return cli.NewCommandAt(&cfg.Command, "show").
		WithSynopsis("show a subject's flow as a tree, and what suspends it: show <subject|demo.pr.*> [--active | --no-active]").
		WithOpts(opts...).
		WithRun(func(cc *cli.Context, args []string) error {
			subject, err := oneArg(cfg.Command, cc, args, "flow show wants a subject address (e.g. demo.pr.1 or demo.pr.*)")
			if err != nil {
				return err
			}
			q := url.Values{"subject": {subject}}
			// A bool the command line did not mention has no value, which is how --no-active is
			// told apart from neither.
			for _, o := range opts {
				if o.Name == "active" && o.Value != nil {
					q.Set("active", fmt.Sprint(cfg.Active))
				}
			}
			resp, err := httpGet("/flow/tree", q)
			if err != nil {
				return fmt.Errorf("flow show: %w", err)
			}
			return printBody(cc, resp)
		})
}
