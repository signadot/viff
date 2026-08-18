package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/api"
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
	defer resp.Body.Close()
	// The STATUS, not just the transport. This printed "voted" on any answer the server gave,
	// including a refusal — so a ballot that was never recorded reported itself cast, and the
	// only way to find out was that the tally never moved.
	if resp.StatusCode >= http.StatusBadRequest {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if m := strings.TrimSpace(string(msg)); m != "" {
			return fmt.Errorf("vote: %s", m)
		}
		return fmt.Errorf("vote: %s", resp.Status)
	}
	// The motion FIRST, because that is the argument order and a confirmation that reads back in
	// the order you typed is one you can check. "me voted 7a11e2ef on motion approve" parses as
	// a sentence whichever way round the values are, which is how a swap went unnoticed.
	fmt.Fprintf(cc.Out, "motion %s: %s voted %q\n", motion, cfg.As, choice)
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
	got, ok, err := readMotion(motion)
	if err != nil {
		return nil // cannot read it: not our call to make
	}
	// A MOTION THAT IS NOT THERE takes no ballots. The store would accept the write — a ballot
	// is an ordinary entity — and nothing would ever read it, so the voter learns nothing and
	// the tally they were watching never moves.
	//
	// The swap is why this matters more than it looks: `vote approve <id>` reads as English and
	// is the wrong way round, so it recorded a ballot on a motion called "approve" with the id
	// as the choice, and the docket was unchanged. Both halves of that are checked here.
	if !ok {
		if _, isMotion, _ := readMotion(choice); isMotion {
			return fmt.Errorf("%w: no motion %q — but %q is one, so the arguments are the wrong "+
				"way round: `verse vote %s %s`", cli.ErrUsage, motion, choice, choice, motion)
		}
		return fmt.Errorf("%w: no motion %q is on the docket — see `verse docket`",
			cli.ErrUsage, motion)
	}
	ent := struct{ Payload map[string]any }{Payload: got}
	if err := checkOpen(motion, ent.Payload); err != nil {
		return err
	}
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

// readMotion reads one motion off the docket: its payload, whether it is there at all, and
// whether the read could be performed.
func readMotion(id string) (map[string]any, bool, error) {
	if id == "" {
		return nil, false, nil
	}
	resp, err := httpGet("/entity", url.Values{"ref": {"verse:motion:channel:" + id}})
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, false, nil
	}
	var ent struct {
		Payload map[string]any `tony:"field=payload"`
	}
	decodeClose(resp, &ent)
	return ent.Payload, len(ent.Payload) > 0, nil
}

// checkOpen refuses a ballot into a motion that is not taking any, for the reason checkChoice
// refuses one no tally could count: the store accepts the write either way, so a voter who is
// not told learns it from a tally that never moves.
//
// Two ways a motion stops taking ballots, and they are worth telling apart. A COMMITTED one is
// decided — terminal-once, so a later ballot cannot move it, and saying so beside the outcome
// is what stops somebody voting on last week's question. An ORPHANED one was never decided:
// nothing is reading the answer (a restart it could not be resumed into), or the question
// stopped applying (its rule no longer selects the subject it is about). It is not final — a
// rule that selects that subject again reopens the same channel with the same ballots — which
// is why the refusal repeats the `why` off the motion rather than declaring the question closed.
func checkOpen(motion string, payload map[string]any) error {
	if outcome, _ := payload["outcome"].(string); outcome != "" {
		return fmt.Errorf("%w: motion %s is already decided (%s) — a ballot cannot move a "+
			"committed motion", cli.ErrUsage, motion, outcome)
	}
	if status, _ := payload["status"].(string); status == "orphaned" {
		why, _ := payload["why"].(string)
		if why == "" {
			why = "nothing is waiting on it"
		}
		return fmt.Errorf("%w: motion %s is not taking ballots: %s", cli.ErrUsage, motion, why)
	}
	return nil
}

// choices lists the ballots a motion's options can be answered with: the `choice` a
// whether-gate tallies (approve/reject), or the option id a select is picked by. It
// reads the motion's own options rather than a fixed vocabulary, which is what lets one check
// serve both axes — and what keeps the words a voter is told to type and the words accepted
// here from drifting apart, since the docket prints those same options.
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
// `verse data provider:review:claude --put '{"driver":"llm","tier":"premium"}'`).
//
// Singular with a plural alias, like every other collection, even though it has no verbs to
// act through: the rule an operator learns is "the command is the singular", and one listing
// that answers only to the plural is one exception to remember.
func providerCmd() *cli.Command {
	return listing("provider", "provider — the provider registry (provider:<capability>:<id>)",
		func(cc *cli.Context, args []string) error {
			// The capability is the KIND, read out of the document rather than sliced back off
			// the front of a rendered ref.
			ents, err := stateAt("provider")
			if err != nil {
				return fmt.Errorf("provider: %w", err)
			}
			if len(ents) == 0 {
				fmt.Fprintln(cc.Out, "no providers")
				return nil
			}
			for _, e := range ents {
				payload := entity.ToObj(e.Payload)
				line := fmt.Sprintf("%-10s %-14s driver=%v", e.Ref.Kind(), e.Ref.ID(), payload["driver"])
				for k, v := range payload {
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
	return listing("docket", "docket — list open motions (votes/selects and their outcomes)",
		func(cc *cli.Context, args []string) error {
			// In verse's own half: a motion is a record verse keeps about its own running,
			// so the docket reads `verse.motion.channel` rather than the bare system.
			ents, err := stateAt(entity.VerseRoot + ".motion.channel")
			if err != nil {
				return fmt.Errorf("docket: %w", err)
			}
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
				ref := entity.Str(e.Payload, "subject")
				if ref == "" {
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
				// The id is a KEY in the document, so it is read rather than trimmed off a ref.
				m := map[string]any{"id": e.Ref.ID()}
				for k, v := range entity.ToObj(e.Payload) {
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
// itself, and a charter fills it from whatever produced the fact — comment-drift.tony carries the
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
