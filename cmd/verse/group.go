package main

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/pkg/api"
)

// groupCmd is `verse group`: a named set of principals, the grantee `group:<name>` admitting its
// members. Bare lists them; add writes one (or replaces its members), show reads one, remove
// takes one away.
func groupCmd() *cli.Command {
	return filteredCollection("group", "groups", "/groups",
		"group — a named set of principals, granted as group:<name>; add|show|remove one",
		nil, func() url.Values { return nil }, groupAddCmd(), groupShowCmd(), groupRemoveCmd())
}

type groupAddConfig struct{ *cli.Command }

func groupAddCmd() *cli.Command {
	cfg := &groupAddConfig{}
	return cli.NewCommandAt(&cfg.Command, "add").
		WithSynopsis("add a group, or replace its members: add <name> <member>… — a member is user:<email> (or the bare email), agent:<name>, runner:<identity>, via:<address>, <kind>:*, group:<name>, or *").
		WithRun(cfg.run)
}

func (cfg *groupAddConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("%w: group add wants a name and its members", cli.ErrUsage)
	}
	resp, err := httpPost("/groups", api.Group{Name: args[0], Members: args[1:]})
	if err != nil {
		return fmt.Errorf("group add: %w", err)
	}
	return printBody(cc, resp)
}

type groupShowConfig struct{ *cli.Command }

func groupShowCmd() *cli.Command {
	cfg := &groupShowConfig{}
	return cli.NewCommandAt(&cfg.Command, "show").WithSynopsis("show a group: show <name>").WithRun(cfg.run)
}

func (cfg *groupShowConfig) run(cc *cli.Context, args []string) error {
	name, err := oneArg(cfg.Command, cc, args, "group show wants a name")
	if err != nil {
		return err
	}
	resp, err := httpGet("/groups/"+url.PathEscape(name), nil)
	if err != nil {
		return fmt.Errorf("group show: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return fmt.Errorf("group show: no such group %q", name)
	}
	return printBody(cc, resp)
}

type groupRemoveConfig struct{ *cli.Command }

func groupRemoveCmd() *cli.Command {
	cfg := &groupRemoveConfig{}
	return cli.NewCommandAt(&cfg.Command, "remove").WithSynopsis("remove a group: remove <name>").WithRun(cfg.run)
}

func (cfg *groupRemoveConfig) run(cc *cli.Context, args []string) error {
	name, err := oneArg(cfg.Command, cc, args, "group remove wants a name")
	if err != nil {
		return err
	}
	resp, err := httpDelete("/groups/"+url.PathEscape(name), nil)
	if err != nil {
		return fmt.Errorf("group remove: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return statusError(resp, "group remove")
	}
	fmt.Fprintf(cc.Out, "removed group %s\n", name)
	return nil
}
