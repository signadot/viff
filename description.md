# proposal: a timed, two-way rendezvous with two-party authority, as the building block of verse's user facing interactive UX

Status: a proposal, for discussion. Related: w22rnvsj (the ask), and the branches
`verse/ask`, `verse/ask-next` and `verse/slack-llm`.

This proposal serves the proposed charter (`docs/charter.md` on the branch
`verse/charter`, draft v1). It is justified by the first three purposes of the charter.

## Terms

- A **door** is a way for a person to enter verse. At a door, what the person says or
  does becomes a record in verse. The record is written as that person. The doors are
  Slack/a chat bot, the command line, an mcp, etc.
- A **rendezvous** is the building block that this document proposes. The next section
  defines it.

## The proposal

We propose that verse has one building block for what a person does with verse
interactively. We call it a **rendezvous**: a meeting of two parties, with a time limit
and a consequence.

The meeting is written in one record.

- One party **raises** a rendezvous: words which are communicated to the other.
- The other party **answers**: in words, or by choosing an option.
- The rendezvous has a **clock**: a deadline, and a rule for what happens if nobody
  answers before the deadline.
- Something **follows** the answer: nothing, the next step of the raiser, or a task.
  This is the consequence.

A rendezvous is **two-way**. Each party can be a person, a task or a rule. Each of them
can raise a rendezvous.

A rendezvous has **two-party authority**. The two parties are the raiser and the
answerer. Each party needs authority for its own part of the rendezvous: the raiser to
raise it, and the answerer to answer it. The consequence uses the authority of the
raiser only. An answer is consent. The answerer does not need permission for the
consequence, and does not give any.

Every door offers this one building block.

## What the proposal serves

This section quotes the first three purposes of the charter. After each purpose, it
explains how the proposal serves that purpose.

**1. "To govern SDLC work by people and agents … so that the work is organised,
observable, gated, steerable, and controllable."**

"Gated" and "steerable" describe the two places where a person and the work meet. At a
gate, the work waits for a person. In steering, a person sends an instruction to the
work. Today verse has a separate mechanism for each. The rendezvous is one form for
both.

**2. "To govern that work whoever does it … And to state what each one's gates
guarantee."**

The work is done by verse's own loop, or by agents, tools and people that are already
in use. Therefore all of them must be able to raise a rendezvous and to answer one,
from the door they already use. Each rendezvous also states its guarantee in its
record: who raised it, who answered it, the deadline, and whose authority the
consequence used.

**3. "… maximises the return value of where it seeks human attention and which scales
to a massive rate of change."**

A rendezvous is the unit in which verse asks for attention. It names the person who is
asked. It gives a deadline. It says what happens if nobody answers. So verse asks one
time, asks the right person, and waits for a limited time. After the deadline, the work
continues. A question that can wait without any limit does not scale to a high rate of
change.

With no single building block, such as the rdv, the realisation of this goal will
likely become a tangled fan-out and impedes user understanding.


## Scope

This proposal is about user facing, interactive use. This means: what a person does
now, directly or through their agent, and what the person receives back.

This proposal is not about managing automation that stays installed. That is the work
of charters.

Purpose 1 also names observing and controlling. The section "The second building
block, and what else may not fit" discusses them.

## Motivating examples

### What people say to verse

Issue w22rnvsj lists seventeen scenarios. They guided the search for what the Slack
conversation must do. The table shows how each scenario can be read as a rendezvous.

