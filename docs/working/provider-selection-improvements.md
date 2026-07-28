# Provider selection: what a doer still cannot be

Status: proposals. Nothing here runs. What *does* run — a selection binding a doer's driver,
frame, tools and grant, under a ceiling the charter states — is
[provider selection](../provider-selection.md), and this note assumes it.

Three extensions, related by one question: **what more can a doer be, without any of it
becoming a way to widen what the charter granted?** The ceiling discipline is the constraint
every proposal here has to satisfy, and it is why they sit in one note rather than three.

They are independent of each other and are listed by how much they change. The human row is
the one that makes the feature more than a refactor; the tool expressivity is the one with no
workaround today; the vector ordering is the smallest and the least certain.

## 1. Selecting a human

**The gap.** A human *is* a provider. The dial-in broker already runs a process a person
executes ([process control](../process-control.md),
[tutorial §5](../tutorial.md#5-human-executed-processes-dial-in)), so "this finding needs a
person" ought to be a selection whose winner happens to be human — over the same votable,
attributable surface as every other who-question — rather than a gate, or a separate rule
whose whole job is to route to people.

It isn't reachable, because a brokered doer is not a driver name. The runner is a property of
the *verse*, not of a spawn: `versed.Server.runner` is a single field set once, the `Runner`
interface's `Spawn(Launch, ContentReader)` takes no selector, and the runner reaches the
process entity only as `mirror.Meta.Runner`. A provider entity can say `driver: broker` and it
will mean nothing.

**Two shapes.**

*A runner axis on the launch spec.* `runner:` becomes a field a fragment can set, resolved
against a registry of runners the way `grant:` resolves against `namedGrants`. Symmetric with
every other axis, and it makes "which substrate" a first-class property of a doer, which is
what it is. Costs the Server a runner registry where it currently holds one runner, and a
resolution step in the spawn path.

*A driver that resolves to a brokered handle.* `driver: broker` returns a handle from the
broker rather than the local runtime. Much cheaper, and `Runner` stays a property of the
verse. Pays for that by making "which substrate" a driver name — which is precisely the
conflation the fragment work just undid one axis over, reintroduced one axis further down.

The first looks right for that reason. But it is the larger change and the human row is still
speculative, so the second's smallness is a real argument while nobody has written the charter
that wants this.

**What it forces either way.** A human doer has an envelope too. `grant` and `tools` mean
something different when the executor is a person — a tool the broker does not offer is a
tool the person is not asked for, and a grant is a statement about what they may do on the
verse's behalf. Neither the ceiling rule nor the overlay needs changing for that, which is
mild evidence the shape is right.

## 2. Tools: required, optional, excluded

**The gap.** `tools` is one flat allow-list on both sides, intersected. Two things it cannot
say, and the second is the common one:

*A doer that is nearly useless still launches.* Spawn offers `[read_file, write_file,
run_bash]`, doer offers `[read_file]`, the intersection is `[read_file]`, and an agent told
"apply the fix, build, run the named test" starts work it cannot finish. Eligibility has no
opinion, because the intersection succeeds.

*Exclusion is inexpressible.* Because an empty list means *all* of the driver's tools, a spawn
that wants "whatever this driver normally offers, minus `run_bash`" would have to enumerate
the driver's entire tool set — which it cannot know, since the full set lives at the driver
and is not resolved until `launchOf` hands the launch over.

**The shape.** A spawn declares three sets rather than one, and a doer's tools are matched
against all three:

```
final    = (spawn.allow ∩ doer.allow) \ (spawn.exclude ∪ doer.exclude)
eligible ⟺ spawn.required ⊆ final
```

Allows intersect, excludes union, so both sides can still only subtract and nothing about the
composition can widen. `required` is the only non-narrowing element and it is a *filter* on
eligibility rather than a contribution — it can refuse a doer, never grant it anything.

`optional` is the field that exists today (the ceiling). A doer wants `exclude` as well as
`allow`, for the same reason the spawn does: a doer whose `tools` is empty (= all) has no
other way to say "I never shell out".

**Why `required` is more than convenience.** It turns an attribute convention into a
structural fact. The routing charter today matches `risk: low` as a stand-in for "this doer
can apply a fix"; with required tools the spawn states what the *work* needs and the registry
states what each doer *has*, and eligibility becomes a capability question. The fixer
qualifies because it offers `write_file` and `run_bash`, not because someone remembered to tag
it.

**Install-time invariants**, in the same place and for the same reason as the grant
requirement — a charter that cannot mean what it says fails where its author is present:
`required ∩ excluded = ∅`, and `required ⊆ allow` when `allow` is non-empty.

**What it does not close.** `required ⊆ doer.tools` does not imply the doer can do the work. A
doer can offer `write_file` under `grant: read-only` and be refused the moment it uses it —
`launch.Spec.Tools` already notes that a tool offered-but-ungranted costs a turn being
refused. Making the two axes agree means deciding whether `read-only` permits `run_bash`,
which is a question about derived effects rather than names: the capability-derivation
frontier [authority](../roadmap/authority.md) is parked at. Required tools tighten selection
without touching that, and should say so rather than appear to have solved it.

**Mechanics.** Keeping `tools:` as the allow list and adding two siblings is non-breaking and
needs no codec work. Folding it into `tools: {need, allow, deny}` reads better and costs a
custom codec to keep the plain-list form working — `Condition` already carries `codec=custom`
for a similar union, so there is precedent, but not a reason on its own.

**Staging.** `excluded` earns its place first: it is the shape charters want most often and
the only one with no workaround at all. `required` is worth doing when the second real routing
charter makes the `risk:`-style convention creak — which is the signal that eligibility is
being asked to answer a capability question through a proxy.

## 3. May a doer tighten its own supervision?

`vector` is refused on a provider entity today, and the reasoning is one-directional: it is
the one axis that widens without touching authority, since `vector: auto` means "this firing
already answered its gates" and would take every action boundary off the docket while the
grant looked untouched. Whether a person sees an action is the charter's call.

But the argument only rules out *loosening*. A doer that wants to run at `ask` under a
charter's `auto` — a doer that asks for more supervision than it was given — is narrowing, and
narrowing is what the whole overlay permits. That needs an ordering over `auto | ask | park`
and a rule that a doer may only move toward `park`.

Two reasons it is not obviously worth having. The ordering is not quite total in meaning —
`park` is not "more supervised than `ask`" so much as differently supervised — and a `Vector`
is more than its `Default` once real gates are in it, so "narrower" over full vectors is a
harder claim than over the three names. And nobody has asked. Listed because the refusal in
the code states a one-directional reason for a two-directional rule, which is the kind of
asymmetry worth revisiting deliberately rather than by accident.

## Smaller open items

- **Predicate subsumption in `Grant.Subset`.** Sound-but-incomplete today (identity, or a
  wildcard parent). It costs nothing yet and cannot be made to cost anything, because a doer's
  grant is a *name* from a four-name vocabulary built out of shared per-verb atoms. A
  structured grant and real subsumption arrive together or not at all.
- **A grant name is resolved twice** — by the engine to check the ceiling, by `versed.launchOf`
  to build the launch. Harmless; consolidating would put the vocabulary in one place, which is
  what `launchOf`'s own comment argues for.
