package main

import (
	"fmt"
	"strings"

	"github.com/scott-cotton/cli"
)

func flowCmd() *cli.Command {
	return cli.NewCommand("flow").
		WithSynopsis("flow — engine steering, per subject (control plane)").
		WithSubs(
			flowListCmd(),
			flowPauseCmd(),
			flowResumeCmd(),
			flowCancelCmd(),
			flowSayCmd(),
		)
}

func flowListCmd() *cli.Command {
	return leaf("list", "list in-flight flows",
		func(cc *cli.Context, args []string) error {
			resp, err := httpGet("/flows", nil)
			if err != nil {
				return fmt.Errorf("flow list: %w", err)
			}
			return printBody(cc, resp)
		})
}

type flowPauseConfig struct{ *cli.Command }

func flowPauseCmd() *cli.Command {
	cfg := &flowPauseConfig{}
	return cli.NewCommandAt(&cfg.Command, "pause").
		WithSynopsis("pause a flow's processes at their next boundary: pause <subject>").WithRun(cfg.run)
}

func (cfg *flowPauseConfig) run(cc *cli.Context, args []string) error {
	subject, err := oneArg(cfg.Command, cc, args, "flow pause wants a subject (entity ref)")
	if err != nil {
		return err
	}
	resp, err := httpPost("/flow/pause", map[string]any{"subject": subject})
	if err != nil {
		return fmt.Errorf("flow pause: %w", err)
	}
	return printBody(cc, resp)
}

type flowResumeConfig struct{ *cli.Command }

func flowResumeCmd() *cli.Command {
	cfg := &flowResumeConfig{}
	return cli.NewCommandAt(&cfg.Command, "resume").
		WithSynopsis("resume a paused flow's processes: resume <subject>").WithRun(cfg.run)
}

func (cfg *flowResumeConfig) run(cc *cli.Context, args []string) error {
	subject, err := oneArg(cfg.Command, cc, args, "flow resume wants a subject (entity ref)")
	if err != nil {
		return err
	}
	resp, err := httpPost("/flow/resume", map[string]any{"subject": subject})
	if err != nil {
		return fmt.Errorf("flow resume: %w", err)
	}
	return printBody(cc, resp)
}

type flowCancelConfig struct{ *cli.Command }

func flowCancelCmd() *cli.Command {
	cfg := &flowCancelConfig{}
	return cli.NewCommandAt(&cfg.Command, "cancel").
		WithSynopsis("cancel a flow's processes: cancel <subject>").WithRun(cfg.run)
}

func (cfg *flowCancelConfig) run(cc *cli.Context, args []string) error {
	subject, err := oneArg(cfg.Command, cc, args, "flow cancel wants a subject (entity ref)")
	if err != nil {
		return err
	}
	resp, err := httpPost("/flow/cancel", map[string]any{"subject": subject})
	if err != nil {
		return fmt.Errorf("flow cancel: %w", err)
	}
	return printBody(cc, resp)
}

type flowSayConfig struct{ *cli.Command }

func flowSayCmd() *cli.Command {
	cfg := &flowSayConfig{}
	return cli.NewCommandAt(&cfg.Command, "say").
		WithSynopsis("say something to a flow's processes: say <subject> <text…>").WithRun(cfg.run)
}

func (cfg *flowSayConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("%w: flow say wants a subject and text", cli.ErrUsage)
	}
	resp, err := httpPost("/flow/say", map[string]any{
		"subject": args[0], "text": strings.Join(args[1:], " "),
	})
	if err != nil {
		return fmt.Errorf("flow say: %w", err)
	}
	return printBody(cc, resp)
}
