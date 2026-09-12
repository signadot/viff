# Directed gates: who a question goes to, and who may answer it

Status: proposal, nothing here runs. It stands on [perms.md](../perms.md), which does run, and
takes up git-issue `m57mw5bmh12krmcmh5n0` (gates should be directable to users). Two of its
pieces move the charter grammar and the binding record, which is why the spellings are written
down here before anything is built.

The question it answers: how does a charter say that only davido may approve a gate — and then,
since the same machinery has to carry it, how does davido hand the question to a group, and how
does a question posed to several people mean *both* rather than *either*.

## What stands in the way today

A charter gate poses a question at `verse.gate.question.<id>`. Who may answer is what the
check admits `read-write` on that record: the charter's owner and the system, as co-owners
through `under`, and grantees. The grantees today come from one place, the seed's row on
`verse.gate.question` holding out `read-write` to every user, and that row reaches every
question because a question's record carries no owner of its own to stop it. So every person
may approve every gate, and no spelling in a charter says otherwise. A binding's `method` is a
service behind a url; nothing routes a question to a person.

Two things are already right and are kept as they are:

- **One or many is a threshold over a set.** A question's options each carry `need`, ballots
  are one per answerer, and a second ballot by one answerer replaces the first. With answerers
  `[A, B]`, `need: 1` is a race and `need: 2` is both; anything between is a quorum, which is
  what `select: vote` already means. Nothing about tallying moves. What is new is only the
  *set*, and the set is the grant on the question's record.
- **Escalation is a ladder of bindings.** A binding hands a question on, `resolver.Next`
  picks the next binding covering the subject, `reown` moves the grant and the clock with it,
  and a binding with nothing above it lands the question at the docket, ⊤. A rung is a
  binding, one question is owned by one rung at a time, and the order is the layer rule's.

## Vocabulary

A **binding** is the record at `verse.resolvers.<kind>.<name>`: a condition over subjects (the
standing door), `byRef` (the named door), a policy — `exclusive`, `deadline` — and who answers.
Who answers is the **resolver**, and it is one of three:

| `method.driver` | who answers | how they answer |
|---|---|---|
| `url` | a service | dials in through its instance, posts verdicts |
| `person` | one principal | from the docket, `verse gate approve` |
| `group` | a group's members | each as themselves: a person from the docket, a service through an instance of its own |

"People as bindings" is the wrong phrase, then: a binding binds a resolver to subjects, and a
person is one kind of resolver.

## The five pieces, and groups

### 1. A question's record is an owner record

The mirror writes `owner: system` on every question's record beside `under`. The seed's row
then stops at it, as a grant stops at any owner record, and what a question holds out is
decided where its record is written, like any other node: the default on `verse.gate.question`,
copied at pose.

```tony
# verse.perms.node."verse.gate.question"
grants: []
default: {read: ["*"], read-write: [user, agent]}     # the open verse's docket
```

A private verse narrows that one default. This is the umask design applied to the docket and
it is the piece everything below needs: without it, a grant to davido sits beside a grant to
everybody.

### 2. Answerers on a gate

```tony
gates:
- name: approval
  mode: ask
  need: 1
  answerers: [oscar@example.com]
```

`answerers` is a grantee list — a principal, a kind, `*`, a group's address — and the mirror
writes it as the question's `read-write` grant at pose, in place of the default. The gate's
`need` (per option, as today) is what one-or-many means over it. A gate naming no answerers
holds out the default. The spawn's own gates (a task's `vector.gates`) take the same field and
mean the same thing about the task's questions.

### 3. A person, or a group, as a binding's resolver

```tony
name:      payments-owner
kind:      approvals
condition: {subject: {path: [change, pr, *], value: {area: payments}}}
method:    {driver: person, who: oscar@example.com}
exclusive: true
deadline:  {within: "4h", then: escalate}
```

The standing door, `exclusive`, the deadline and the ladder all apply to davido exactly as to
a service. When a question is bound to this rung, the mirror writes `read-write` for davido —
the resolver, not the binding's address — on the question's record; for `driver: url` it goes
on writing the binding's address, since a service acts as the binding through its instance.
The issue's sketch, a person declaring which subjects they gate at `verse.gating.<user>`, is
this record spelled on the binding side, where the condition vocabulary and the layer rule
already live.

`{driver: group, group: verse.group.payments}` is the same with a set behind it; §groups says
what the set is.

### 4. A person escalates

```sh
verse gate escalate <id> --as oscar@example.com [--say "not mine: infra"]
```

