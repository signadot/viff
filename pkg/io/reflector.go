package io

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/entity"
)

// Source reads external truth for a slice of the entity-viff and hands it back. It owns one
// root (`git`, `github`, …) and is the single writer of it, which is what lets that
// slice go without CAS. See docs/io.md.
//
// IT IS NOT HANDED A STORE, and that is the whole shape of this interface. Reflect answers
// with what it read; converging the store with that answer is the Reflector's job, on viff's
// side of the seam. The three laws converge keeps — skip what is unchanged, tombstone only
// what the answer claims authority over, never tombstone what it does not — are knowledge
// about the entity-viff rather than about git or GitHub, and a connector author who had to
// hold them would get them wrong in a way nothing notices until a charter fires two hundred
// times.
//
// The other half of that: an external source cannot be handed a store anyway, which is why
// the seam is here and not somewhere more convenient.
type Source interface {
	// Root is the slice this source owns and is the single writer of.
	Root() string
	// Reflect reads external truth for the slice `ref` names — a zero ref is everything this
	// source owns — and returns it. It does not write, and it is not handed anything that
	// could.
	Reflect(ctx context.Context, ref entity.Ref) (Reflection, error)
}

// Reflection is one answer: what a source read, and what that answer is COMPLETE for.
type Reflection struct {
	// Covers is the tombstone authority, and it is the load-bearing field. An entity under a
	// covered pattern and absent from Entities is tombstoned; an entity in Entities under no
	// covered pattern is written and never tombstoned.
	//
	// It is not new information — it is the scoping a reconcile always had, said out loud
	// instead of held in a parameter. It is also what turns "keep git history" from a branch
	// in viff's code into a declaration in the source's answer: the git source covers
	// `git.<repo>.ref.*` and `git.<repo>.head` and does NOT cover `git.<repo>.commit.*`, so
	// commits fall out of the bounded tip window and stay.
	//
	// The grammar is entity.Pattern, the charter's own selector, so ownership needs no second
	// vocabulary. A cover's path says WHERE the answer is complete — a repository is a level of
	// the address, so one repository's pass is scoped by the place it reads — and its Predicate,
	// evaluated against the stored payload, narrows further where a place alone cannot: GitHub's
	// open and closed halves of one kind.
	Covers []entity.Pattern `tony:"field=covers,omitzero"`
	// Entities is what external truth says stands there right now.
	Entities []entity.Entity `tony:"field=entities,omitzero"`
}

// Cover is a cover at a place: one segment per level, each a literal name, with a bare `*` any
// one segment (entity.SegMatches). `Cover("git", "viff", "ref", "*")` is an answer complete
// for every ref of one repository; `Cover("git", "viff", "head")` for its head alone. A cover
// that also narrows by payload sets Predicate on the result.
func Cover(segs ...string) entity.Pattern {
	path := make([]*ir.Node, len(segs))
	for i, s := range segs {
		path[i] = ir.FromString(s)
	}
	return entity.Pattern{Path: path}
}

// Add appends one reflected entity, spelled as the object payload most sources build.
func (r *Reflection) Add(ref entity.Ref, p entity.Obj) {
	r.Entities = append(r.Entities, entity.Entity{Ref: ref, Payload: p.Payload()})
}

