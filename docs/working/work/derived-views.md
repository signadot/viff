# Derived views: a path read that tracks

Status: proposal, nothing here runs. It answers a question about derived
facts going stale, and proposes one spelling — `!view.get-path` / `!view.list-paths` — for the
tracking half of a read. The claim it rests on is that a view is a *dependency declaration*
rather than a recompute engine, and that the machinery to honour it is already built.

Its Go sketch is written against types that have since moved: a `pathSpec` is `At []*ir.Node`, a
kinded path at any depth, and `reads()` answers subtrees with a depth rather than a system and a
kind. The argument survives the spelling; the sketch wants re-expressing before anyone builds
from it.

## The question

`derive` writes computed entities and [strata](../../strata.md) prevent cycles, but a derive only
fires when its rule fires. If an input changes some other way the derived value goes stale.
Do we need view semantics, where a derived fact recomputes whenever its inputs change, or is
event-driven good enough?

## What is true today

The gap is not incidental and it is already written down. `trigger/path.go` says it of itself:

> **THEY COPY, AND DO NOT TRACK.** Whatever is read is read once, at the commit that caused the
> firing. If it changes afterwards nothing re-runs: this rule fires again only when its own
> subject changes, or when an `on`/`when` it carries covers the target. Copying is the point,
> but it is the thing someone will file a bug about.

The reason is structural rather than an oversight. A trigger wakes on what `Trigger.reads()`
returns (`trigger/edge.go`), and that is the *condition's* subtrees only — its `on`, its `when`,
and the set its `subject` selects. Path reads live in the **action**, and the action is not
consulted:

```go
func (t Trigger) reads() [][2]string {
	// ...
	add(t.Condition.On.Subtrees())
	add(t.Condition.When.Subtrees())
	add(matchFormula(t.Condition.Subject).Subtrees())
	return out
}
```

Since this was written `reads()` has gained one more entry — what the subject's own PREDICATE
reads (`trigger/edge.go`) — which narrows the gap without closing it: the action's reads, and so
every path read, are still not in the list.

That one list is load-bearing twice over — it is both what a scoped engine opens a
`WatchSubtree` on (`trigger/engine.go`) and what the commit skip is asked about
(`Trigger.touches`). Its own comment says why they must be the same list: *"a trigger's verdict
can only change when something it reads does."* A path read is a thing the rule reads that the
list does not mention, which is exactly the staleness.

## The proposal

Say it in the tag. A read spelled `!view.*` goes into `reads()`; a read spelled the way it is
spelled today does not.

```tony
- name: pr-branch
  condition:
    when: {path: "change.pr.*"}
  action:
    derive:
      fact.branch.$subject.name:
        # copies once, at the firing that caused it
        head:   !get-path      {at: [git, repo, $subject.value.head], path: sha}
        # tracks: this rule now wakes when git.repo does
        branch: !view.get-path {at: [git, repo, $subject.value.head], path: branch}
```

The prefix is orthogonal to the cardinality suffix on purpose. `path.go` argues that telling
the two apart *by name* rather than by what the address happens to contain is the point — "a
value slot takes one thing, and 'this is a list' should be legible in the charter rather than
depending on whether some id turned out to hold a `*`". Tracking is a second, independent axis,
so it reads as a prefix and leaves the cardinality where it was: `!view.get-path` answers with
one node, `!view.list-paths` answers with a list.

### Sketch

```go
// path.go — two more tags, and one more thing a pathSpec knows about itself.
const (
	GetPathTag       = "get-path"
	ListPathsTag     = "list-paths"
	ViewGetPathTag   = "view." + GetPathTag
	ViewListPathsTag = "view." + ListPathsTag
)

type pathSpec struct {
	kind         string // GetPathTag or ListPathsTag — cardinality, unchanged
	view         bool   // ...and whether it tracks
	System, Kind string // still always literal
	ID           string // still literal, a $reference, or (list only) a glob
	Path         string
}

// pathKind keeps answering with the cardinality, so every existing caller —
// validate, resolveRead, checkLevels — is untouched by the new spelling.
func pathKind(n *ir.Node) string {
	switch tagName(n) {
	case GetPathTag, ViewGetPathTag:
		return GetPathTag
	case ListPathsTag, ViewListPathsTag:
		return ListPathsTag
	}
	return ""
}
```