The person's form of what a service does through its instance: the question moves to the next
rung covering its subject, with `reownedFrom` and `reownedWhy` on the record as today, and to
the docket when there is none. Admitted to whoever holds `read-write` on the question, since
handing a question on is an answer of a kind — *not by me* — and refused to anybody else.

A person silent past their binding's `deadline` escalates as a service does. The clock on the
record means one thing.

### 5. Refused at install

The way an exclusive binding under `need: 2` should be and is not yet
([gate-unify-residual.md §4](gate-unify-residual.md#4-what-is-not-refused)):

- `need` above the number of answerers that could ever cast a ballot — a gate with
  `answerers: [davido]` and `need: 2` — is a question nobody can settle;
- an answerer that names no listed principal, no kind, no group and no binding is a typo, and
  a question held out to a typo is held out to nobody;
- a `person` resolver whose `who` is not listed; a `group` resolver whose group does not exist.

### Groups

A group is a named set with an owner, a node like anything else:

```sh
verse group add payments oscar@example.com ana@example.com verse.group.security
```

writes `verse.group.payments` with its members, owned by whoever made it and set through the
same verb, readable by its members. **As a grantee, the group's address admits its members**,
the way a binding's address admits whoever acts as the binding: one vocabulary, a grantee is a
principal, `*`, a kind, or an address, and an address admits whoever is *behind* it. Members
may be principals, kinds, other groups, or bindings, walked to a bounded depth so that a cycle
admits nobody rather than everybody.

Two things a group is not. It does not order: two rungs to be tried in turn are two bindings,
ordered as the layer rule orders them, and one rung to be answered by several at once is one
binding over a group. And it is not a copy: **membership is read at check time**, since a group
is for changing who is in it without touching every place it is granted. That makes it the one
grantee whose meaning moves after the fact, which is the point, and is worth saying beside the
rule that a grant withdrawn touches nothing that exists.

**Escalating to a set of bindings** is therefore escalating to one binding whose resolver is a
group whose members are bindings. Each service member gets an instance minted for it, the way
a named binding does today, so it can read the run and post through it; the people answer from
the docket; the question's `need` decides how many of them it takes.

## Two choices that are the model's, said plainly

- **The charter's owner co-owns the question.** "Only davido" always includes the author of
  the rule, and the system. The model says so on purpose: the party accountable for a domain
  is a grantee, and the author is not shut out of what their own work asked. A gate that must
  exclude its author belongs to a charter installed by somebody else, or shipped as the
  system's.
- **`exclusive` is whose answer counts, not who may answer.** It stays as it is. A person
  answering over an exclusive rung says `--override` and the record names both; a person who
  holds nothing on the question is refused before that question arises.

## Costs

- An owner record per question, where today a question's record carries only `under`. One
  small record either way.
- A group's membership is a read per check, through the same cache the records use; a group
  of groups is that many reads. Bounded by the depth.
- A group of service bindings mints an instance per service per question. That is the
  fan-out's real price and the only new one.
- The charter grammar moves (`answerers`), the binding record moves (`method.driver`), and a
  verb and a group record are new: `contract` tier, a `Breaks:` line, and a notice.

## Order of work

Each step leaves the tree green; the first is the only one nothing visible depends on.

1. The question's owner record and the default on `verse.gate.question` (§1), with the seed's
   row becoming that default. *Proves:* every existing answer path still answers, through the
   copied default rather than the row above.
2. `answerers` on a gate and on a spawn's gates, written as the grant (§2), and the install
   refusals about `need` and unknown answerers (§5). *Proves:* davido answers, bob is refused,
   the author still answers; `need: 2` over `[davido, ana]` waits for both.
3. `driver: person` (§3) and `verse gate escalate` (§4). *Proves:* a standing person-binding
   takes the questions its condition covers; davido escalates to the next rung; silence past
   the deadline escalates.
4. Groups (§groups) and `driver: group`. *Proves:* a group admits its members and nobody else;
   a group of bindings fans out with an instance per service; a cycle admits nobody.
5. Land: [perms.md](../perms.md) gains the docket's default and groups,
   [reference/resolvers.md](../reference/resolvers.md) the two drivers,
   [trigger-model.md](../trigger-model.md) the gate field; this note goes; what is left goes to
   [grants-residual.md](grants-residual.md).

## What this note does not do

- **Carry gating through derivations.** The issue's second paragraph: a derived entity
  inheriting the answerers of the subject it was derived from. Nothing here forbids it and
  nothing here builds it; it is a question about `derive`, and belongs with strata.
- **Verify anybody.** A person is still self-asserted; a group of self-asserted names is a
  self-asserted group. The model's stance on that is unchanged and is what makes a verified
  principal a drop-in later.