// Check reports what would make a reflection unsafe to converge, given the root its source
// owns. It is called before anything is written, because every fault here is one that costs
// entities rather than returning an error.
//
// A COVER WITH NO PATH is the dangerous one, and it is dangerous in the direction that is easy
// to miss: an empty path is the root, which every address is under (entity.Pattern.MatchRef), so
// a cover that lost its path does not claim nothing — it claims everything the pass looked at,
// and tombstones whatever the answer did not carry.
func (r Reflection) Check(root string) error {
	for _, p := range r.Covers {
		if len(p.Path) == 0 {
			return fmt.Errorf("reflection: a cover with no path claims EVERYTHING it is compared "+
				"against, since the empty path is the root — begin it with %q", root)
		}
		// A COVER NAMES ITS SOURCE'S PLACE IN ITS FIRST SEGMENT, and it must be a literal: a `*`
		// or an operator there claims every place it is compared against.
		top, ok := entity.PathLiteral(p.Path[0])
		if !ok {
			return fmt.Errorf("reflection: a cover whose path does not begin with a literal "+
				"place claims every place it is compared against — begin it with %q", root)
		}
		if top != root {
			return fmt.Errorf("reflection: cover over root %q, but this source owns %q — a "+
				"source is the single writer of one slice and may not tombstone another's",
				top, root)
		}
		// Op filters a CHANGE, and converge compares against what stands in the store, which
		// has no op. Carrying one here would read as a narrowing that is silently not applied.
		if p.Op != "" {
			return fmt.Errorf("reflection: cover over %s carries op %q — a cover selects "+
				"stored entities, which have no op", coverString(p), p.Op)
		}
	}
	for _, e := range r.Entities {
		// The reserved half FIRST, because `Root()` does not say which half a ref is in: a
		// source registered as `task` answering `mirror.task.instance.x` passes the check below,
		// and converge writes to the store directly — under the refusal that guards `POST
		// /entity`. A source reflects the world; what viff keeps about its own running is
		// written by the verbs that own it.
		if e.Ref.Reserved() {
			return fmt.Errorf("reflection: entity %s is in viff's own half, which no source "+
				"reflects — a source writes what the world is about, beside it", e.Ref)
		}
		if !e.Ref.Under(root) {
			return fmt.Errorf("reflection: entity %s is outside %q, the place this source owns",
				e.Ref, root)
		}
		if len(e.Ref.Segs()) < 2 {
			return fmt.Errorf("reflection: entity %s is the source's whole place, which a "+
				"reflection writes into rather than over", e.Ref)
		}
	}
	return nil
}

// Reflector drives a Source in the informer shape: an initial full reconcile, a periodic full
// reconcile (the resync net that catches every missed hint), and targeted reconciles kicked
// by hint deliveries (Hint). A hint only schedules; the reconcile is the authority.
//
// It also owns converge — the store side of the seam — so every source, in-process or
// deployed, converges through one implementation.
type Reflector struct {
	src    Source
	store  entity.Store
	every  time.Duration
	kick   chan entity.Ref
	onPass func(Pass)
}

// Pass is what one reconcile did, handed to OnPass so a caller can record it without a second
// read of the store.
//
// Wrote is the count of entities written or tombstoned, and ZERO IS THE COMMON CASE: external
// truth is usually what it was a minute ago. That distinction is the whole reason this carries
// a count rather than just an error — "a pass happened" and "a pass changed something" are
// different facts, and a caller that stamps the registration on the first writes to it every
// poll interval forever.
type Pass struct {
	// Ref is what was reconciled; the zero ref is a full pass.
	Ref entity.Ref
	// Wrote counts the entities this pass wrote or tombstoned.
	Wrote int
	// Err is the reflect or converge failure, nil when the pass completed.
	Err error
}

// NewReflector reflects src into store, with a full resync every `every` (<=0 disables the
// periodic net, leaving only hint-driven and initial reconciles).
func NewReflector(src Source, store entity.Store, every time.Duration) *Reflector {
	return &Reflector{src: src, store: store, every: every, kick: make(chan entity.Ref, 64)}
}

// OnPass registers a sink called after every reconcile with what it did. Without one, errors
// are dropped — the periodic net retries anyway.
//
// It replaced an OnError that carried only the failure, which could say a source was broken and
// never that it was working: clearing a recorded error needs the SUCCESS as an event too.
func (rf *Reflector) OnPass(f func(Pass)) *Reflector { rf.onPass = f; return rf }