```go
// edge.go — the whole behavioural change.
func (t Trigger) reads() [][2]string {
	// ... on, when, subject as today ...
	add(t.Action.viewSubtrees())
	return out
}

// viewSubtrees is readTargets over the action, kept to the tracking spelling. A
// subtree is knowable at install even when the entity is not: validate() already
// forces system and kind literal, so `id: $subject.value.head` costs nothing here.
func (a Action) viewSubtrees() [][2]string {
	var out [][2]string
	for _, p := range a.allPathReads() { // readTargets over patch, derive, seq, hooks
		if p.view {
			out = append(out, [2]string{p.System, p.Kind})
		}
	}
	return out
}
```

That is the extent of it, and the reason to write the note: **nothing downstream is new**, in
either dispatch mode, because `reads()` is the pivot in both.

- Under the root dispatch loop the engine already sees every commit, and `Trigger.touches` is
  what declines to evaluate on the ones that changed nothing the rule reads. Adding view targets
  to the list un-skips exactly the commits that touch a viewed subtree.
- In subtree (scoped) mode, `watchTriggerSubtree` opens one `WatchSubtree` per entry in
  `reads()`. Adding view targets opens exactly the watches a view needs.

The re-run is then `evaluate`, which re-resolves the action against the new snapshot through the
`resolveRead` that already exists. A view is not a second evaluation model living beside the
reactive one — it is the reactive one, told about a read it was not being told about.

Two properties fall out rather than being built:

- **A recompute to the same bytes is free.** Writing an unchanged value produces no commit
  ([change-events.md](../../change-events.md)), so an input that changes in a way the view does not
  observe costs one evaluation and stops. No dirty-tracking is needed to avoid churn.
- **Runaway is already bounded.** A view that does churn is a rule firing repeatedly, which is
  what [the cycle throttle](../../cycle-throttle.md) catches at runtime.

## What is not settled

1. **Fan-out is the real cost, and this design does not price it.** Because the id may be
   `$subject.value.head`, a commit on `git.repo` does not say *which* subjects read it — so the
   rule re-evaluates over its whole selection. That is what per-commit evaluation already does,
   but a view makes rules wake on subtrees they merely read, and the number of (rule, commit)
   evaluations grows with the fan-in. Whether subtree watches suffice or a view needs per-entity
   dependency records is the first thing a concrete use case should be made to answer.
2. **Derive and the level check — likely free, but this is the claim to verify.** `checkLevels`
   already forces a path read inside a `derive` to resolve at or below the rule's own stratum,
   precisely so the levels above cannot influence what a derive writes. A view there therefore
   wakes on a level at or below and cascades upward, the direction the ordering already allows,
   and it looks like it needs no new check. Derive inertness is what the whole ordering rests
   on, so "looks like" is not good enough: this wants a test before it wants an implementation.
3. **Absence probably means something different.** `!get-path` fails the firing when nothing
   stands there, on the argument that writing null would launder "nothing is there" into
   "something is there, and it is nothing". For a *view*, not-there-yet is an ordinary state that
   may later become there, so failing may be the wrong default and holding may be right. Unclear,
   and it is the kind of thing only a use case settles.
4. **Whether the answer is a view at all.** Event-driven may be good enough, and the honest
   version of this note is that it proposes a spelling for a thing nobody has yet needed twice.

## See also

- [`!get-path` and `!list-paths`](../../trigger-model.md#get-path-and-list-paths--reaching-another-entity)
  — the grammar this extends
- [Strata](../../strata.md) — the level rule a derive's reads answer to
- [Convergence](../../convergence.md) — why a firing that rewrites the same bytes ends
