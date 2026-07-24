package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/scott-cotton/cli"
)

// voteCmd casts a ballot into a decision's vote log — the participant side of the quorum
// surface. A vote is a plain, visible entity (vote:<decision>:<who>): open, cooperative
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
		WithSynopsis("vote <decision> <choice> --as <who> — cast a ballot (approve|reject, or a select-option)").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *voteConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("%w: vote wants a decision id and a choice (approve|reject) — see `verse decisions`", cli.ErrUsage)
	}
	// choice is approve|reject for a quorum decision, or an option id for a select
	// decision — the vote log is the same, the discipline that reads it differs.
	decision, choice := args[0], args[1]
	if choice == "" {
		return fmt.Errorf("vote: a choice is required (approve|reject, or an option for a select decision)")
	}
	if cfg.As == "" {
		return fmt.Errorf("vote: --as <who> is required (a ballot names its voter)")
	}
	ref := "vote:" + decision + ":" + cfg.As
	resp, err := httpPost("/entity", map[string]any{
		"ref":     ref,
		"payload": map[string]any{"choice": choice, "by": cfg.As},
	})
	if err != nil {
		return fmt.Errorf("vote: %w", err)
	}
	resp.Body.Close()
	fmt.Fprintf(cc.Out, "%s voted %s on decision %s\n", cfg.As, choice, decision)
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
				Ref     string         `json:"ref"`
				Payload map[string]any `json:"payload"`
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

// decisionsCmd lists decision channels — what needs a vote, and how tallies stand. Each is
// written by the engine when a governed decision opens (decision:channel:<id>), across both
// axes: a whether-gate (options proceed/deny) and a who-select (options = the eligible doers).
func decisionsCmd() *cli.Command {
	return leaf("decisions", "decisions — list decision channels (open votes/selects and their outcomes)",
		func(cc *cli.Context, args []string) error {
			resp, err := httpGet("/entities", url.Values{"system": {"decision"}, "kind": {"channel"}})
			if err != nil {
				return fmt.Errorf("decisions: %w", err)
			}
			var ents []struct {
				Ref     string         `json:"ref"`
				Payload map[string]any `json:"payload"`
			}
			decodeClose(resp, &ents)
			if len(ents) == 0 {
				fmt.Fprintln(cc.Out, "no decisions")
				return nil
			}
			for _, e := range ents {
				id := strings.TrimPrefix(e.Ref, "decision:channel:")
				p := e.Payload
				axis, _ := p["axis"].(string)
				if axis == "" {
					axis = "decision"
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

// optionLabels renders a decision's options as "id(need) …" (the threshold shown only when
// > 1), across both axes: "proceed(2) deny" for a whether-gate, "gpt(2) claude(2)" for a
// select. Returns "" when there are no options.
func optionLabels(v any) string {
	opts, ok := v.([]any)
	if !ok || len(opts) == 0 {
		return ""
	}
	names := make([]string, 0, len(opts))
	for _, o := range opts {
		om, _ := o.(map[string]any)
		id, _ := om["id"].(string)
		if n, ok := om["need"].(float64); ok && n > 1 {
			id = fmt.Sprintf("%s(%d)", id, int(n))
		}
		names = append(names, id)
	}
	return strings.Join(names, " ")
}