// Reconcile is one level-triggered pass: reflect the slice `ref` names, then converge the
// store with the answer. A zero ref is a full pass.
//
// It is exported because it is the whole of what a reflector does, and because the initial
// sync, the periodic net and a hint are all this call with different arguments — a caller
// wanting one pass (a test, a `viff iod sync`) should drive the same path they do rather
// than a second one that could differ.
func (rf *Reflector) Reconcile(ctx context.Context, ref entity.Ref) error {
	p := Pass{Ref: ref}
	rn, err := rf.src.Reflect(ctx, ref)
	if err != nil {
		p.Err = fmt.Errorf("reflect %s: %w", rf.src.Root(), err)
	} else {
		p.Wrote, p.Err = rf.converge(ctx, rn)
	}
	if rf.onPass != nil {
		rf.onPass(p)
	}
	return p.Err
}

// converge writes what a reflection changed and tombstones what it covers and did not carry.
// This is the one implementation of the three laws, and each is load-bearing:
//
//   - SKIP UNCHANGED, or every periodic resync re-fires every trigger over the slice. The
//     tree has paid for getting this wrong once, with a measurement attached: comparing
//     nodes with reflect.DeepEqual made a re-established watch re-emit an entire slice as
//     changed — twenty entities delivered forty events, and every trigger over them would
//     have fired again for nothing.
//   - TOMBSTONE WHAT IS GONE, scoped to what the reflection covers, so one repo's pass never
//     deletes another's refs.
//   - NEVER TOMBSTONE WHAT IS NOT COVERED — commits fall out of the bounded tip window and
//     stay.
//
// A write REPLACES rather than merges (entity.Modify), because a source is the single writer
// and its answer is what the entity IS: a field it stopped reporting is a field that is gone.
// A merge would leave the stale field standing, and — since the comparison above is against
// what the source said — the entity would then differ on every pass forever, firing its
// triggers each time. The read Modify costs is paid only by an entity that actually changed.
func (rf *Reflector) converge(ctx context.Context, rn Reflection) (int, error) {
	if err := rn.Check(rf.src.Root()); err != nil {
		return 0, err
	}
	wrote := 0
	current, err := rf.current(rn)
	if err != nil {
		return 0, err
	}
	desired := make(map[string]bool, len(rn.Entities))
	for _, e := range rn.Entities {
		for _, p := range places(rn.Covers, e.Ref, e.Payload) {
			desired[p.at.String()] = true
		}
	}
	for _, e := range rn.Entities {
		// THE FENCE: a cancelled reflector stops WRITING, not merely stops looping.
		//
		// Cancelling used to end the pass only where Run selects on ctx.Done, which is between
		// passes — so a stop signalled mid-pass let every remaining write land, and against a
		// scope being deleted that is a slice re-created one entity at a time in an overlay
		// nobody asked for. A 700-entity first pass makes the window a quarter of an hour.
		//
		// It is a ctx check rather than a precondition because a precondition is what converge
		// deliberately gave up: the note below measures conditional writes at 3–24/second
		// against ~1500 unconditional, and paying that back per entity to defend against a
		// deleted scope would be the same trade the ownership rule already refused. An atomic
		// load costs nothing and answers the same question.
		//
		// It cannot be airtight — a cancel landing between this check and the write still gets
		// through — but it bounds the exposure at ONE write instead of a whole pass, and there
		// is no tighter fence available while the store's write carries the store's context
		// rather than the caller's.
		if err := ctx.Err(); err != nil {
			return wrote, err
		}
		cur, exists := current[e.Ref]
		// Where this answer is authoritative: the node, once, for each cover that reaches it. An
		// entity no cover matches at all is written and never tombstoned — git's commits.
		at := places(rn.Covers, e.Ref, e.Payload)
		if len(at) == 0 {
			at = []spot{{at: e.Ref.At()}}
		}
		for _, p := range at {
			// Compared against the answer AS IT STANDS. This compared against a stripped copy of
			// it, because viff spelled a removed field with a bare null and hid every bare null
			// on read — so a source that legitimately reported one differed from itself on every
			// pass, forever. Absence is `!delete` now and a null is a null, so there is nothing
			// to strip here and nothing to mimic.
			if exists && entity.Equal(cur, e.Payload) {
				continue
			}
			// NO PRECONDITION, and this is the ownership rule finally being spent rather than
			// merely stated. A source is the single writer of its slice, so the compare-and-swap
			// a Modify carries defends against a concurrent writer that does not exist — and it
			// is not free: measured against a local logd, an unconditional patch runs at
			// ~1500/second and does not slow as the document grows, while a conditional one runs
			// at 3–24/second. That factor is what made a 700-entity first pass take a quarter of
			// an hour, on a laptop as much as on a cluster.
			//
			// It holds for two sources that share a thing too: they own two nodes beneath it —
			// `…owners.ENG` and `…owners.OPS` — so neither is a writer of anything the other
			// writes, and there is still no update to lose. What makes that true is the
			// install-time proof that their claims are apart, and nothing here re-checks it.
			//
			// `cur` is what keeps it a REPLACE: a write merges at its path, so a field the source
			// has stopped reporting goes away only because an explicit null is written at it. The
			// List above is where it comes from, in the same pass, from the only writer there is.
			if err := entity.ReplaceAt(rf.store, p.at, cur, e.Payload); err != nil {
				return wrote, fmt.Errorf("reflecting %s: %w", p.at, err)
			}
			wrote++
		}
	}
	for _, p := range covered(rn.Covers, current) {
		if desired[p.at.String()] {
			continue
		}
		// Fenced as the writes above are, and for the sharper reason: a tombstone written into
		// a scope that has just gone away re-creates the overlay in order to record an absence.
		if err := ctx.Err(); err != nil {
			return wrote, err
		}
		if err := rf.removeAt(p); err != nil {
			return wrote, err
		}
		wrote++
	}
	return wrote, nil
}

