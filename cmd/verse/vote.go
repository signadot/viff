package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
)

// voteCmd casts a ballot into a motion's vote log — the participant side of the quorum
// surface. A vote is a plain, visible entity (vote:<motion>:<who>): open, cooperative
// voting, not enforced secrecy. Storing each ballot as its own record keeps the ordered
// vote log, so richer disciplines (ranked, timed-ranked) can read it later; secret
// ballots via COW scopes are a later, deliberately soft option.
type voteConfig struct {
	*cli.Command
	As string `cli:"name=as desc='voter identity (required) — a ballot names its voter'"`
}

func voteCmd() *cli.Command {
	cfg := &voteConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "vote").
		WithSynopsis("vote <motion> <choice> --as <who> — cast a ballot (approve|reject, or a select-option)").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *voteConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("%w: vote wants a motion id and a choice (approve|reject) — see `verse docket`", cli.ErrUsage)
	}
	// choice is approve|reject for a quorum motion, or an option id for a select
	// motion — the vote log is the same, the discipline that reads it differs.
	motion, choice := args[0], args[1]
	if choice == "" {
		return fmt.Errorf("vote: a choice is required (approve|reject, or an option for a select motion)")
	}
	if cfg.As == "" {
		return fmt.Errorf("vote: --as <who> is required (a ballot names its voter)")
	}
	ref := "vote:" + motion + ":" + cfg.As
	resp, err := httpPost("/entity", api.PutEntity{
		Ref:     ref,
		Payload: map[string]any{"choice": choice, "by": cfg.As},
	})
	if err != nil {
		return fmt.Errorf("vote: %w", err)
	}
	resp.Body.Close()
	fmt.Fprintf(cc.Out, "%s voted %s on motion %s\n", cfg.As, choice, motion)
	return nil
}

// providersCmd lists the provider registry — the attribute-rich doers a select gate draws
// from (provider:<capability>:<id>). Providers are registered as entities (e.g.
// `verse entity put provider:review:claude '{"driver":"llm","tier":"premium"}'`).
func providersCmd() *cli.Command {
	return leaf("providers", "providers — list the provider registry (provider:<capability>:<id>)",
		func(cc *cli.Context, args []string) error {
			resp, err := httpGet("/entities", url.Values{"system": {"provider"}})
			if err != nil {
				return fmt.Errorf("providers: %w", err)
			}
			var ents []struct {
				Ref     string         `tony:"field=ref"`
				Payload map[string]any `tony:"field=payload"`
			}
			decodeClose(resp, &ents)
			if len(ents) == 0 {
				fmt.Fprintln(cc.Out, "no providers")
				return nil
			}
			for _, e := range ents {
				capability, id := "", strings.TrimPrefix(e.Ref, "provider:")
				if parts := strings.SplitN(id, ":", 2); len(parts) == 2 {
					capability, id = parts[0], parts[1]
				}
				line := fmt.Sprintf("%-10s %-14s driver=%v", capability, id, e.Payload["driver"])
				for k, v := range e.Payload {
					if k != "driver" {
						line += fmt.Sprintf(" %s=%v", k, v)
					}
				}
				fmt.Fprintln(cc.Out, line)
			}
			return nil
		})
}

// docketCmd lists the docket — the open motions, what each needs, and how tallies stand. Each
// is written by the engine when a governed motion opens (motion:channel:<id>), across both
// axes: a whether-gate (options proceed/deny) and a who-select (options = the eligible doers).
func docketCmd() *cli.Command {
	return leaf("docket", "docket — list open motions (votes/selects and their outcomes)",
		func(cc *cli.Context, args []string) error {
			resp, err := httpGet("/entities", url.Values{"system": {"motion"}, "kind": {"channel"}})
			if err != nil {
				return fmt.Errorf("docket: %w", err)
			}
			var ents []struct {
				Ref     string         `tony:"field=ref"`
				Payload map[string]any `tony:"field=payload"`
			}
			decodeClose(resp, &ents)
			if len(ents) == 0 {
				fmt.Fprintln(cc.Out, "nothing on the docket")
				return nil
			}
			for _, e := range ents {
				id := strings.TrimPrefix(e.Ref, "motion:channel:")
				p := e.Payload
				axis, _ := p["axis"].(string)
				if axis == "" {
					axis = "motion"
				}
				line := fmt.Sprintf("%s  %-7s %-6v", id, axis, p["status"])
				if names := optionLabels(p["options"]); names != "" {
					line += "  options=[" + names + "]"
				}
				if o, ok := p["outcome"].(string); ok {
					line += "  outcome=" + o
					if tally, ok := p["tally"]; ok {
						line += fmt.Sprintf(" tally=%v", tally)
					}
				} else if at, ok := p["deadline_at"].(string); ok {
					line += "  closes=" + at
				}
				if s, ok := p["subject"].(string); ok {
					line += "  subject=" + s
				}
				fmt.Fprintln(cc.Out, line)
			}
			return nil
		})
}

// optionLabels renders a motion's options as "id(need) …" (the threshold shown only when
// > 1), across both axes: "proceed(2) deny" for a whether-gate, "gpt(2) claude(2)" for a
// select. Where the ballot that counts differs from the option's name — a whether-motion
// tallies approve/reject, not proceed/deny — it is shown as "id=choice", so the listing
// tells a voter what to actually type. Returns "" when there are no options.
func optionLabels(v any) string {
	opts, ok := v.([]any)
	if !ok || len(opts) == 0 {
		return ""
	}
	names := make([]string, 0, len(opts))
	for _, o := range opts {
		om, _ := o.(map[string]any)
		id, _ := om["id"].(string)
		if choice, _ := om["choice"].(string); choice != "" && choice != id {
			id = id + "=" + choice
		}
		// A threshold read out of a decoded payload has no guaranteed Go type — it is
		// float64 from encoding/json, int64 from tony — so it goes through codec.
		if n, ok := codec.AsInt64(om["need"]); ok && n > 1 {
			id = fmt.Sprintf("%s(%d)", id, n)
		}
		names = append(names, id)
	}
	return strings.Join(names, " ")
}
