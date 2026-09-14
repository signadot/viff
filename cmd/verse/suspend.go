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

// suspend and resume: hold the work about a charter, a subject, or a pair, and lift it
// (docs/working/suspend.md). Bare `verse suspend` lists what is held, by whom and why.

type suspendConfig struct {
	*cli.Command
	Why string `cli:"name=why desc='what the suspension is for — required, since nothing ends it but somebody reading this'"`
	As  string `cli:"name=as desc='who holds it; the configured user when left off'"`
}

func suspendCmd() *cli.Command {
	cfg := &suspendConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "suspend").
		WithAliases("suspensions").
		WithSynopsis("suspend — bare lists what is held; suspend [<charter>[:<rule>]] [<subject>] --why <text> holds it").
		WithOpts(append(opts, filterOpts()...)...).
		WithRun(cfg.run)
}

func (cfg *suspendConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		resp, err := httpGet("/suspensions", nil)
		if err != nil {
			return fmt.Errorf("suspend: %w", err)
		}
		return printBody(cc, resp)
	}
	body, err := suspensionTarget(args)
	if err != nil {
		return err
	}
	if cfg.Why == "" {
		return fmt.Errorf("%w: suspend wants --why: a suspension says what it is for", cli.ErrUsage)
	}
	body.Why = cfg.Why
	if _, err := principal(cfg.As); err != nil {
		return err
	}
	resp, err := httpPost("/suspend", body)
	if err != nil {
		return fmt.Errorf("suspend: %w", err)
	}
	return printBody(cc, resp)
}

type resumeConfig struct {
	*cli.Command
	As string `cli:"name=as desc='who is lifting it; the configured user when left off'"`
}

func resumeCmd() *cli.Command {
	cfg := &resumeConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "resume").
		WithSynopsis("resume [<charter>[:<rule>]] [<subject>] — lift the suspension the same words made").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *resumeConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return fmt.Errorf("%w: resume wants what to lift, in the words that suspended it", cli.ErrUsage)
	}
	body, err := suspensionTarget(args)
	if err != nil {
		return err
	}
	if _, err := principal(cfg.As); err != nil {
		return err
	}
	resp, err := httpPost("/resume", body)
	if err != nil {
		return fmt.Errorf("resume: %w", err)
	}
	return printBody(cc, resp)
}

// suspensionTarget reads what a suspend or a resume names.
//
//	<charter>[:<rule>] <subject>   a pair; `*` for every charter, `:<rule>` for baseline's rule
//	<charter>:[<rule>]             a charter's rules, about every subject
//	<address>                      the charter installed there, about every subject — or, when no
//	                               charter is installed there, every charter about that subject
//
// Baseline's charter has no address, so it is never spelled: `:claim` is its rule.
func suspensionTarget(args []string) (api.Suspend, error) {
	switch len(args) {
	case 2:
		charter, rule := splitCharter(args[0])
		return api.Suspend{Charter: charter, Rule: rule, Subject: args[1]}, nil
	case 1:
		if strings.Contains(args[0], ":") {
			charter, rule := splitCharter(args[0])
			return api.Suspend{Charter: charter, Rule: rule}, nil
		}
		charter, err := isCharter(args[0])
		if err != nil {
			return api.Suspend{}, err
		}
		if charter {
			return api.Suspend{Charter: args[0]}, nil
		}
		return api.Suspend{Charter: "*", Subject: args[0]}, nil
	}
	return api.Suspend{}, fmt.Errorf("%w: [<charter>[:<rule>]] [<subject>] — at most two words", cli.ErrUsage)
}

// splitCharter splits `<charter>:<rule>` on its last colon, since a rule's name holds none.
func splitCharter(s string) (charter, rule string) {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// isCharter asks whether a charter is installed at an address: its node in the charter tree
// says what the charter there writes, and a place with no charter says nothing of the kind.
//
// BASELINE IS ASKED, whatever --scope says. A charter at an address is a package, and packages
// are baseline's; a scope's charter has no address, and the scoped route answers with it
// whatever address is asked. So from a scope, a package's address still reads as its charter,
// and the route refuses it as baseline's rather than suspending a subject by that name.
func isCharter(at string) (bool, error) {
	if strings.HasSuffix(at, ".*") || at == "*" {
		return false, nil
	}
	scope := g.Scope
	g.Scope = ""
	resp, err := httpGet("/charter", url.Values{"at": {at}, "depth": {"0"}})
	g.Scope = scope
	if err != nil {
		return false, fmt.Errorf("suspend: asking whether %s is a charter: %w", at, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, statusError(resp, "suspend: asking whether "+at+" is a charter")
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	node, err := codec.ParseNode(raw, respFmt(resp))
	if err != nil {
		return false, err
	}
	return entity.Get(node, "writes") != nil, nil
}