// removeAt tombstones a node, and collects whatever that emptied: a node holding nothing but the
// null the step below just left is taken too, up to — and not including — the place this spot's
// cover is asked under.
//
// THAT CLIMB IS HOW A SHARED THING IS COLLECTED, and it is the only contended write in the whole
// design. Every other write here is at a node one source owns alone; these are above it, where
// nobody owns alone. Two owners of one project are two nodes beneath it —
// `linear.project.p1.owners.ENG` and `…owners.OPS` — so the last owner out empties `owners`,
// which empties `p1`, and the project goes when the second owner leaves and not before.
//
// THE DELETE IS CONDITIONAL, which is what makes that safe without a lock: the precondition asks
// the node "is every key here mine, or a tombstone", and a refusal means somebody else is still
// there. The tombstone arm is not optional, because logd keeps a removed key as a null leaf, so
// a departed sibling is still a key; without it the condition is false forever and a shared
// thing is never collected (the shared-ownership note has the transcript, in the history).
//
// A refused delete is not an error: it is the ordinary case, and the reason the write is
// conditional at all. It ends the climb, since a node that is still standing has not emptied the
// one above it either.
func (rf *Reflector) removeAt(p spot) error {
	if _, _, err := rf.store.Write(entity.Write{At: p.at}); err != nil {
		return fmt.Errorf("tombstoning %s: %w", p.at, err)
	}
	segs := p.at.Ref.Segs()
	for len(segs) > len(p.place)+1 {
		mine := segs[len(segs)-1]
		parent := entity.RefAt(segs[:len(segs)-1]...)
		only := ir.FromSlice([]*ir.Node{
			ir.FromString(mine).WithTag("!field"),
			ir.Null().WithTag("!irtype"),
		}).WithTag("!all.or")
		took, _, err := rf.store.Write(entity.Write{
			At:   parent.At(),
			When: []entity.Require{{At: parent.At(), Pattern: only}},
		})
		if err != nil {
			return fmt.Errorf("collecting %s: %w", parent, err)
		}
		if !took {
			return nil
		}
		segs = parent.Segs()
	}
	return nil
}

