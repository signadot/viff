package source

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
)

// Source reads external truth for a slice of the entity-verse and hands it back. It owns one
// system (`git:*`, `github:*`, …) and is the single writer of it, which is what lets that
// slice go without CAS. See docs/sources.md and docs/working/sources.md.
//
// IT IS NOT HANDED A STORE, and that is the whole shape of this interface. Reflect answers
// with what it read; converging the store with that answer is the Reflector's job, on verse's
// side of the seam. The three laws converge keeps — skip what is unchanged, tombstone only
// what the answer claims authority over, never tombstone what it does not — are knowledge
// about the entity-verse rather than about git or GitHub, and a connector author who had to
// hold them would get them wrong in a way nothing notices until a charter fires two hundred
// times.
//
// The other half of that: an external source cannot be handed a store anyway, which is why
// the seam is here and not somewhere more convenient.
type Source interface {
	// System is the slice this source owns and is the single writer of.
	System() string
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
	// in verse's code into a declaration in the source's answer: the git source covers
	// `git:ref:*` and `git:head:*` for a repo and does NOT cover `git:commit:*`, so commits
	// fall out of the bounded tip window and stay.
	//
	// The grammar is entity.Pattern, the charter's own selector, so ownership needs no second
	// vocabulary. A pattern's Predicate is evaluated against the stored payload — for git,
	// `{repo: <id>}`, which is the field it already writes into every entity.
	Covers []entity.Pattern `tony:"field=covers,omitzero"`
	// Entities is what external truth says stands there right now.
	Entities []entity.Entity `tony:"field=entities,omitzero"`
}

// Add appends one reflected entity, spelled as the object payload most sources build.
func (r *Reflection) Add(ref entity.Ref, p entity.Obj) {
	r.Entities = append(r.Entities, entity.Entity{Ref: ref, Payload: p.Payload()})
}

