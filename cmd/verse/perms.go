package main

import (
	"fmt"
	"net/url"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// permsConfig is `verse perms`: a node's permissions, read and set by its owner.
type permsConfig struct {
	*cli.Command
	ChildGrants string `cli:"name=child-grants desc='what a node created beneath this one holds out, as grantees by level: {read: [\"*\"], read-write: [user:*]}; {} for nothing; null to inherit again'"`
	Grants      string `cli:"name=grants desc='what this node holds out, replacing the list: [{read: […]}] is about the node, {below: true, …} what is beneath it, {child-owner: […]} who may create beneath it and own what they create, {at: …, …} another path; [] for nothing'"`
	Grant       string `cli:"name=grant desc='one line to add at this node, as grantees by level: {read: [ana@example.com]}'"`
	Below       bool   `cli:"name=below desc='the --grant covers what is beneath the node, not the node itself'"`
	Revoke      string `cli:"name=revoke desc='grantees to take off this node, by level: {read: [ana@example.com]}; every other line stands'"`
	// Root names the ROOT, whose address is the empty path and so cannot be given as steps —
	// no words at all is the listing. It is the deployment's own top: who may take a place of
	// their own, and what every place holds out until a record beneath says otherwise.
	Root bool `cli:"name=root desc='the ROOT itself, whose address is the empty path: what the top holds out, and who may take a place'"`
}

func permsCmd() *cli.Command {
	cfg := &permsConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "perms").
		WithSynopsis("perms — permissions: bare lists the records of every node you may read; perms <path…> reads one, and sets it as its owner with [--child-grants '{…}'] [--grants '[…]'] [--grant '{…}' [--below]] [--revoke '{…}'] — a grantee is user:<email> (or the bare email), agent:<name>, runner:<identity>, via:<address>, <kind>:*, group:<name>, or *").
		// --if/--trim beside its own, as every listing carries them (filteredCollection).
		WithOpts(append(opts, filterOpts()...)...).
		WithRun(cfg.run)
}

// run lists with no path, reads the record with a path and no flags, and sets it with any. The
// path is given as words, the way `verse data` takes one, so a segment holding a dot needs no
// quoting from the shell.
func (cfg *permsConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if cfg.Root && len(args) > 0 {
		return fmt.Errorf("%w: perms --root is the root itself; it takes no path", cli.ErrUsage)
	}
	if len(args) == 0 && !cfg.Root {
		if cfg.ChildGrants != "" || cfg.Grants != "" || cfg.Grant != "" || cfg.Revoke != "" {
			return fmt.Errorf("%w: setting a record wants the path of the node it is about: perms <path…> [--child-grants …], or --root for the top", cli.ErrUsage)
		}
		resp, err := httpGet("/perms/records", nil)
		if err != nil {
			return fmt.Errorf("perms: %w", err)
		}
		return printBody(cc, resp)
	}
	if len(args) > 0 && args[0] == "list" {
		return fmt.Errorf("%w: there is no `verse perms list` — bare `verse perms` is the listing", cli.ErrUsage)
	}
	// STEPS, as `verse data` reads them: the quoted clause is a name, and an access or a `*` is
	// refused, since a record is about one node. The root is no steps at all, which is why it
	// has a flag: on this command line, no words is the listing.
	at := ""
	if !cfg.Root {
		names, err := entity.StepNames(args)
		if err != nil {
			return fmt.Errorf("%w: perms: %s", cli.ErrUsage, err)
		}
		at = entity.JoinSegs(names)
	}
	if cfg.ChildGrants == "" && cfg.Grants == "" && cfg.Grant == "" && cfg.Revoke == "" {
		resp, err := httpGet("/perms", url.Values{"at": {at}})
		if err != nil {
			return fmt.Errorf("perms: %w", err)
		}
		return printBody(cc, resp)
	}
	edit := api.PermsEdit{At: at, Below: cfg.Below}
	for _, f := range []struct {
		flag, src string
		into      *entity.Payload
	}{{"--child-grants", cfg.ChildGrants, &edit.ChildGrants}, {"--grants", cfg.Grants, &edit.Grants}, {"--grant", cfg.Grant, &edit.Grant}, {"--revoke", cfg.Revoke, &edit.Revoke}} {
		if f.src == "" {
			continue
		}
		n, err := codec.ParseNode([]byte(f.src), codec.Tony)
		if err != nil {
			return fmt.Errorf("perms %s: %w", f.flag, err)
		}
		*f.into = n
	}
	resp, err := httpPut("/perms", edit)
	if err != nil {
		return fmt.Errorf("perms: %w", err)
	}
	return printBody(cc, resp)
}
