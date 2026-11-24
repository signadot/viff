# Viff beside other tools

This page places viff next to GitHub Actions, Temporal and an agent framework. The
[basic concepts](concepts.md) are four terms and the loop they run in; read them first if the
words below are new.

Viff works with things it does not own: a repository, a chat workspace, a person's time. Viff
can *observe* them, and it can *ask* that they change. Every real delivery process works this
way. It reads and writes git, an issue tracker, bug reports, a sprint board, the chat at work.

A workflow keeps a position in a plan. That position is a second account of the state of those
things, and it is wrong as soon as one of them changes between two steps. So viff keeps no
plan. It reacts to what it last observed. It stores each observation as a commit with a
revision, and a watch replays the changes from any revision the store still keeps.

Here are six questions a team asks before it lets software act for it, each answered for the
three tools and for viff.

**Where is the plan?**

| | |
|---|---|
| GitHub Actions | a workflow file: jobs and steps, run in order on an event |
| Temporal | workflow code, made durable by replaying its history |
| an agent framework | a graph or a loop in the agent's process |
| **viff** | nowhere: rules over a store of what was last observed |

**What happens when the world moves between two steps?**

| | |
|---|---|
| GitHub Actions | the run continues on the checkout it started with; a concurrency group can cancel it |
| Temporal | whatever the workflow code says; it is code |
| an agent framework | whatever the next tool call reads |
| **viff** | the change is one more change to the data, and it matches a rule or it does not; a stale proposal is retired |

**Who holds the credentials?**

| | |
|---|---|
| GitHub Actions | the job: secrets are in its environment |
| Temporal | the activity worker |
| an agent framework | the agent's process |
| **viff** | the runner; a task never holds the git token, it asks the runner to push |

**How does a person say no?**

| | |
|---|---|
| GitHub Actions | a required reviewer on an environment, or cancel the run |
| Temporal | a signal the workflow code waits for |
| an agent framework | an approval node the author added |
| **viff** | a gate on the rule or on the task, with named answerers, a threshold and a deadline; or a hold or a suspension at any time |

**Whose is it?**

| | |
|---|---|
| GitHub Actions | the repository's: a workflow runs under the repository's token |
| Temporal | the service's: workers run under the service's identity |
| an agent framework | one process, with the identity its author gave it |
| **viff** | a team's: one shared store, every act under a person's or an agent's own name, and the work visible and steerable by whoever holds the permission |

**What is kept afterwards?**

| | |
|---|---|
| GitHub Actions | logs and artifacts, for a retention period |
| Temporal | the event history |
| an agent framework | what the agent logged |
| **viff** | every task, question and answer, as data: who decided what, and when |

The tools are not rivals. GitHub Actions runs a pipeline well, and a viff task can run a
pipeline. Temporal makes a long computation durable; viff makes the state of the world durable,
and no computation. An agent framework gives one agent a loop; viff gives any number of agents,
scripts and people the same store, the same gates and the same record.

## The design in short

### The state of the world is observed durably

Viff stores the state of the world as a tree of documents. A write is a patch, made as one
commit, and a read is the whole document at an address as it stands. [Iods](glossary.md#iod)
carry the world in both directions: an iod writes what a system holds into the store, and
carries what a rule writes back out to the system.

### The state of viff is in the same store

Rules, tasks, questions, iods, runners and permissions are all in the store, under `viff.*`.
So you read them, watch them and write rules over them as you do any other data.

### Gates are questions

A gate is a question with two sides: who asks, and who answers. A person can answer, and so
can an agent, which may in turn ask somebody else. A question can need several answers before it
settles, and it can have a deadline.

### Rules decide what happens

Rules over the data decide what viff does, and that data includes viff's own records. A rule
reads the store as it stands at one commit, and what it does depends only on what it read.

### Every write has an author

Every write to the store is made by a principal, and viff records who it was.
