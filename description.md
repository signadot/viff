# systems: verse-charters, a runner system whose verbs install, list and remove charters at an address

A task can make a charter but nothing lets it install one: no action installs a charter, the credential a gate mints is for a system verse does not own, and a task's tools go through the runner, which performs `git`, `github`, `slack`… and nothing on verse itself. The one path there is, the git iod's `install` input, is the repo-charters reconciler's doer: it installs a tree of the repository, so a charter made by a task would have to be pushed, reflected and installed from a branch, which turns a dynamic charter into a pull request's worth of machinery for something no person asked to read.

`verse-charters` is one more system in the systems model: a verb on a system, with the parameter a charter wants to say something about, performed by the runner with its own principal, under a ceiling. Containment is the pattern check that already exists.

## The document

```tony
name: verse-charters
date: "2026-10-19"
description: "The charters of the verse this runner serves. Each verb is a request the runner makes to verse with its own principal, as `verse charter` makes it; a task never holds the principal. A charter a task installs is a package at an address, confined to it as any package is."
verbs:
- name: install
  description: "Install the charter files of a directory in the workspace at an address, as `verse charter -a <at> -d <dir> --prune` does: every file in it or none, each at or under the address, and what the directory leaves out at and under the address removed. An unchanged rule is not restarted."
  params:
  - {name: at, description: "the address installed at; every file's `at:` lies at or under it"}
- name: list
  description: "What is installed at and under an address, as `verse charter <at> --paths` prints it: one path per rule, level and schedule."
  params:
  - {name: at, description: "the address listed"}
- name: remove
  description: "Remove the charter at an address and every charter under it, as `verse charter -R <at>` does. What they wrote stays."
  params:
  - {name: at, description: "the address removed"}
```

Every verb is a step, the read included. `at` is the only parameter: a row's `at` is a literal or a pattern, and a ceiling's is the same.

## The tools

`verse-charters-install -at <address> -dir <dir>`, `verse-charters-list -at <address>` and `verse-charters-remove -at <address>`, from the task's shell as the other systems' tools are, and through `verse-call-tool` from a script. `-dir` is a directory of the workspace; the runner reads the files and makes the one request. The reply is verse's: what was installed, or the refusal, in verse's words.

## What the runner needs

The system is offered where the runner's configuration names the verse it acts on and the credential it acts with (`systems.verse-charters.credential`, the NAME of the variable, as `linear-issue` and `slack` are offered only with theirs), and not otherwise. The runner installs as its own principal, so it has to own the address or hold `child-owner` above it; what verse refuses, the tool reports.

## The ceiling

```tony
systems:
  verse-charters:
    credential: VERSE_RUNNER_TOKEN
    ceiling:
    - {match: {at: "repos.verse.git-issue.issue.*"}, then: allow}
    - {then: deny}
```

The default is `deny`, and the document says so: a task that can install a charter at the baseline can write rules that spawn tasks with any policy, which is the one verb that would widen a ceiling from inside. An operator opens one address pattern, and a spawn's row lies within it.

## What it is not

- Not the repo-charters path. That installs what is on `verse/live` and gates the install; this installs what a task made, under a row the spawn wrote and a ceiling the operator wrote, and the gate is the spawn's.
- Not a way to read the store. `list` answers what is installed, not what the charters wrote.
- Not a relay. The package is the runner's; the task that asked is on the task's stream. Installing as the task's carried identity through `client.NewRelay` is a later choice, if the record wants it.

## The first use

The software factory on issue-next: a mapping task renders a per-issue charter from fixed station templates and installs it at `repos.verse.git-issue.issue.<id>`; the charter's last element removes it after reporting on the issue. See 34f9wqb3h12ks1aqtnn0.