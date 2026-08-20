package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/scott-cotton/cli"
	"github.com/signadot/verse/api"
)

func flowCmd() *cli.Command {
	return collection("flow", "flows", "/flows",
		"flow — the distinct subjects with in-flight tasks; steer one by the subject it is about",
		flowShowCmd(),
		flowPauseCmd(),
		flowResumeCmd(),
		flowCancelCmd(),
		flowSayCmd(),
	)
}

type flowShowConfig struct{ *cli.Command }

// flow show renders a subject's work as a shape rather than a list: which trigger acted on
// it, what that spawned, and the completion branches hanging off it — including branches the
// charter declares but nothing has reached yet. `flow list` answers "which subjects are
// busy"; this answers "what is happening to this one, and what comes next".
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

type flowPauseConfig struct{ *cli.Command }

func flowPauseCmd() *cli.Command {
	cfg := &flowPauseConfig{}
	return cli.NewCommandAt(&cfg.Command, "pause").
		WithSynopsis("pause the addressed tasks at their next boundary: pause <subject|demo.pr.*>").WithRun(cfg.run)
}

func (cfg *flowPauseConfig) run(cc *cli.Context, args []string) error {
	subject, err := oneArg(cfg.Command, cc, args, "flow pause wants a subject address (e.g. demo.pr.1 or demo.pr.*)")
	if err != nil {
		return err
	}
	resp, err := httpPost("/flow/pause", api.FlowSteer{Subject: subject})
	if err != nil {
		return fmt.Errorf("flow pause: %w", err)
	}
	return printBody(cc, resp)
}

type flowResumeConfig struct{ *cli.Command }

func flowResumeCmd() *cli.Command {
	cfg := &flowResumeConfig{}
	return cli.NewCommandAt(&cfg.Command, "resume").
		WithSynopsis("resume held tasks: resume <subject|demo.pr.*>").WithRun(cfg.run)
}

func (cfg *flowResumeConfig) run(cc *cli.Context, args []string) error {
	subject, err := oneArg(cfg.Command, cc, args, "flow resume wants a subject address (e.g. demo.pr.1 or demo.pr.*)")
	if err != nil {
		return err
	}
	resp, err := httpPost("/flow/resume", api.FlowSteer{Subject: subject})
	if err != nil {
		return fmt.Errorf("flow resume: %w", err)
	}
	return printBody(cc, resp)
}

type flowCancelConfig struct{ *cli.Command }

func flowCancelCmd() *cli.Command {
	cfg := &flowCancelConfig{}
	return cli.NewCommandAt(&cfg.Command, "cancel").
		WithSynopsis("cancel the addressed tasks (destructive): cancel <subject|demo.pr.*>").WithRun(cfg.run)
}

func (cfg *flowCancelConfig) run(cc *cli.Context, args []string) error {
	subject, err := oneArg(cfg.Command, cc, args, "flow cancel wants a subject address (e.g. demo.pr.1 or demo.pr.*)")
	if err != nil {
		return err
	}
	resp, err := httpPost("/flow/cancel", api.FlowSteer{Subject: subject})
	if err != nil {
		return fmt.Errorf("flow cancel: %w", err)
	}
	return printBody(cc, resp)
}

type flowSayConfig struct{ *cli.Command }

func flowSayCmd() *cli.Command {
	cfg := &flowSayConfig{}
	return cli.NewCommandAt(&cfg.Command, "say").
		WithSynopsis("say an instruction into the addressed tasks: say <subject|demo.pr.*> <text…>").WithRun(cfg.run)
}

func (cfg *flowSayConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("%w: flow say wants a subject and text", cli.ErrUsage)
	}
	resp, err := httpPost("/flow/say", api.FlowSteer{Subject: args[0], Text: strings.Join(args[1:], " ")})
	if err != nil {
		return fmt.Errorf("flow say: %w", err)
	}
	return printBody(cc, resp)
}