| # | What is said | Who raises | Who answers | What follows | Reading |
|---|---|---|---|---|---|
| 1 | "Merge train is ready. Ship?" | a rule | named people, or a group | the rule continues | a rendezvous |
| 2 | "This comment disagrees with the code: fix, keep or ignore?" | a rule | the owner of the code | the rule does what the chosen option says | a rendezvous |
| 3 | A running task: "which of these two approaches?" | a task | the person who started it | the task continues | a rendezvous |
| 4 | "Should we call it runverse?" | a person | anyone, or a group | nothing: it is a vote | a rendezvous |
| 5 | "Bob, review verse/ask this week? Then merge it." | a person | Bob | a task | a rendezvous |
| 6 | "Can someone take the flaky test?" | a person | the first person who accepts | that person owns the work | a rendezvous |
| 7 | "Wednesday instead?", as an answer to 5 | the person who was asked | the person who raised 5 | the deadline of 5 changes | a new rendezvous, in the opposite direction |
| 8 | "Sam, this has waited two days." | a rule, after a deadline passes | the next person in the list | the consequence of the first rendezvous | a new rendezvous, to the next person |
| 9 | "What's waiting on me?" | a person | verse, from its list of open questions | nothing | answered immediately |
| 10 | "Where's the merge train?" | a person | verse, from its state | nothing | answered immediately |
| 11 | "What's up with this PR?" | a person | verse, from the flow | nothing | answered immediately |
| 12 | "What did triage say about x7y32?" | a person | verse, from the output of a rule | nothing | answered immediately |
| 13 | "Attach to this task." | a person | the output of the task | a stream | a subscription; not a rendezvous |
| 14 | "@task hurry" | a person | a running task | the task reads it and continues | a rendezvous |
| 15 | "Pause, resume, cancel this flow." | a person | nobody | the flow stops, resumes or ends | changes a rendezvous; is not a rendezvous |
| 16 | "Release web." | a person | verse repeats what it understood; the person confirms | the release, after a yes | a rendezvous |
| 17 | "Whenever a PR touches perms/, ask Sam." | a person | verse repeats what it understood; the person confirms | a rule that stays installed | out of scope |

Ten of the seventeen are already a rendezvous today. Four are a rendezvous with no
waiting. Of the other three, one changes a rendezvous, one is a subscription, and one belongs
to charters.

### What happened during the work

- **"Shall I force-push?"** An agent asked this question and waited. The person was
  doing other work at the same time. The question had no deadline and no default
  answer. So the agent could only wait. The person asked the agent to withdraw the
  question.
- **Questions in issue comments.** "Say if the verb is wanted too." The comment did not
  name a person. It did not give a deadline. Nobody has answered it.
- **A question with no deadline.** A question from a task has no deadline. If the
  person leaves, the question waits until somebody cancels the task.
- **Built separately, combined late.** A gate, an ask from a person, a question from a
  task, and a confirmation in Slack were built separately. Three bugs in one of them
  appeared only when another one was built on top of it.

## Why two-way

A task asks its person a question (scenario 3). A person sends words to a running task
(scenario 14). A conversation with a task is a sequence of these two.

If only people could raise a rendezvous, then questions from tasks and steering would
need a second mechanism. That is how verse was built until now.

If both directions are one building block, steering is not a special case. The words
of a person arrive at a task in the same way as an answer. If the task is waiting for
the person, the words are the answer. If the task is not waiting, the words wait until
the task reaches its next boundary. A boundary is the point between two steps of a
task.

## Why timed

The parties work at the same time, on different things. One party cannot expect the
immediate attention of the other. A question with a deadline and a default answer lets
both parties continue their work. It also always ends.

The clock is also the basis of sequences:

- An extension (scenario 7) changes a deadline.
- An escalation (scenario 8) starts when a deadline passes.
- A claim (scenario 6) is won by the first person who answers.

## The authority policy

**Both parties need authority for the rendezvous. The consequence uses the authority of
the raiser only. The answer is consent.**

Example: I ask Bob, "May I merge this?" Bob is not allowed to merge it. I am
allowed. Bob says yes. The merge is done with my permissions.

This reduces the permissions that the whole system needs. A person who decides does not
need the permissions of the person who acts.

The table compares this policy with other possible policies for two parties.

