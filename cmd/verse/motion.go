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

// motionCmd is the motions collection: the docket bare, one motion by id, and the one thing a
// person does to a motion — casting a ballot into its vote log.
//
// ONE COMMAND, like every other collection, because a motion is one thing whichever end of it
// you are holding. The listing and the ballot are not two subjects that happen to share a
// noun: what `verse motion` prints is exactly what `--vote` answers, down to the words a
// choice may be (see choices), so a reader who has just been shown a question has the command
// that answers it under the cursor rather than a cross-reference to a second name.
//
// THE CHOICE IS A FLAG'S VALUE, not a verb, and that is the split `gate` draws in the other
// direction. A gate's answer is approve|deny — a closed binary, so it is grammar and gets a
// subcommand each. A motion's choice is an option id, and on the who-axis that is a provider
// name: an OPEN set, which is data, and data belongs on the right-hand side of a flag. The
// positional is then the motion in every case — bare it is every motion, with an id it is that
// one, and `--vote` says what to do about the one named — which is `data`'s shape and for
// `data`'s reason.
//
// A vote is a plain, visible entity (vote:<motion>:<who>): open, cooperative voting, not
// enforced secrecy. Storing each ballot as its own record keeps the ordered vote log, so richer
// disciplines (ranked, timed-ranked) can read it later; secret ballots via COW scopes are a
// later, deliberately soft option.
type motionConfig struct {
	*cli.Command
	Vote string `cli:"name=vote desc='cast a ballot into the motion named: approve|reject, or a select-option'"`
	As   string `cli:"name=as desc='voter identity (REQUIRED with --vote — a ballot names its voter)'"`
	// --watch is the SAME collection, as it changes rather than as it stands. A docket is read
	// by somebody waiting on it, so the state of it now and the changes to it are one question
	// asked at two times — which is what `data` and `watch` already are to each other.
	//
	// It streams from now. The cursor pair belongs to `watch`, where the address is the whole
	// argument: `verse watch verse motion channel --ago 20` is the same stream with history,
	// and two flags here whose meaning is written down over there would be worse than the one
	// spelling that carries them.
	Watch bool `cli:"name=watch desc='stream the changes to the motions instead of the state of them now'"`
}

func motionCmd() *cli.Command {
	cfg := &motionConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "motion").
		WithAliases("motions").
		WithSynopsis("motion — the motions: bare lists them, <id> is one, --vote <choice> --as <who> answers one").
		// Every listing filters, this one included — and on a watch the pair applies per frame,
		// since a frame is what a stream lists one of.
		WithOpts(append(opts, filterOpts()...)...).
		WithRun(cfg.run)
}

func (cfg *motionConfig) run(cc *cli.Context, args []string) error {
	// ONE TOKEN, not steps: a motion is named by its id, and the id is a key in the document
	// this command prints. `verse motion verse motion channel m1` is somebody typing `data`'s
	// spelling, and the answer is to say so rather than to read the first word as the id.
	id, err := optionalArg(cfg.Command, cc, args,
		"motion takes one motion id (bare, it lists every motion)")
	if err != nil {
		return err
	}
	if cfg.Vote != "" {
		return cfg.vote(cc, id)
	}
	// --as ALONE ANSWERS NOTHING. It is the ballot's other half, so unaccompanied it is a
	// half-typed vote — and a listing that ignored it would print the docket and look like it
	// had done what was asked.
	if cfg.As != "" {
		return fmt.Errorf("%w: --as names a voter, so it goes with --vote: "+
			"`verse motion <id> --vote <choice> --as %s`", cli.ErrUsage, cfg.As)
	}
	if cfg.Watch {
		return cfg.stream(cc, id)
	}
	return cfg.list(cc, id)
}

// list prints the docket — every motion, or the one named — as a document.
//
// TONY, like every other collection this CLI prints, rather than a line format of its own. A
// docket entry is a motion plus the one thing a motion cannot say about itself, and both are
// data: `verse motion -o yaml`, `| o get "[0].subject"`, and a diff against the docket a minute
// ago all work, where a bespoke line meant re-parsing prose to ask anything of it at all.
//
// ONE MOTION IS ONE DOCUMENT, not an array of one, which is the distinction `data` draws at the
// same place: an address that names one thing answers with that thing. `--if` and `--trim` then
// mean what they mean everywhere — an array by its elements, anything else as one item.
func (cfg *motionConfig) list(cc *cli.Context, id string) error {
	// ONE MOTION IS READ AS ONE ENTITY, by the same read the ballot checks it with, because a
	// listing narrowed to a single address cannot tell an absent motion from a present one: a
	// read there answers with the payload standing at it, and nothing standing at it is `null`
	// — an entry, with an id and no motion under it.
	if id != "" {
		got, ok, err := readMotion(id)
		if err != nil {
			return fmt.Errorf("motion: %w", err)
		}
		if !ok {
			return fmt.Errorf("%w: no motion %q is on the docket — see `verse motion`",
				cli.ErrUsage, id)
		}
		return printNode(cc, motionEntries([]motionRow{{id: id, payload: got}})[0])
	}
	// In verse's own half: a motion is a record verse keeps about its own running, so this
	// reads `verse.motion.channel` rather than the bare system.
	ents, err := stateAt(motionsAt(""))
	if err != nil {
		return fmt.Errorf("motion: %w", err)
	}
	if len(ents) == 0 {
		fmt.Fprintln(cc.Out, "no motions")
		return nil
	}
	rows := make([]motionRow, 0, len(ents))
	for _, e := range ents {
		// The id is a KEY in the document, so it is read rather than trimmed off a ref.
		rows = append(rows, motionRow{id: e.Ref.ID(), payload: entity.ToObj(e.Payload)})
	}
	return printNode(cc, motionEntries(rows))
}