// spot is one node a reflection owns, and the place its cover is asked under —
// `linear.project` for a cover over `linear.project.*.owners.ENG`. The place bounds what
// removing the node may collect: what this source emptied goes with it, and above the place is
// somebody else's.
type spot struct {
	at    entity.Path
	place []string
}

// places is where an answer is authoritative for one node: nothing when no cover matches it, and
// the node itself otherwise.
//
// A node is a whole thing here, at every depth. Two sources that share a thing do not share a
// node — they own two nodes beneath it — so there is no second notion of how MUCH of a node an
// answer is about, and nothing to refuse as a contradiction: an address says it.
//
// The DEEPEST place wins when two covers reach one node, because it is the one that collects
// least. A cover that reaches a node says this source owns what stands there; it does not say
// the source may take the node above it, and where two covers disagree about that the quiet
// answer is the smaller claim.
func places(covers []entity.Pattern, ref entity.Ref, payload entity.Payload) []spot {
	var out []spot
	for _, c := range covers {
		if !c.MatchEntity(entity.Entity{Ref: ref, Payload: payload}) {
			continue
		}
		place := c.Place()
		if len(out) == 0 {
			out = append(out, spot{at: ref.At(), place: place})
			continue
		}
		if len(place) > len(out[0].place) {
			out[0].place = place
		}
	}
	return out
}

// current reads what stands in the store at the places a reflection is about — one read per
// place, at the depth that place's entities sit at, rather than one List at the root.
//
// A PLACE IS WHAT A COVER NAMES, read down to its first wildcard: `git.viff.ref.*` is a read of
// `git.viff.ref` four deep, and `git.viff.head` is a read of that one entity. An answer is
// complete for exactly those, so they are what a tombstone can be decided from.
//
// An entity NO COVER HOLDS is read too, at its parent, because the unchanged-comparison has to
// see it: a git commit is written once and never tombstoned, and one that was not re-read would
// be re-written on every pass.
//
// It does not read the whole root, and on a store carrying several repositories under one
// root that is the difference between a pass reading its own repository and reading every
// repository's history. This runs on every pass.
//
// A READ THAT FAILS ENDS THE PASS. What it answers is what converge compares against, so a
// listing that did not happen, read as an empty one, makes every entity in the answer look
// absent and the pass rewrites the whole slice: staging's git-issue sweep wrote each of a
// repository's issues again, one after another, into a store already timing out its reads.
// The next pass reads again.
func (rf *Reflector) current(rn Reflection) (map[entity.Ref]entity.Payload, error) {
	var at []place
	for _, c := range rn.Covers {
		at = append(at, coverPlace(c))
	}
	held := func(ref entity.Ref) bool {
		for _, p := range at {
			if p.holds(ref) {
				return true
			}
		}
		return false
	}
	for _, e := range rn.Entities {
		if segs := e.Ref.Segs(); !held(e.Ref) {
			at = append(at, place{prefix: segs[:len(segs)-1], depth: len(segs)})
		}
	}
	out := map[entity.Ref]entity.Payload{}
	for _, p := range distinctPlaces(at) {
		// Not viff's own half, for the reason Claimed says at length: a root name is not a
		// half, and this map is what `covered` decides tombstones from.
		//
		// EXACTLY AT THE PLACE'S DEPTH, because a listing also reports a value standing above
		// it (Reader.ListAt), and a head's fields are not refs.
		ents, err := entity.ListAtErr(rf.store, p.prefix, p.depth, func(ref entity.Ref) bool {
			return !ref.Reserved() && len(ref.Segs()) == p.depth
		})
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", strings.Join(p.prefix, "."), err)
		}
		for _, e := range ents {
			out[e.Ref] = e.Payload
		}
	}
	return out, nil
}

// place is one read converge makes: the literal segments to read under, and how deep the
// entities there sit.
type place struct {
	prefix []string
	depth  int
}

// holds reports whether a ref is one of the entities a read of this place returns.
func (p place) holds(ref entity.Ref) bool {
	segs := ref.Segs()
	return len(segs) == p.depth && entity.Under(p.prefix, segs, entity.Literal)
}

