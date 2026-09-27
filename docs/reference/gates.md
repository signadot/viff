# The gates of a rule

This page holds the `gates` field of a rule. It says what a gate asks, who answers it, and
what a refusal, a deadline and a restart do to the question. The model that gates belong to
is in [the trigger model](../trigger-model.md).

## `gates` — whether it proceeds

A list of gates, all of which must resolve before the action fires. Gates are per *firing*, and a
firing is about one entity, so a rule selecting twelve PRs asks twelve questions. Each is
`{ name: …, mode: auto|ask|park }`. An empty list fires with no approval.

The question's id is a function of (trigger, gate, subject), which is what makes a re-evaluated
rule ask once. A firing that finds its question already standing joins it rather than posting a
copy, and one answer releases everyone waiting. A rule waiting on a human is therefore not a rule
filling the docket.

Multiple gates model multi-approval: a deploy needing both a security and a release-manager sign-off
is two `ask` gates. One gate that takes more than one voice says so on the question it poses:

```tony
  gates:
  - name: approvals
    pose: "Deploy *$[subject.value.title]* ($[subject.value.changes] changes) to staging?"
    need: 2                                    # proceed takes two ballots; one deny refuses
    deadline: {within: "24h", default: deny}   # and the clock decides silence
    answerers: [oscar@example.com, group:payments]   # and whose ballots
  - name: route
    options:
    - {id: ship, need: 1, says: "merge it now"}
    - {id: hold, need: 2, says: "wait for the release call"}
    - {id: drop, need: 1}
```