// stream is the docket as it changes: the same entities, watched where they live.
func (cfg *motionConfig) stream(cc *cli.Context, id string) error {
	at := motionsAt(id)
	if _, err := entity.ParseAt(at); err != nil {
		return fmt.Errorf("motion: %w", err)
	}
	return streamChanges(cc, "motion", url.Values{"at": {at}})
}

// motionsAt is where the motions live: all of them, or the one named. A motion's id is a KEY
// in a document rather than a word in a sentence, so it is rendered through the address
// grammar — an id needing quotes is an id, not a syntax error.
func motionsAt(id string) string {
	if id == "" {
		return entity.VerseRoot + ".motion.channel"
	}
	return entity.VerseRef("motion", "channel", id).KPath()
}

// motionRow is one motion as read: its id, which is a key in the document rather than a token
// inside a rendered ref, and what it says about itself.
type motionRow struct {
	id      string
	payload map[string]any
}

// motionEntries folds each motion together with the one thing a motion cannot say about
// itself — what its subject says it is about.
//
// EVERY SUBJECT AT ONCE, before anything is written. Each `says` is its own request, which is
// nothing against a verse on localhost and a visible stutter against one across a network: a
// serial docket prints a line, waits a round trip, prints the next. Concurrent, so the wall
// clock is one round trip rather than N.
func motionEntries(rows []motionRow) []map[string]any {
	says := make([]string, len(rows))
	var wg sync.WaitGroup
	for i, r := range rows {
		ref, _ := r.payload["subject"].(string)
		if ref == "" {
			continue
		}
		wg.Add(1)
		go func(i int, ref string) { defer wg.Done(); says[i] = subjectSays(ref) }(i, ref)
	}
	wg.Wait()

	// WHAT IS BEING DECIDED travels beside the address of it. A docket entry is a status and a
	// ref plus the subject's own line, because a gate answered without knowing what it is about
	// is answered by reflex, which is the failure the gate exists to prevent.
	out := make([]map[string]any, 0, len(rows))
	for i, r := range rows {
		m := map[string]any{"id": r.id}
		for k, v := range r.payload {
			m[k] = v
		}
		if says[i] != "" {
			m["says"] = says[i]
		}
		out = append(out, m)
	}
	return out
}

// vote casts a ballot into a motion's vote log — the participant side of the quorum surface.
func (cfg *motionConfig) vote(cc *cli.Context, motion string) error {
	// A shell hands an unset variable through as an empty word: `verse motion "$m" --vote
	// approve` with $m unset would build the ref "vote::alice" — a well-formed write of a
	// ballot belonging to no motion, which no tally will ever read and nothing will ever
	// complain about.
	if motion == "" {
		return fmt.Errorf("%w: --vote answers one motion, so it wants an id: "+
			"`verse motion <id> --vote %s --as <who>` — see `verse motion`", cli.ErrUsage, cfg.Vote)
	}
	if err := requireAs(cfg.As, "who", "a ballot names its voter"); err != nil {
		return err
	}
	if err := checkChoice(motion, cfg.Vote); err != nil {
		return err
	}
	resp, err := httpPost("/entity", api.PutEntity{
		At:      entity.JoinSegs([]string{"vote", motion, cfg.As}),
		Payload: entity.Obj{"choice": cfg.Vote, "by": cfg.As}.Payload(),
	})
	if err != nil {
		return fmt.Errorf("vote: %w", err)
	}
	defer resp.Body.Close()
	// The STATUS, not just the transport. A confirmation printed on any answer the server gave
	// would report a refused ballot as cast, and the only way to find out would be that the
	// tally never moved.
	if resp.StatusCode >= http.StatusBadRequest {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if m := strings.TrimSpace(string(msg)); m != "" {
			return fmt.Errorf("vote: %s", m)
		}
		return fmt.Errorf("vote: %s", resp.Status)
	}
	// The motion FIRST, because that is the argument order and a confirmation that reads back
	// in the order you typed is one you can check. "me voted 7a11e2ef on motion approve" parses
	// as a sentence whichever way round the values are, so a swap reads as success.
	fmt.Fprintf(cc.Out, "motion %s: %s voted %q\n", motion, cfg.As, cfg.Vote)
	// AND WHETHER IT CAN DECIDE ANYTHING. An `!until [!quorum 2, !deadline "24h"]` closes on the
	// quorum or on the clock, so no number of ballots for the deadline's default option settles
	// it: two people can deny and the motion stays open, subject untouched
	// (git-issue 88r53xm6h12ks6bng9n0). The ballot is still worth casting, and the voter is
	// entitled to know it is dissent on the record rather than a decision.
	if why := deadlineOnly(motion, cfg.Vote); why != "" {
		fmt.Fprintln(cc.Out, why)
	}
	return nil
}

