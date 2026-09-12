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
	Default string `cli:"name=default desc='what a node created beneath this one holds out, as grantees by level: {read: [\"*\"]}; {} for nothing; null to inherit again'"`
	Grants  string `cli:"name=grants desc='what this node itself holds out, replacing the list: [{at: …, read: […]}]; [] for nothing'"`
	Grant   string `cli:"name=grant desc='one line to add at this node, as grantees by level: {read: [ana@example.com]}'"`
	Below   bool   `cli:"name=below desc='the --grant covers what is beneath the node, not the node itself'"`
	Revoke  string `cli:"name=revoke desc='grantees to take off this node, by level: {read: [ana@example.com]}; every other line stands'"`
}

func permsCmd() *cli.Command {
	cfg := &permsConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "perms").
		WithSynopsis("perms — a node's permissions, as its owner: perms <path…> [--default '{…}'] [--grants '[…]'] [--grant '{…}' [--below]] [--revoke '{…}']").
		WithOpts(opts...).
		WithRun(cfg.run)
}

// run reads the record with no flags, and sets it with any. The path is given as words, the
// way `verse data` takes one, so a segment holding a dot needs no quoting from the shell.
func (cfg *permsConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return fmt.Errorf("perms wants a path: perms <path…>")
	}
	at := entity.JoinSegs(args)
	if cfg.Default == "" && cfg.Grants == "" && cfg.Grant == "" && cfg.Revoke == "" {
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
	}{{"--default", cfg.Default, &edit.Default}, {"--grants", cfg.Grants, &edit.Grants}, {"--grant", cfg.Grant, &edit.Grant}, {"--revoke", cfg.Revoke, &edit.Revoke}} {
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
