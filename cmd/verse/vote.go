package main

import (
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// voteCmd casts a ballot into a motion's vote log — the participant side of the quorum
// surface. A vote is a plain, visible entity (vote:<motion>:<who>): open, cooperative
// voting, not enforced secrecy. Storing each ballot as its own record keeps the ordered
// vote log, so richer disciplines (ranked, timed-ranked) can read it later; secret
// ballots via COW scopes are a later, deliberately soft option.
type voteConfig struct {
	*cli.Command
	As string `cli:"name=as desc='voter identity (REQUIRED — a ballot names its voter)'"`
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
	// Both positionals are checked for emptiness, because a shell hands an unset variable
	// through as one: `verse vote "$m" y` with $m unset built the ref "vote::alice" — a
	// well-formed write of a ballot belonging to no motion, which no tally will ever read and
	// nothing will ever complain about.
	if motion == "" {
		return fmt.Errorf("%w: vote wants a motion id — see `verse docket`", cli.ErrUsage)
	}
	if choice == "" {
		return fmt.Errorf("%w: vote wants a choice (approve|reject, or an option for a select motion)", cli.ErrUsage)
	}
	if err := requireAs(cfg.As, "who", "a ballot names its voter"); err != nil {
		return err
	}
	if err := checkChoice(motion, choice); err != nil {
		return err
	}
	ref := "vote:" + motion + ":" + cfg.As
	resp, err := httpPost("/entity", api.PutEntity{
		Ref:     ref,
		Payload: entity.Obj{"choice": choice, "by": cfg.As}.Payload(),
	})
	if err != nil {
		return fmt.Errorf("vote: %w", err)
	}
	resp.Body.Close()
	fmt.Fprintf(cc.Out, "%s voted %s on motion %s\n", cfg.As, choice, motion)
	return nil
}

// checkChoice refuses a ballot no tally could ever count. A quorum leaf matches
// `{choice: approve}` and a select matches the option id, so a choice naming neither lands in
// the vote log, counts nothing, and leaves the motion open with nothing on the docket to say
// why — which is what `vote <motion> y` did, `y` being the word `gate approve` and the
// interactive prompt both teach. Silently accepting a vote that cannot be tallied is the worst
// of the three available behaviours: worse than counting it, and worse than saying so.
//
// It refuses rather than translating. A ballot is a durable, attributed record of what
// somebody chose, and a client that quietly rewrote `y` to `approve` would be putting a word
// in the voter's mouth — cheap here, and the wrong precedent for a log that richer disciplines
// are meant to read later.
//
// A motion the client cannot read is not grounds to refuse: an unreachable verse or an
// unreadable payload leaves the ballot to the server, exactly as before. What is refused is
// the case that is certainly wrong — the motion is right there, its options are known, and the
// choice is none of them.
func checkChoice(motion, choice string) error {
	resp, err := httpGet("/entity", url.Values{"ref": {"motion:channel:" + motion}})
	if err != nil {
		return nil // cannot read it: not our call to make
	}
	var ent struct {
		Payload map[string]any `tony:"field=payload"`
	}
	decodeClose(resp, &ent)
	want := choices(ent.Payload["options"])
	if len(want) == 0 {
		return nil
	}
	for _, w := range want {
		if choice == w {
			return nil
		}
	}
	return fmt.Errorf("%w: motion %s takes %s, not %q — see `verse docket`",
		cli.ErrUsage, motion, strings.Join(want, " or "), choice)
}

// choices lists the ballots a motion's options can be answered with: the `choice` a
// whether-gate tallies (approve/reject), or the option id a select is picked by. It mirrors
// what optionLabels shows after the "=", so the words a voter is told to type and the words
// accepted here cannot drift apart.
func choices(v any) []string {
	opts, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		om, _ := o.(map[string]any)
		c, _ := om["choice"].(string)
		if c == "" {
			c, _ = om["id"].(string)
		}
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// providerCmd lists the provider registry — the attribute-rich doers a select gate draws
// from (provider:<capability>:<id>). Providers are registered as entities (e.g.
// `verse entity put provider:review:claude '{"driver":"llm","tier":"premium"}'`).
//
// Singular with a plural alias, like every other collection, even though it has no verbs to
// act through: the rule an operator learns is "the command is the singular", and one listing
// that answers only to the plural is one exception to remember.
func providerCmd() *cli.Command {
	return leaf("provider", "provider — the provider registry (provider:<capability>:<id>)",
		func(cc *cli.Context, args []string) error {
			resp, err := httpGet("/entities", url.Values{"system": {"provider"}})
			if err != nil {
				return fmt.Errorf("provider: %w", err)
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
		}).WithAliases("providers")
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
			// EVERY SUBJECT AT ONCE, before anything is written. Each `says` is its own
			// request, which is nothing against a verse on localhost and a visible stutter
			// against one across a network: the docket printed a line, waited a round trip,
			// printed the next. Concurrent, so the wall clock is one round trip rather than N.
			says := make([]string, len(ents))
			var wg sync.WaitGroup
			for i, e := range ents {
				ref, ok := e.Payload["subject"].(string)
				if !ok {
					continue
				}
				wg.Add(1)
				go func(i int, ref string) { defer wg.Done(); says[i] = subjectSays(ref) }(i, ref)
			}
			wg.Wait()

			// TONY, like every other collection this CLI prints, rather than a line format of
			// its own. A docket entry is a motion plus the one thing a motion cannot say about
			// itself, and both are data: `verse docket -o yaml`, `| o get "[0].subject"`, and a
			// diff against the docket a minute ago all work, where a bespoke line meant
			// re-parsing prose to ask anything of it at all.
			//
			// WHAT IS BEING DECIDED travels beside the address of it. A docket entry used to be
			// a status and a ref, so the only way to learn what a vote meant was to fetch the
			// subject, read a branch name and two shas out of it, and go find the diff
			// elsewhere. A gate answered without knowing what it is about is answered by
			// reflex, which is the failure the gate exists to prevent.
			out := make([]map[string]any, 0, len(ents))
			for i, e := range ents {
				m := map[string]any{"id": strings.TrimPrefix(e.Ref, "motion:channel:")}
				for k, v := range e.Payload {
					m[k] = v
				}
				if says[i] != "" {
					m["says"] = says[i]
				}
				out = append(out, m)
			}
			return printNode(cc, out)
		})
}

// subjectSays is the one line a motion's subject offers about itself, or "" when it offers none.
//
// A CONVENTION rather than a schema: an entity that wants to be decidable puts a `says` on
// itself, and a charter fills it from whatever produced the fact — merge-gate.tony carries the
// review's own report onto the proposal it derives. Nothing requires it and nothing breaks
// without it, which is why this is silent about every failure: a docket that refused to list a
// motion because its subject was unreadable would be worse than one that lists it bare.
//
// One request per motion, issued CONCURRENTLY by the caller. A docket is short by construction —
// it is the things waiting on a person — but short is a count, not a duration: serial requests to
// a verse across a network printed one line per round trip, which reads as the command hanging
// between items. The alternative to asking at all is the engine denormalising a summary onto
// every motion it writes, which is a schema decision that should be made by somebody who wants it.
func subjectSays(ref string) string {
	resp, err := httpGet("/entity", url.Values{"ref": {ref}})
	if err != nil {
		return ""
	}
	var ent struct {
		Payload struct {
			Says string `tony:"field=says,omitzero"`
		} `tony:"field=payload"`
	}
	decodeClose(resp, &ent)
	// WHOLE, not clipped. This used to return one line of at most a hundred characters,
	// because it was being pasted into a terminal line — but the docket is data now, and a
	// summary with an ellipsis in it is a value nothing downstream can use: not a diff, not a
	// match, not a reader piping it somewhere narrower. Truncating is the renderer's job, and
	// the renderer is whoever is looking.
	return strings.TrimSpace(ent.Payload.Says)
}

// optionLabels renders a motion's options as "id(need) …" (the threshold shown only when
// > 1), across both axes: "proceed(2) deny" for a whether-gate, "gpt(2) claude(2)" for a
// select. Where the ballot that counts differs from the option's name — a whether-motion
// tallies approve/reject, not proceed/deny — it is shown as "id=choice", so the listing
// tells a voter what to actually type. Returns "" when there are no options.
//
// An option that would run under its own envelope shows it: "fixer[no-delegate +run_bash]".
// A who-motion's options stopped being interchangeable when a doer could bring its own grant
// and tools — picking between two names became picking between two authorities — and a
// listing that showed only the names would ask a voter to authorise a capability nobody put
// in front of them. Only what DIFFERS from a plain name is added, so a ballot between doers
// that vary by nothing but model reads exactly as it did before.
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
		if env := optionEnvelope(om); env != "" {
			id += "[" + env + "]"
		}
		names = append(names, id)
	}
	return strings.Join(names, " ")
}

// optionEnvelope renders what a select option would run AS: its grant, and the tools it is
// narrowed to. The tools are prefixed "+" so a reader can tell a tool list from a grant name
// without knowing the grant vocabulary by heart. "" when the option carries neither, which is
// every whether-gate option and every doer that only names a driver.
func optionEnvelope(om map[string]any) string {
	var parts []string
	if g, _ := om["grant"].(string); g != "" {
		parts = append(parts, g)
	}
	if ts, ok := om["tools"].([]any); ok && len(ts) > 0 {
		tools := make([]string, 0, len(ts))
		for _, t := range ts {
			if name, ok := t.(string); ok {
				tools = append(tools, name)
			}
		}
		if len(tools) > 0 {
			parts = append(parts, "+"+strings.Join(tools, ","))
		}
	}
	return strings.Join(parts, " ")
}
