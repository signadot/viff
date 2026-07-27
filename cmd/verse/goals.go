package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/scott-cotton/cli"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
)

// The group is `goal`, singular, and `goals` is the listing — the one place the plural rule
// cost a rename rather than just adding a command. It was `goals` with a `goal` alias, so the
// plural named the group AND the listing under it (`goals list`), which is precisely the
// ambiguity the rule removes everywhere else.
func goalCmd() *cli.Command {
	return cli.NewCommand("goal").
		WithSynopsis("goal — one owning aggregate: owned triggers + done/fail latch + status (list them with `verse goals`)").
		WithSubs(
			goalsAddCmd(),
			goalsRemoveCmd(),
			goalsPauseCmd(),
			goalsResumeCmd(),
			goalsCancelCmd(),
			goalsSayCmd(),
		)
}

func goalsCmd() *cli.Command {
	return collection("goals", "/goals",
		"goals — list goals with status / waitingOn / outcome (act on one with `verse goal`)", "goal")
}

type goalsAddConfig struct {
	*cli.Command
	File    string `cli:"name=file aliases=f desc='read a full GoalDef (incl. owned triggers) from FILE (- = stdin); format follows the suffix'"`
	Subject string `cli:"name=subject desc='the subject entity this goal targets (steering key)'"`
	// The old desc spelled the formula as JSON, which is the one dialect that cannot write
	// the tagged form the engine actually takes — `!fact` has no JSON spelling, so the
	// example taught a shape the interesting formulas cannot use.
	DoneWhen string `cli:"name=done-when desc='success predicate as a formula, e.g. !fact ci:status or !until [ !quorum 2, !deadline 4h ]'"`
	Deadline string `cli:"name=deadline desc='fail-latch: elapsed with doneWhen unmet ⇒ unachievable (e.g. 4h)'"`
}

func goalsAddCmd() *cli.Command {
	cfg := &goalsAddConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "add").
		WithSynopsis("add a goal: add --file <f> | add <name> --done-when <formula> [--subject <ref>] [--deadline <dur>]").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *goalsAddConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}

	// Full-record mode: --file FILE (or "-" for stdin) carries a complete GoalDef, including
	// owned triggers. Inline mode: build a GoalDef from a name positional + flags (no owned
	// triggers — install those via `verse trigger add` or the file form).
	var node *ir.Node
	if cfg.File != "" {
		// The file's suffix names its dialect; stdin uses --ifmt or the configured default.
		data, srcFmt, err := codec.ReadSource(cc.In, cfg.File, inFmt())
		if err != nil {
			return fmt.Errorf("goal add: %w", err)
		}
		n, err := codec.ParseNode(data, srcFmt)
		if err != nil {
			return fmt.Errorf("goal add: %w", err)
		}
		node = n
	} else {
		name, err := oneArg(cfg.Command, cc, args, "goal add wants a name (or --file <f>)")
		if err != nil {
			return err
		}
		if cfg.DoneWhen == "" {
			return fmt.Errorf("%w: goal add wants --done-when <formula> (or a --file <f>)", cli.ErrUsage)
		}
		// --done-when is a formula typed on the command line, so it names no format of its
		// own: it is read as --ifmt / the configured default. It stays a DOCUMENT from here
		// to the server — which is what makes
		// `--done-when '!until [ !quorum 2, !deadline "4h" ]'` work, tags and all.
		doneWhen, err := codec.ParseNode([]byte(cfg.DoneWhen), inFmt())
		if err != nil {
			return fmt.Errorf("goal add: --done-when: %w", err)
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
		return fmt.Errorf("goal add: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("goal add: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
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
	name, err := oneArg(cfg.Command, cc, args, "goal remove wants a name")
	if err != nil {
		return err
	}
	resp, err := httpDelete("/goals/"+name, nil)
	if err != nil {
		return fmt.Errorf("goal remove: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("goal remove: no such goal %q", name)
	}
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("goal remove: %s", resp.Status)
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
			gname, err := oneArg(cfg.Command, cc, args, "goal "+name+" wants a goal name")
			if err != nil {
				return err
			}
			resp, err := httpPost(path, api.GoalSteer{Name: gname})
			if err != nil {
				return fmt.Errorf("goal %s: %w", name, err)
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
		return fmt.Errorf("%w: goal say wants a name and text", cli.ErrUsage)
	}
	resp, err := httpPost("/goal/say", api.GoalSteer{Name: args[0], Text: strings.Join(args[1:], " ")})
	if err != nil {
		return fmt.Errorf("goal say: %w", err)
	}
	return printBody(cc, resp)
}