| Policy | The consequence runs with the permissions of | The problem |
|---|---|---|
| The answerer | the person who said yes | Each answerer needs the permissions for everything that the answerer approves. Asking becomes a way to use another person's permissions. Also, the answerer becomes responsible for a consequence that the answerer did not write. |
| Both parties | the raiser and the answerer, together: only what both are allowed to do | Bob must be allowed to merge before he can approve a merge. The system needs more permissions, not fewer. |
| One of the two parties | the party with more permissions | Each party can use the permissions of the other. |
| A grant that is passed on | a token that the answerer gives to the raiser | The answerer must hold the permission to give it. Somebody must create, expire and revoke the token. This is extra state, and it can be lost. |
| **The raiser** | **the raiser only** | The answerer decides if the consequence happens. The permissions of the raiser decide what it can do. Nothing is passed from one party to the other. |

A different policy is approval by two people, where nothing is done until both people
approve. In this proposal, that approval is the answer. It is separate from authority,
so a yes never increases what the consequence is allowed to do.

## From two parties to many

Groups and open spaces are built on the two-party rendezvous. They do not need a
different building block.

- **A group answers by counting.** Each answerer gives one answer. Verse checks each
  answer as the answer of that one person. The record says how many answers are needed.
  A vote, a quorum, and "the first person who accepts" are all counts of single answers.
- **A sequence is one rendezvous after another.** An extension is a new rendezvous in
  the opposite direction. An escalation is a new rendezvous to the next person. Each
  one has its own deadline, so each sequence ends.
- **An open space contains many rendezvous in one place.** In a Slack channel, many
  people write. But each message has one author. The place of the message decides its
  subject. A thread is a sequence. A channel is a place where a sequence starts.

This works for one reason. The authority for the consequence comes from one party: the
raiser. Answers are counted. They are never combined into more authority. So a larger
audience gives more answers, but it does not give more permissions, and it does not
need more.

Compare this with a policy where the consequence uses the authority of the answerer.
With that policy, each new member of a group increases what the consequence can do. An
open channel gives the consequence the permissions of everybody.

## How the existing work fits

| Built as | Who raises | Who answers | Where it is |
|---|---|---|---|
| a gate | a rule | people, or a group | `main` |
| an ask, or a vote | a person | people, or anyone | `verse/ask-next` |
| a question from a task, or a turn | a task | its person | `verse/ask` |
| `say` | a person | a running task | `main`, `verse/ask` |
| a confirmation | verse, for the speaker | the speaker | `verse/slack-llm` |

All of them, except `say`, already write the same question record to the same list of
open questions. `say` already travels in the same way as an answer: on the control
channel, which verse handles before it handles observed changes.

## What the proposal needs from the work

1. Every question has a clock. This includes a question from a task.
2. There is one record, for every kind of raiser.
3. Every door offers the rendezvous. The command line is first.
4. The raiser is told the answer. The raiser does not need to check for it.
5. Each thing has one name.
6. The consequence uses the authority of the raiser only. Done on `verse/ask-next`
   (2e17878e): a rule that runs a consequence for its raiser may select only in a place
   its installer owns.

## The second building block, and what else may not fit

**Attach** (scenario 13) is not a rendezvous. It is the second building block, a
**subscription** (decided 2026-09-28; the reasons are in the comments).

A subscription is a cursored, one-way reading of the stream of a subject. It has no
clock and no consequence. `verse watch`, `verse task attach`, `verse flow show` and a
visualizer are all subscriptions. The subscription is for reading, and the rendezvous
is for interacting. Both exist today: the stream and the cursor.

What else may not fit:

- **Queries** (scenarios 9 to 12) do not wait for anybody. Maybe they are simple reads.
- **Pause, resume and cancel** (scenario 15) are done by one party alone. They change a
  rendezvous or a running task. They are not a rendezvous.
- **A conversation turn** has no clock. This is an exception to "every question has a
  clock", and we accept it.
