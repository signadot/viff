package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/scott-cotton/cli"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
)

func goalsCmd() *cli.Command {
	return cli.NewCommand("goals").WithAliases("goal").
		WithSynopsis("goals — owning aggregates (owned triggers + done/fail latch + status)").
		WithSubs(
			goalsListCmd(),
			goalsAddCmd(),
			goalsRemoveCmd(),
			goalsPauseCmd(),
			goalsResumeCmd(),
			goalsCancelCmd(),
			goalsSayCmd(),
		)
}

func goalsListCmd() *cli.Command {
	return leaf("list", "list goals with status / waitingOn / outcome",
		func(cc *cli.Context, args []string) error {
			resp, err := httpGet("/goals", nil)
			if err != nil {
				return fmt.Errorf("goals list: %w", err)
			}
			return printBody(cc, resp)
		})
}

type goalsAddConfig struct {
	*cli.Command
	File     string `cli:"name=f desc='read a full GoalDef (JSON, incl. owned triggers) from FILE (- = stdin)'"`
	Subject  string `cli:"name=subject desc='the subject entity this goal targets (steering key)'"`
	DoneWhen string `cli:"name=done-when desc='success predicate as a formula, e.g. {\"fact\":\"ci:status\"}'"`
	Deadline string `cli:"name=deadline desc='fail-latch: elapsed with doneWhen unmet ⇒ unachievable (e.g. 4h)'"`
}

func goalsAddCmd() *cli.Command {
	cfg := &goalsAddConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "add").
		WithSynopsis("add a goal: add -f <file> | add <name> --done-when <formula> [--subject <ref>] [--deadline <dur>]").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *goalsAddConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}

	// Full-record mode: -f FILE (or "-" for stdin) carries a complete GoalDef, including
	// owned triggers. Inline mode: build a GoalDef from a name positional + flags (no owned
	// triggers — install those via `verse trigger add` or the file form).
	var node *ir.Node
	if cfg.File != "" {
		// The file's suffix names its dialect; stdin uses --ifmt or the configured default.
		data, srcFmt, err := codec.ReadSource(cc.In, cfg.File, inFmt())
		if err != nil {
			return fmt.Errorf("goals add: %w", err)
		}
		n, err := codec.ParseNode(data, srcFmt)
		if err != nil {
			return fmt.Errorf("goals add: %w", err)
		}
		node = n
	} else {
		name, err := oneArg(cfg.Command, cc, args, "goals add wants a name (or -f <file>)")
		if err != nil {
			return err
		}
		if cfg.DoneWhen == "" {
			return fmt.Errorf("%w: goals add wants --done-when <formula> (or a -f <file>)", cli.ErrUsage)
		}
		// --done-when is a formula typed on the command line, so it names no format of its
		// own: it is read as --ifmt / the configured default. It stays a DOCUMENT from here
		// to the server — which is what makes
		// `--done-when '!until [ !quorum 2, !deadline "4h" ]'` work, tags and all.
		doneWhen, err := codec.ParseNode([]byte(cfg.DoneWhen), inFmt())
		if err != nil {
			return fmt.Errorf("goals add: --done-when: %w", err)
		}
		kvs := []ir.KeyVal{
			{Key: ir.FromString("name"), Val: ir.FromString(name)},
			{Key: ir.FromString("doneWhen"), Val: doneWhen},
		}
		if cfg.Subject != "" {
			kvs = append(kvs, ir.KeyVal{Key: ir.FromString("subject"), Val: ir.FromString(cfg.Subject)})
		}
		if cfg.Deadline != "" {
			kvs = append(kvs, ir.KeyVal{Key: ir.FromString("deadline"), Val: ir.FromString(cfg.Deadline)})
		}
		node = ir.FromKeyVals(kvs)
	}

	resp, err := httpPostNode("/goals", node)
	if err != nil {
		return fmt.Errorf("goals add: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("goals add: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	fmt.Fprintln(cc.Out, "installed goal")
	return nil
}

type goalsRemoveConfig struct{ *cli.Command }

func goalsRemoveCmd() *cli.Command {
	cfg := &goalsRemoveConfig{}
	return cli.NewCommandAt(&cfg.Command, "remove").
		WithSynopsis("remove a goal (stops pursuit; latched terminals stay): remove <name>").WithRun(cfg.run)
}

func (cfg *goalsRemoveConfig) run(cc *cli.Context, args []string) error {
	name, err := oneArg(cfg.Command, cc, args, "goals remove wants a name")
	if err != nil {
		return err
	}
	resp, err := httpDelete("/goals/"+name, nil)
	if err != nil {
		return fmt.Errorf("goals remove: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("goals remove: no such goal %q", name)
	}
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("goals remove: %s", resp.Status)
	}
	fmt.Fprintln(cc.Out, "removed", name)
	return nil
}

// goalSteerCmd builds a `pause|resume|cancel <name>` subcommand that delegates to the goal's
// flow (a goal is a flow with a destination).
func goalSteerCmd(name, synopsis, path string) *cli.Command {
	cfg := &struct{ *cli.Command }{}
	return cli.NewCommandAt(&cfg.Command, name).
		WithSynopsis(synopsis).
		WithRun(func(cc *cli.Context, args []string) error {
			gname, err := oneArg(cfg.Command, cc, args, "goals "+name+" wants a goal name")
			if err != nil {
				return err
			}
			resp, err := httpPost(path, map[string]any{"name": gname})
			if err != nil {
				return fmt.Errorf("goals %s: %w", name, err)
			}
			return printBody(cc, resp)
		})
}

func goalsPauseCmd() *cli.Command {
	return goalSteerCmd("pause", "pause a goal's in-flight work: pause <name>", "/goal/pause")
}
func goalsResumeCmd() *cli.Command {
	return goalSteerCmd("resume", "resume a paused goal: resume <name>", "/goal/resume")
}
func goalsCancelCmd() *cli.Command {
	return goalSteerCmd("cancel", "cancel a goal's in-flight work: cancel <name>", "/goal/cancel")
}

type goalsSayConfig struct{ *cli.Command }

func goalsSayCmd() *cli.Command {
	cfg := &goalsSayConfig{}
	return cli.NewCommandAt(&cfg.Command, "say").
		WithSynopsis("say something to a goal's in-flight processes: say <name> <text…>").WithRun(cfg.run)
}

func (cfg *goalsSayConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("%w: goals say wants a name and text", cli.ErrUsage)
	}
	resp, err := httpPost("/goal/say", map[string]any{"name": args[0], "text": strings.Join(args[1:], " ")})
	if err != nil {
		return fmt.Errorf("goals say: %w", err)
	}
	return printBody(cc, resp)
}
