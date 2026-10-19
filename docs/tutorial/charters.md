# [Tutorial](README.md) -- Charters

A [charter](../glossary.md#charter) is a list of [rules](../glossary.md#rule). A rule says:
when the [data](data.md) looks like this, ask [this](questions.md), and do [that](tasks.md).
Every rule has the same parts: a `condition` that selects the entities the rule is about, each
one its **subject**; `gates` that ask before it acts; and an `action` on the subject.

A charter is installed at an [address](../glossary.md#address), and its rules write only under
it. The tutorial's charter lives at `verse-tutorial`, where the tutorial's data lives, so
everything it makes is in one place and leaves in one command. `verse charter` reads a charter
back as `verse data` reads the store, and prints the file that installs it:

```sh
verse charter verse-tutorial     # the charter, as a file
verse charter                    # every charter, the baseline one first
```

This page goes in four steps, each one on from the last. The first rule changes data. The
second asks first. The third starts a task and writes its answer back into the data. The fourth
derives an entity of its own, which keeps its lineage, and one more rule reacts to it. Each is
installed from the text shown, on [a verse running](README.md#start), so what you read is what
runs. [Charters, continued](charters-2.md) has the rest of what a rule can say.

## 1. A rule that changes data

An issue is opened. The rule triages it.

```sh
verse charter -a verse-tutorial -f - <<'EOF'
rules:
- name: triage
  condition:
    subject: {path: "verse-tutorial.issue.*", value: {stage: open}}
  action:
    patch: {stage: triaged}
EOF
verse data verse-tutorial issue 1 -put '{stage: open, title: "login fails"}'
verse data verse-tutorial issue 1          # stage: triaged
```

The condition is a path with a wildcard and a pattern on the value. The action has no target of
its own: it patches the subject. An entity already standing when the rule is installed is acted
on too. A rule is a statement about the world, not a subscription to what changes next.

## 2. A rule that asks first

A release is ready, and shipping it matters. The rule ships what is ready while CI is green, and
only once Ana says yes.

```sh
verse charter -a verse-tutorial -f - <<'EOF'
rules:
- name: ship
  condition:
    subject: {path: "verse-tutorial.release.*", value: {stage: ready}}
    when:    {path: "verse-tutorial.ci.main", value: {state: green}}
  gates:
  - name: approval
    mode: ask
    answerers: [ana@example.com]
    deadline: {within: "4h", default: deny}
    onDenied:
      patch: {stage: held}
  action:
    patch: {stage: shipped}
EOF
verse data verse-tutorial release 1 -put '{stage: ready}'
verse data verse-tutorial ci main -put '{state: green}'
verse question --all
```

The question reaches the docket a moment after the write that raised it; an empty list means you
read too early.

```tony
- answerers:
  - user:ana@example.com
  deadline:
    default: deny
    within: 4h
  gate: approval
  id: verse-tutorial:verse.trigger.ship.approval~d584bf79
  origin: ship
  subject: 'verse-tutorial.release."1"'
  …
```

```sh
verse question verse-tutorial:verse.trigger.ship.approval~d584bf79 --answer proceed --as ana@example.com
verse data verse-tutorial release 1        # stage: shipped
```

`when` reads another address and makes the action conditional on it. The subject says what to
act on; `when` says whether this is a moment to act. Verse evaluates the rule again at every
commit that touches either address, so a release that becomes ready later is asked about too,
and one that is ready while CI is red waits.

The gate's question is an ask, as on [the questions page](questions.md): on the same docket, put
to the people in `answerers`, settled on `deny` if nobody answers in four hours. The rule asks
**once** per subject, however often verse evaluates it. `onDenied` says what a no means; without
it, the next commit that touches the subject asks again
([`onDenied`](../reference/gates.md#ondenied-what-a-no-means)).

### No is a real outcome

A gate names its options when "yes and no, one each" is not the question, and a deadline says
what silence means:

```tony
options: [{id: proceed, need: 2}, {id: deny, need: 2}]    # two rejects → deny
deadline: {within: "4h", default: deny}                    # the clock defaults it to deny
options: [{id: ship, need: 1}, {id: hold, need: 1}, {id: drop, need: 2}]   # not yes or no at all
```

Each answer is a ballot that names its answerer, and the record of the question says who chose
what. Quote the duration: unquoted, `4h` reads as the number `4` beside a literal `h`, and verse
refuses it at install ([a deadline settles](../reference/gates.md#a-deadline-settles)).

## 3. A rule that starts a task, and reads its answer

The issue from the first rule, and now something is done about it. The rule moves the issue to
`reviewing` and starts an agent that reviews it. When the agent is done, the rule writes what it
said onto the issue.

```sh
verse charter -a verse-tutorial -f - <<'EOF'
rules:
- name: review
  condition:
    subject: {path: "verse-tutorial.issue.*", value: {stage: triaged}}
  action:
    patch: {stage: reviewing}
    spawn:
      doer: {agent: verse, with: {network: {mode: open}}}
      prompt: "Review this issue: $title. Is it a bug or a feature, and how severe is it?"
      answer:
        kind:     !or [bug, feature]
        severity: !or [low, normal, high]
    onDone:
      patch: {stage: reviewed, kind: $data.kind, severity: $data.severity}
    onFailed:
      patch: {stage: unreviewed, why: $err}
EOF
verse task                                 # the task, while it runs, and its result once it is done
verse data verse-tutorial issue 1          # stage: reviewed, kind: bug, severity: high
```

The spawn names its **doer**: here `verse`, verse's own agent, with the host's network and a
`prompt` that is the one job. `$subject` and every field of the issue are bound in the action,
so `$title` in the prompt is the issue's title. The spawn names no policy, so the agent is given
no verb: it reads and answers, and cannot write a file, run a command or push
([the policy](tasks.md#the-policy)). A script is
`doer: {script: …}`, and the script is the work.

`onDone` fires when the task is done, and what it reads is the task: `$result` is the
[answer](tasks.md#the-answer) as text, and `$data` is the answer read as a document. So
`$data.kind` is what the review decided, and the patch writes it onto the issue as the first rule
wrote `stage`. From here on the answer is data like any other: `verse data` shows it, and a rule
that selects `{kind: bug}` fires on it without knowing that a task wrote it. That is how a task
connects to everything else in verse: through what it writes, not through a caller waiting on it
([the loop](../concepts.md#the-loop)).

An agent answers in words, and the rule reads fields. `answer` is the **contract** between them:
the shape the answer must have, written as a match pattern in the vocabulary of `when`. Verse
tells the agent the contract when it starts, and checks the answer when it stops. An answer that
misses is sent back with what was wrong, and the agent repairs it; one that never fits fails the
task. So `$data.kind` in `onDone` is never absent, and `onFailed` says what happens when the
task could not say it, with `$err` the reason
([the answer contract](../reference/action.md#the-answer-contract)). Only a doer that can be
asked again can carry a contract, so a script carries none.

The first patch is what makes this rule safe to leave installed. It moves the issue out of the
rule's own selection, so the rule does not fire on the same issue twice. Verse refuses at install
a spawning rule that does not do this ([the latch](../glossary.md#latch)).

The agent needs a model key ([the embedded runner](../reference/up.md#the-embedded-runner)).
Without one, the runner offers no agent, and verse refuses the rule when you install it:
`this runner offers no agent "verse"`. Nothing is installed, and the issue stays `triaged`. To go
on, write what the agent would have answered:

```sh
verse data verse-tutorial issue 1 -put '{stage: reviewed, title: "login fails", kind: bug, severity: high}'
```

## 4. Lineage and derivations

A rule may write an entity that is not its subject. That is `derive`, and because a charter that
writes anywhere can write in a circle, it comes with a declared order of levels, the **strata**:
a rule's subject is in its own stratum, and what it derives lands in a higher one.

```sh
verse charter -a verse-tutorial -f - <<'EOF'
strata:
- name: issues
  level: 0
  match: verse-tutorial.issue
- name: bugs
  level: 10
  match: verse-tutorial.bug
rules:
- name: raise
  stratum: issues
  condition:
    subject: {path: "verse-tutorial.issue.*", value: {stage: reviewed, kind: bug}}
  action:
    patch: {stage: raised}
    derive:
    - path: "verse-tutorial.bug"
      id: $subject.name
      value:
        about: $subject
        title: $subject.value.title
        priority: $subject.value.severity
- name: schedule
  stratum: bugs
  condition:
    subject:
      path: "verse-tutorial.bug.*"
      value: !and [{priority: high}, !not {scheduled: null}]
  action:
    patch: {scheduled: true}
EOF
verse data verse-tutorial issue 1          # stage: raised
verse data verse-tutorial bug 1            # about: verse-tutorial.issue."1", title: "login fails", priority: high, scheduled: true
verse data verse-tutorial bug 1 verse meta derivedFrom   # rule: raise, subject: verse-tutorial.issue."1"
```

The issue at `reviewed` with `kind: bug` is standing when this is installed, and the install
fires over it. The second rule fires on what the first wrote, a moment later. Read the bug
again if `scheduled` is not there yet.

A derived entity keeps its **lineage**: what it came from. The engine writes it at
`verse.meta.derivedFrom`, naming the rule and the subject, whatever the derived value says. So a
bug leads back to its issue, and a read over `derivedFrom` finds everything one rule derived.
`id: $subject.name` puts the bug at `verse-tutorial.bug.1`, under the name of the issue it is
about; without `id:`, the engine addresses it by the rule and the subject. Either way, no new id
is minted for it ([`derive`: identity, not state](../strata.md#derive-identity-not-state)).

The two rules are related by data and nothing else. The first derives a bug from a reviewed issue,
carrying the issue's address in `about` and the severity the task answered as the bug's
priority. The second selects bugs of high priority and schedules them. Neither rule names the
other: the second fires because the first wrote something it selects, and would fire the same
way on a bug anybody else wrote there.

Each rule writes fields of its own. The second does not touch `priority`, which the first
writes, and its condition is the absence of `scheduled`, which it writes: so it selects each bug
once, and a re-install, which fires every rule over everything it selects again, cannot undo its
work. Two rules that write one field install with a warning saying so.

That is how a flow of several steps continues in verse: through the store. Nothing sequences the
steps.

## Your own charters

You have the four parts now. A charter of your own is made of the same kinds of rule, over
your own data. On real systems the shapes stay the same. [Iods](../io.md) connect verse to a
repository, a forge or a chat workspace: they write the entities, and they carry what a rule
writes back out to the system. Tasks run agents on [runners](../runners.md), with a policy in
the spawn that says what each step may do. An irreversible step is behind a gate with named
answerers and a deadline.

- [Charters, continued](charters-2.md) has the rest of what a rule can say: the backlog, the
  latch, steps in order, a cause rather than a condition. It keeps a charter in a file, finds
  out why a rule did nothing, and stops a rule.
- [The charter reference](../trigger-model.md) is the model of a rule, with a page for each
  part.