// coverPlace is where a cover's entities are read: its path down to the first segment that is
// not a literal name, at the path's own depth.
func coverPlace(c entity.Pattern) place {
	var prefix []string
	for _, seg := range c.Path {
		lit, ok := entity.PathLiteral(seg)
		if !ok {
			break
		}
		prefix = append(prefix, lit)
	}
	return place{prefix: prefix, depth: len(c.Path)}
}

// distinctPlaces drops a place another one already reads: the same depth under a prefix that is
// already being read.
func distinctPlaces(at []place) []place {
	sort.SliceStable(at, func(i, j int) bool { return len(at[i].prefix) < len(at[j].prefix) })
	var out []place
	for _, p := range at {
		redundant := false
		for _, q := range out {
			if q.depth == p.depth && entity.Under(q.prefix, p.prefix, entity.Literal) {
				redundant = true
				break
			}
		}
		if !redundant {
			out = append(out, p)
		}
	}
	return out
}

// coverString renders a cover's address for a message.
func coverString(c entity.Pattern) string {
	segs := make([]string, len(c.Path))
	for i, seg := range c.Path {
		if lit, ok := entity.PathLiteral(seg); ok {
			segs[i] = lit
		} else {
			segs[i] = "*"
		}
	}
	return strings.Join(segs, ".")
}

// covered returns the PLACES in the stored entities a reflection claims tombstone authority
// over, in path order so a pass deletes in the same order twice.
//
// A spot rather than a bare ref, because removing one carries the bound on what it may collect
// with it: the place its cover is asked under.
//
// It asks entity.Pattern the same question a charter does (MatchEntity), rather than Select:
// Select skips an entity marked VANISHING, which is right for an actor that must not act on
// something on its way out, and wrong for the slice's own single writer — an entity hidden
// from converge would be neither refreshed nor tombstoned, and would sit there forever.
func covered(covers []entity.Pattern, current map[entity.Ref]entity.Payload) []spot {
	var out []spot
	for ref, payload := range current {
		out = append(out, places(covers, ref, payload)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at.String() < out[j].at.String() })
	return out
}

// Hint schedules a targeted reconcile of each ref, and a full one if given none — the whole of
// what a hint may do. It writes nothing, whoever delivered it: that is the law, and it is what
// lets a hint route stay unauthenticated in a wide-open viff, since a delivery carrying no
// data can cost at most a reconcile of a slice its sender does not get to choose the contents
// of.
//
// viff does not PARSE deliveries, and that is why this takes refs rather than a request. A
// source owns its own delivery — it deployed the git hook, it holds the webhook secret — and
// tells viff which refs went stale; a second implementation of "map a delivery to refs" per
// source is exactly how the law comes to be broken in one of two places, and across a network
// a divergence is least recoverable. The route that reaches this is
// POST /iods/<name>/hint?at=…
//
// A full queue drops the hint rather than blocking; the periodic net catches it.
func (rf *Reflector) Hint(refs ...entity.Ref) {
	if len(refs) == 0 {
		refs = []entity.Ref{{}} // no target → a full resync
	}
	for _, ref := range refs {
		select {
		case rf.kick <- ref:
		default: // backed up; the periodic net will catch it
		}
	}
}

// Run does the initial full reconcile, then serves hint-kicked and periodic reconciles until
// ctx is cancelled. Run owns the reconcile loop; a caller delivers hints with Hint.
func (rf *Reflector) Run(ctx context.Context) error {
	_ = rf.Reconcile(ctx, entity.Ref{}) // initial full sync; OnPass carries what it did
	var tick <-chan time.Time
	if rf.every > 0 {
		t := time.NewTicker(rf.every)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick:
			_ = rf.Reconcile(ctx, entity.Ref{}) // full resync net
		case ref := <-rf.kick:
			_ = rf.Reconcile(ctx, ref) // targeted, from a hint
		}
	}
}
