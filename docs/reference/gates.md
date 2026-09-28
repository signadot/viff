# The gates of a rule

This page describes the `gates` field of a [rule](../glossary.md#rule). It says what a gate
asks, who answers it, and what a refusal, a deadline and a restart do to the question. The
model that gates belong to is in [the trigger model](../trigger-model.md).

## `gates` — whether it proceeds

`gates` is a list of [gates](../glossary.md#gate-of-a-rule). Every gate must resolve before the
action fires. An empty list fires with no approval. Each gate is
`{ name: …, mode: auto|ask|park }`.

Gates are per *firing*. A [firing](../glossary.md#firing) is about one entity, so a rule that
selects twelve PRs asks twelve [questions](../glossary.md#question).

The id of a question is a function of (rule, gate, subject). So a rule that is evaluated again
asks once. A firing that finds its question already standing joins it, and does not post a copy.
One answer releases every firing that waits on it. So a rule that waits on a person does not
fill the [docket](../glossary.md#docket).

Several gates model several approvals. A deploy that needs a sign-off from security and from a
release manager is two `ask` gates. A single gate that takes more than one voice says so on the
question it poses:

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

### What a question says

The record of a question holds `about` beside the gate's own fields. `about` is the one line
that the subject says about itself. Verse copies it onto the question when it poses the question.
An entity opts in to this by carrying a `says`
([the question record](resolvers.md#the-question-record)). With `pose` and `about`, a question
says what is asked and what it is about. A reader needs no grant on the subject to read either.

`pose` is the question in words. It is the only field of a gate written for a person, and not
for the engine. The other fields say who asked and how the question settles.

Verse expands `pose` at the firing, against the subject that the firing settled on. `$[…]` is
[go-tony's expression](https://github.com/signadot/tony-format). Only `subject.name` and
`subject.value` are in scope. The question carries the result as words.

A sentence that does not expand does not stop the question. Verse drops the words, and a reader
falls back to `reason`.

### Options and their thresholds

Every gate poses the same record:

- the options it can settle on;
- for each option, the number of ballots that settles it;
- a deadline, with the option that the question falls to.

A gate that says none of these poses `proceed` and `deny`, at one ballot each.

| Field | What it says |
|---|---|
| `says` of an option | what choosing the option *means*, for whoever is asked. The `id` is how an answer spells the option, and the name the rule reads. |
| `need` | the short spelling of `options: [{id: proceed, need: N}, {id: deny, need: 1}]`. Do not write `need` and `options` together. |
| `options` | an ordered list. The first option to reach its threshold settles the question. |

A gate that carries any of these, or a `pose`, and no `mode` has the mode `ask`. Verse refuses
such a gate with `mode: auto`, because `auto` asks nobody.

### Who may answer

`answerers` names who the question is held out to. It holds grantees, in the
[grammar](../perms.md#principals-and-grantees) that a grant uses:

| Grantee | Who |
|---|---|
| `user:<email>`, or the bare email | a person |
| `user:*` | a whole kind |
| `*` | everyone |
| `group:<name>` | a group |
| `via:verse.resolvers.<name>` | a resolver |

Verse writes `answerers` as the `read-write` grant of the question when it poses the question.
It replaces the default of the docket. Each answerer has one ballot. `need` over the answerers
says how many must answer:

- `need: 1` is a race;
- `need` equal to the number of answerers is all of them.

The owner of the charter co-owns what its rule asked, and answers it without being named.

Verse refuses at install a gate that nobody could settle:

- a threshold above the number of answerers named;
- an answerer that the grammar does not read;
- a group or a resolver that does not stand.

[perms.md](../perms.md) has the record that `answerers` becomes.

### The tally

The answerers keep the tally, and the engine does not.

- An answer is a ballot on the record.
- The question settles in the same write as the ballot that reaches a threshold.
- An answerer who casts twice replaces their own ballot. The ballot does not count twice.

The engine asks exactly what was written, and acts on the one answer that comes back.

Waiting for a fact is not a gate. A rule that acts once CI is green says so in `when:`.

### `onDenied` — what a no means

A denied firing writes nothing. A firing that did not happen leaves no trace. So the subject
stays in the exact state that asked the question. The next commit that touches the subject
selects it again, and the rule fires again. The gate poses the question again, under the same id,
to somebody who already answered it.

The [latch](../glossary.md#latch) of a well-formed rule cannot help. The latch is in the action,
and on a deny the action never runs. So the gate says what its own refusal means:

```tony
  gates:
  - name: approval
    mode: ask
    onDenied: { patch: { stage: rejected } }
```

`onDenied` is an ordinary action: `patch`, `derive`, `spawn`, its own completion hooks. Verse
applies it about the same subjects that the firing was about. It is a field of the *gate*, not of
the action. It is not a completion hook, and it needs no spawn of its own.

Only a committed refusal runs `onDenied`. It applies to a no however the no arrives: from one
person, from a threshold of people, or from the default of the clock. It does not run when:

- an answerer could not be reached;
- a question was withdrawn because its subject left the selection.

In those cases nobody said anything. A rejection written for them would put a decision in the
store that nobody made.

A gate may omit `onDenied`. "Ask again the next time somebody touches it" is a real policy. So a
gate without `onDenied` gets an advisory at install, and not a refusal. The install prints the
advisory under the line that says what was installed, as `note: rule <name>: …`.

To make a no final without changing the state of the subject itself, use a marker:

```tony
  condition:
    subject: {path: "demo.pr.*", value: !and [ { stage: open }, !not { blocked: true } ]}
  gates:
  - { name: approval, mode: ask, onDenied: { patch: { blocked: true } } }
```

Removing the marker then opens the question again.

### A deadline settles

`deadline: {within: "24h", default: deny}` settles the question on `deny` when the time passes
and no option is at its threshold. The question records `by: deadline`, and the firing sees a
refusal.

Verse stamps the instant on the record once, when it first poses the question. Every later
posing uses that instant. A restart arms the clock against the stamped instant, and not against a
new duration. So a deadline that passed while verse was down fires as verse starts again.

The default is an ordinary option. A ballot for it counts toward its own threshold, like any
other ballot. So a `deny` that needs one ballot settles on one deny, before the clock.

A default left empty settles on nothing (`verdict: none`). The firing reads that as a refusal
that nobody made.

### A question outlives the verse that asked it

The record, its options and every ballot cast into it are entities. So a restart finds the
question exactly where it stood.

The *awaiter* does not survive a restart. The awaiter is the part of the firing that waits for
the answer. A question with no awaiter is the worst kind of entry on the docket: it lists a
decision, somebody makes it, and nothing moves.

Startup gives the awaiter back. The route depends on what asked the question:

| Question of | How startup gives the awaiter back |
|---|---|
| a **gate** | The question blocks the firing itself, and the action never ran. So the firing comes back by being *evaluated again*. A rule that still selects the subject poses the same question again, and joins the record already standing. The ballots already cast still count. The firing is evaluated again, not resumed. |
| a **selection** (a `spawn` with a `provider`) | The question opens *after* the action has written its latch, so nothing fires it again. The question records the `chain` of the action it blocks. Startup follows that chain back into the charter, and enters the selection again. The selection poses the same question again. A resumed task restores its hooks from the same lookup key. |

Verse *settles* a question that nothing can answer any more, and does not leave it standing.
This happens when:

- its rule left the charter;
- its action moved;
- its subject is gone;
- **the subject moved out of what the rule selects.** This case is easy to miss.

For example, a merge proposal set to `state: stale` is not selected by a rule that reads
`!not.has-path state`. That rule never looks at its gate again, however healthy the rule is.

Verse stamps the record `status: orphaned`, with a `why`. The record keeps the trail, and leaves
the list of things a person can act on. This is not a verdict, because nobody decided anything.
So a rule that selects the subject again poses the same question again, over the same ballots.

Posing again is idempotent by construction, so it can run beside a rule that also fires again:

- the question id is derived from the rule, the gate and the subject;
- a question settles once;
- the spawn id is deterministic per (chain, subject).

One question, and one task, however many paths reach it.

**A scope (`verse connect`) is not covered.** Its questions are in the scope's own store,
and nothing lists them at startup. Scoped charters and scoped claims have the same gap.

**When verse stops between settling a question and starting the work it decided, the spawn
is lost.** The record is answered, so nothing poses it again. The same window exists between
`patch` and `spawn` inside one action. An action is not atomic. A crash in the middle of one
leaves the half that landed.

### A gate's answer applies to the world it was asked in

While a question stands, the firing watches the subtree of the subject. At every change it asks
whether the rule would still select the subject. When the rule would not, the question is moot,
and verse **withdraws** it:

- the record says `status: orphaned`, with a `why`;
- there is no verdict and no action;
- `onDenied` does not run, because a moot question is not a refusal.

The ballots stay on the record. A rule that selects that subject again poses the same question
again, over them.

An answer that arrives after the subject left the selection is the same case. The firing
withdraws, and does not act.

**The withdrawal is prompt.** The firing watches the subtree of the subject while it waits. So a
question goes moot at the change that made it moot. Nobody has to answer, and nothing has to
restart.

The check has two limits.

- It asks only about the *subject*, not the cause. A rule fired by an `on` edge may still select
  its subject and never see that edge again. So a question that stands does not promise that the
  rule will fire again.
- When it cannot tell, it lets the question stand. It cannot tell when the store is unreadable,
  or when a read of the predicate fails.

### Who answers a rule's own gates

```tony
gating:
  resolver: verse.resolvers.security-review
  gates:    [{name: ship, mode: ask}]
```

`gating` groups the two facts about the gates of a rule: which gates they are, and who answers
them. `gates:` at the top level means `gating: {gates: …}`. Verse refuses a rule that writes
both, and does not merge them.

`resolver` names an installed [resolver](../glossary.md#resolver) (`verse resolver add`). A spawn
uses the same word for the same act. A spawn names who answers the run it starts. A rule names
who answers the gates it raises itself.

With `resolver` empty, the question goes to whatever standing resolver matches its subject, and
then to the docket that a person reads. Verse refuses a resolver that is not installed. It does
not leave the question to a resolver that cannot answer it. See
[attached resolvers](../attached-resolvers.md).

## Why it is this way

The reasons for this design are in [reference-gates.md](../design/reference-gates.md), in the repository.