// deadlineOnly reports the note owed to somebody who just voted for an option no tally can
// reach, or "" when the ballot counts toward something. Read off the motion's own options, so
// it says nothing when the motion cannot be read — the ballot is cast either way, and a
// warning invented from a failed read would be worse than silence.
func deadlineOnly(motion, choice string) string {
	got, ok, err := readMotion(motion)
	if err != nil || !ok {
		return ""
	}
	opts, _ := got["options"].([]any)
	for _, o := range opts {
		om, _ := o.(map[string]any)
		id, _ := om["id"].(string)
		c, _ := om["choice"].(string)
		if choice != id && choice != c {
			continue
		}
		if by, _ := om["reachedBy"].(string); by == "deadline" {
			return fmt.Sprintf("  recorded, and it decides nothing on its own: %q is reached "+
				"only when this motion's deadline passes, so no number of these closes it. "+
				"The other option carries the threshold.", choice)
		}
		return ""
	}
	return ""
}

// checkChoice refuses a ballot no tally could ever count. A quorum leaf matches
// `{choice: approve}` and a select matches the option id, so a choice naming neither lands in
// the vote log, counts nothing, and leaves the motion open with nothing on the docket to say
// why — which is what `--vote y` would do, `y` being the word `gate approve` and the
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
	// The swap is why this matters more than it looks: the id and the choice are both bare
	// words, so `verse motion approve --vote <id>` records a ballot on a motion called
	// "approve" with the id as the choice, and leaves the docket unchanged. Both halves of that
	// are checked here.
	if !ok {
		if _, isMotion, _ := readMotion(choice); isMotion {
			return fmt.Errorf("%w: no motion %q — but %q is one, so the id and the choice are "+
				"the wrong way round: `verse motion %s --vote %s`",
				cli.ErrUsage, motion, choice, choice, motion)
		}
		return fmt.Errorf("%w: no motion %q is on the docket — see `verse motion`",
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
	return fmt.Errorf("%w: motion %s takes %s, not %q — see `verse motion`",
		cli.ErrUsage, motion, strings.Join(want, " or "), choice)
}

// readMotion reads one motion off the docket: its payload, whether it is there at all, and
// whether the read could be performed.
func readMotion(id string) (map[string]any, bool, error) {
	if id == "" {
		return nil, false, nil
	}
	resp, err := httpGet("/entity", url.Values{"at": {entity.VerseRef("motion", "channel", id).KPath()}})
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
// here from drifting apart, since the listing prints those same options.
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

// subjectSays is the one line a motion's subject offers about itself, or "" when it offers none.
//
// A CONVENTION rather than a schema: an entity that wants to be decidable puts a `says` on
// itself, and a charter fills it from whatever produced the fact — comment-drift.tony carries the
// review's own report onto the proposal it derives. Nothing requires it and nothing breaks
// without it, which is why this is silent about every failure: a listing that refused to show a
// motion because its subject was unreadable would be worse than one that shows it bare.
//
// One request per motion, issued CONCURRENTLY by the caller. A docket is short by construction —
// it is the things waiting on a person — but short is a count, not a duration: serial requests to
// a verse across a network printed one line per round trip, which reads as the command hanging
// between items. The alternative to asking at all is the engine denormalising a summary onto
// every motion it writes, which is a schema decision that should be made by somebody who wants it.
func subjectSays(ref string) string {
	// The subject is a rendered address on the motion's record, so it is read back the way
	// every recovered address is (ParseAddr) and sent as the kinded path the wire takes.
	subj, err := entity.ParseAddr(ref)
	if err != nil {
		return ""
	}
	resp, err := httpGet("/entity", url.Values{"at": {subj.KPath()}})
	if err != nil {
		return ""
	}
	var ent struct {
		Payload struct {
			Says string `tony:"field=says,omitzero"`
		} `tony:"field=payload"`
	}
	decodeClose(resp, &ent)
	// WHOLE, not clipped. A summary with an ellipsis in it is a value nothing downstream can
	// use: not a diff, not a match, not a reader piping it somewhere narrower. Truncating is
	// the renderer's job, and the renderer is whoever is looking.
	return strings.TrimSpace(ent.Payload.Says)
}