Beside it on the record stands `about`: the one line the SUBJECT says about itself, which verse
copies onto the question when it poses it — a convention an entity opts into by carrying a `says`
([the docket has shown it since `06476112`](resolvers.md#the-question-record)). Between
them, a question says what is being asked and what it is about without a reader holding any grant
on the subject at all.

`pose` is the question in WORDS, and the only thing on a gate written for a person rather than for
the engine: everything else says who asked and how it settles, and a rule name beside an address is
not what somebody deciding needs to read. It is expanded at the firing, against the subject that
firing settled on — `$[…]` is [go-tony's expression](https://github.com/signadot/tony-format), with
`subject.name` and `subject.value` in scope and nothing else — and it rides the question as words.

Expanded there rather than where it is shown, for three reasons in order of weight: a presenter may
hold no grant on the subject, while the engine is already reading it to fire; the sentence then
says what the rule SAW, rather than what the subject looks like by the time somebody reads it; and
it is read once rather than once per presenter. A sentence that will not expand does not stop the
question — the words are dropped and `reason` is what a reader falls back to, because a gate nobody
is asked because its prose broke is worse than a bare one.

Every gate poses the same record: the options it can settle on, each with the number of ballots
that settles it, and a deadline with the option it falls to. A gate that says nothing poses
`proceed` and `deny` at one each, which is the question a gate has always asked. An option's
`says` is what choosing it MEANS, for whoever is being asked: an id is what an answer is spelled
with and a name the rule reads, and a question whose options are three ids is one answered by
guessing. `need` is the
short spelling of `options: [{id: proceed, need: N}, {id: deny, need: 1}]`, and the two are not
written together. `options` are ordered: the first to reach its threshold settles the question.
A gate that carries any of these, or a `pose`, and no `mode` is `ask`; with `mode: auto` it is
refused, since auto asks nobody.

`answerers` is who the question is held out to: grantees in the
[grammar](../perms.md#principals-and-grantees) a grant names them in — a person (`user:<email>`, or
the bare email), a whole kind (`user:*`), `*`, a group (`group:<name>`), a resolver
(`via:verse.resolvers.<name>`). It is written as the question's
`read-write` grant when the question is posed, in place of the docket's default, and `need` over
it is what one-or-many means: one ballot per answerer, `need: 1` a race, `need` equal to their
number all of them. The charter's owner co-owns what their rule asked and answers it without
being named. A gate nobody could settle is refused at install: a threshold above the answerers
named, an answerer the grammar does not read, or a group or a resolver that does not stand. [perms.md](../perms.md) has the record it becomes.

The tally is whoever answers' to keep, not the engine's: an answer is a ballot on the record,
the question settles in the same write as the ballot that reaches a threshold, and one answerer
casting twice replaces their own ballot rather than counting twice. What the engine owes is to
ask exactly what was written, and to act on the one answer that comes back.

Waiting for a fact is not a gate. A rule that acts once CI is green says so in `when:`.

### `onDenied` — what a no means

A denied firing writes nothing: a firing that did not happen leaves no trace of having
happened. The consequence is that the subject is left in the exact state that asked the
question, so the next commit that touches it re-selects it, the rule fires again, and the gate
is re-posed under the same id — to somebody who already answered it.

The latch a well-formed rule carries cannot help, because it lives in the action and on a deny
the action never runs. So the gate says what its own refusal means:

```tony
  gates:
  - name: approval
    mode: ask
    onDenied: { patch: { stage: rejected } }
```

It is an ordinary action — `patch`, `derive`, `spawn`, its own completion hooks — applied about the
same subjects the firing was about. It hangs off the *gate* rather than the action, because a no is
the gate's outcome: two gates refuse for different reasons, and each says what its own refusal
means. It is not a completion hook and needs no spawn of its own.

Only a committed refusal runs it. An answerer that could not be reached, or a question withdrawn
because its subject left the selection, said nothing at all, and writing a rejection on their
behalf would put a decision in the store that nobody made. It applies to a no however it arrives:
one person's, a threshold of them, or the clock's default.

Omitting it is legitimate — "ask again next time somebody touches it" is a real policy — so a
gate without one earns an advisory at install rather than a refusal. The install prints it
under the line that says what was installed, as `note: rule <name>: …`. To say a no is final
without changing the subject's own state, the vanishing-marker shape works:

```tony
  condition:
    subject: {path: "demo.pr.*", value: !and [ { stage: open }, !not { blocked: true } ]}
  gates:
  - { name: approval, mode: ask, onDenied: { patch: { blocked: true } } }
```

Removing the marker is then what re-opens the question.

### A deadline settles

`deadline: {within: "24h", default: deny}` settles the question on `deny` when the clock passes
with no option at its threshold, as `by: deadline`, and the firing sees it as a refusal. The
instant is stamped on the record once, when the question is first posed, and honoured by every
later posing: a restart arms against the instant the question was given, not against a fresh
duration, so a deadline that passed while the verse was down fires on the way back up.

The default is an ordinary option. A ballot for it counts toward its own threshold like any
other, so a `deny` that needs one settles on one deny, before the clock. A default left empty
settles on nothing (`verdict: none`), which the firing reads as a refusal nobody made.

### A question outlives the verse that asked it

The record, its options and every ballot cast into it are entities, so a restart finds the
question exactly where it stood. What does not survive is the *awaiter*, which lives inside the
firing. A question with no awaiter is the worst kind of docket entry: it lists a decision,
somebody makes it, and nothing moves.

Startup gives the awaiter back, by the route the axis makes available:

- A **gate's** question blocks the firing itself, and the firing comes back by being
  *evaluated again*: its action never ran — that is what the gate is holding — so a rule that
  still selects the subject re-poses the same question, joins the record already standing, and
  the ballots already cast still count. Re-evaluated, not resumed, which is the distinction the
  next paragraph turns on.
- A **selection's** question (a `spawn` with a `provider`) opens *after* the action has
  written its latch, so nothing re-fires it. It records the `chain` of the action it is
  blocking, and startup navigates that chain back into the charter and re-enters the
  selection, which re-poses the same question — the same lookup key a resumed task's hooks
  are rehydrated from.

A question nothing can answer any more is *settled* rather than left standing: its rule left the
charter, its action moved, its subject is gone — or, the case that is easy to miss, **the
subject moved out of what the rule selects**. A merge proposal swept to `state: stale` is no
longer picked by a rule that reads `!not.has-path state`, so that rule will never look at its
gate again however healthy the rule is. The record is stamped `status: orphaned` with a `why`,
keeping the trail while taking it off the list of things a person can act on. That is not a
verdict — nobody decided anything — so a rule that selects the subject again re-poses the same
question over the same ballots.

The re-pose is idempotent by construction, which is what lets it run beside a rule that also
re-fires: the question id is derived from the rule, the gate and the subject, the settle is
terminal-once, and the spawn id is deterministic per (chain, subject). One question, one task,
however many paths reach it.

**A scoped verse (`verse connect`) is not covered.** Its questions live in the scope's own store,
which nothing enumerates at startup — the same gap scoped charters and scoped claims have.

**A verse that dies between settling a question and starting the work it decided loses that
spawn.** The record is answered, so nothing re-poses it. The resume does not introduce this: it
is the window `patch` then `spawn` already has inside one action. An action is not atomic, and
a crash in the middle of one leaves the half that landed.

### A gate's answer applies to the world it was asked in

A question is not answered in a vacuum. While it stands, the firing watches the subject's own
subtree beside it, and asks at every change whether the rule would still select the subject. When
it would not, the question is moot: it is **withdrawn** — `status: orphaned` with a `why`, no
verdict, no action, and no `onDenied`, because a moot question is not a refusal. The ballots stay
on the record, and a rule that selects that subject again re-poses the same question over them.
An answer that arrives after the subject left the selection is the same case from the other
side: the firing withdraws rather than acts.

This is not hypothetical tidiness. Without it, a merge proposal swept to `state: stale` while its
gate stands open leaves a question that watches only its ballots: it reaches its threshold on the
approval already cast, settles `proceed`, and merges a branch the sweep retired, losing the work
on it. A gate that can only be
answered about a world that has moved on is a gate answered by reflex, which is the failure gates
exist to prevent.

**The withdrawal is prompt.** The firing watches the subject's own subtree while it waits, so a
question goes moot at the change that made it moot, with nobody having to answer and nothing
having to restart.

One limit is worth knowing. It asks only about the *subject*, not the cause: a rule fired by an
`on` edge may still select its subject and never see that edge again, so a question that stands is
not a promise the rule will re-fire. And when it cannot tell — an unreadable store, a predicate
whose read fails — it lets the question stand, because a stale docket entry costs a reader a
moment while a wrongly withdrawn one takes a live question away from the people it is in front of.

### Who answers a rule's own gates

```tony
gating:
  resolver: verse.resolvers.security-review
  gates:    [{name: ship, mode: ask}]
```

`gating` groups the two facts about a rule's gates: which they are, and who answers them. `gates:`
at the top level means `gating: {gates: …}`, one field read two ways rather than two that can
disagree, and writing both is refused rather than merged, because a rule that says which gates it
has twice has said it twice.

`resolver` names an installed resolver (`verse resolver add`). It is the same word a spawn uses for
the same act: a spawn names who answers the run it starts, a rule names who answers the gates it
raises itself. Empty leaves the question to whatever standing resolver matches its subject, and then
to the docket a person reads. A resolver that is not installed is refused here rather than left to
own a question nothing can answer; see [attached resolvers](../attached-resolvers.md).
