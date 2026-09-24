# a charter authored per change: an agent writes the rules for one change

From `docs/roadmap/dynamic-charter.md`, removed with the roadmap; the note whole is
`git show fb75880d:docs/roadmap/dynamic-charter.md`. Status there: idea, not designed.

## The idea

A charter is written by a person, up front, for a class of change. Instead, a prompt plus a diff
spawns an agent that authors the charter for THAT change — its rules, gates and sequence — which
the engine installs and runs, and something with the change's lifetime reaps when it lands.

The substrate is most of the way there: a charter is data and installing is a runtime act; a
scope isolates a bundle of rules and dropping it drops the program; goals (`9by2kc0n`) owned and
reaped a bundle of rules, and the missing piece was always what AUTHORS them.

## What is open is governance, not generation

- Authoring rules is the most privileged act in the model; an envelope over "what rules may this
  agent write" is harder than one over "what may this task do" (the authority issue).
- The diff is attacker-influenced; a charter authored from a hostile diff is a hostile program
  installed by us.
- Termination: a generated program has no prior evidence it halts on this change; the runaway
  backstop bounds damage per firing, not a program that keeps finding work.

Smallest version first: an agent proposes a charter and a person installs it — the same loop
with governance answered by a person, showing whether the rules are any good first. The roadmap
ordered this last, after authority.