// Check reports what would make a reflection unsafe to converge, given the system its source
// owns. It is called before anything is written, because every fault here is one that costs
// entities rather than returning an error.
//
// A COVER WITH NO SYSTEM is the dangerous one, and it is dangerous in the direction that is
// easy to miss. An empty dimension in a Pattern is a WILDCARD (entity.Pattern.MatchRef), so a
// cover that lost its system does not claim nothing — it claims everything the pass looked
// at, and tombstones whatever the answer did not carry.
func (r Reflection) Check(system string) error {
	for _, p := range r.Covers {
		if p.System == "" {
			return fmt.Errorf("reflection: a cover with no system claims EVERYTHING it is "+
				"compared against, since an empty dimension is a wildcard — name %q", system)
		}
		if p.System != system {
			return fmt.Errorf("reflection: cover over system %q, but this source owns %q — a "+
				"source is the single writer of one slice and may not tombstone another's",
				p.System, system)
		}
		// Op filters a CHANGE, and converge compares against what stands in the store, which
		// has no op. Carrying one here would read as a narrowing that is silently not applied.
		if p.Op != "" {
			return fmt.Errorf("reflection: cover over system %q carries op %q — a cover selects "+
				"stored entities, which have no op", p.System, p.Op)
		}
	}
	for _, e := range r.Entities {
		if e.Ref.System != system {
			return fmt.Errorf("reflection: entity %s is outside %q, the slice this source owns",
				e.Ref, system)
		}
		if e.Ref.Kind == "" || e.Ref.ID == "" {
			return fmt.Errorf("reflection: entity %s has no kind or no id", e.Ref)
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
// wanting one pass (a test, a `verse source sync`) should drive the same path they do rather
// than a second one that could differ.
func (rf *Reflector) Reconcile(ctx context.Context, ref entity.Ref) error {
	p := Pass{Ref: ref}
	rn, err := rf.src.Reflect(ctx, ref)
	if err != nil {
		p.Err = fmt.Errorf("reflect %s: %w", rf.src.System(), err)
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
	if err := rn.Check(rf.src.System()); err != nil {
		return 0, err
	}
	wrote := 0
	current := rf.current(rn)
	desired := make(map[entity.Ref]bool, len(rn.Entities))
	for _, e := range rn.Entities {
		desired[e.Ref] = true
	}
	for _, e := range rn.Entities {
		// Compared against the payload AS THE STORE WILL HOLD IT: a read strips null fields
		// (they are how the log spells a tombstone), so comparing against the raw answer would
		// make a source that writes one differ from itself on every pass.
		if cur, ok := current[e.Ref]; ok && entity.Equal(cur, asStored(e.Payload)) {
			continue
		}
		payload := e.Payload
		if err := entity.Modify(ctx, rf.store, e.Ref, func(entity.Payload) entity.Payload {
			return payload
		}); err != nil {
			return wrote, fmt.Errorf("reflecting %s: %w", e.Ref, err)
		}
		wrote++
	}
	for _, ref := range covered(rn.Covers, current) {
		if desired[ref] {
			continue
		}
		if _, _, err := rf.store.Write(entity.Write{At: ref.At()}); err != nil {
			return wrote, fmt.Errorf("tombstoning %s: %w", ref, err)
		}
		wrote++
	}
	return wrote, nil
}

// current reads what stands in the store for the systems the reflection touches — one List,
// prefiltered by system, which is what a store's List takes.
//
// It reads the whole system slice rather than only what the covers select, because the
// unchanged-comparison has to see entities that no cover claims: a git commit is written once
// and never tombstoned, and one that was re-read without this would be re-written on every
// pass.
func (rf *Reflector) current(rn Reflection) map[entity.Ref]entity.Payload {
	systems := map[string]bool{}
	for _, p := range rn.Covers {
		systems[p.System] = true
	}
	for _, e := range rn.Entities {
		systems[e.Ref.System] = true
	}
	out := map[entity.Ref]entity.Payload{}
	for _, e := range rf.store.List(func(ref entity.Ref) bool { return systems[ref.System] }) {
		out[e.Ref] = e.Payload
	}
	return out
}

// covered returns the refs of the stored entities a reflection claims tombstone authority
// over, in ref order so a pass deletes in the same order twice.
//
// It asks entity.Pattern the same question a charter does (MatchEntity), rather than Select:
// Select skips an entity marked VANISHING, which is right for an actor that must not act on
// something on its way out, and wrong for the slice's own single writer — an entity hidden
// from converge would be neither refreshed nor tombstoned, and would sit there forever.
func covered(covers []entity.Pattern, current map[entity.Ref]entity.Payload) []entity.Ref {
	var out []entity.Ref
	for ref, payload := range current {
		for _, p := range covers {
			if p.MatchEntity(entity.Entity{Ref: ref, Payload: payload}) {
				out = append(out, ref)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// asStored renders a payload the way a read of it will come back: an object's null fields are
// how the log spells a tombstone, so the store drops them (entity's nodeToPayload) and a
// comparison against the unstripped answer would never hold.
func asStored(p entity.Payload) entity.Payload {
	if p == nil || p.Type != ir.ObjectType {
		return p
	}
	live := map[string]*ir.Node{}
	for k, v := range ir.ToMap(p) {
		if v == nil || v.Type == ir.NullType {
			continue
		}
		live[k] = asStored(v)
	}
	return ir.FromMap(live).WithTag(p.Tag)
}

// Hint schedules a targeted reconcile of each ref, and a full one if given none — the whole of
// what a hint may do. It writes nothing, whoever delivered it: that is the law, and it is what
// lets a hint route stay unauthenticated in a wide-open verse, since a delivery carrying no
// data can cost at most a reconcile of a slice its sender does not get to choose the contents
// of.
//
// verse does not PARSE deliveries, and that is why this takes refs rather than a request. A
// source owns its own delivery — it deployed the git hook, it holds the webhook secret — and
// tells verse which refs went stale; a second implementation of "map a delivery to refs" per
// source is exactly how the law comes to be broken in one of two places, and across a network
// a divergence is least recoverable. The route that reaches this is
// POST /sources/<system>/<name>/hint?ref=…
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
