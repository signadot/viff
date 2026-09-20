# A charter authored per change

Status: idea, not designed.

Verse is a harness for distributed agents, and a charter is the program they run. Today that
program is written by a person, up front, for a class of change: "when a PR opens, review it."
The rules are general because they have to cover every change that will ever match them.

The idea is to let a specific change get its own rules. A prompt plus a diff spawns an agent
whose job is to *author the charter for that change* — the triggers, gates and sequence this
particular change needs — which the engine then installs and runs. A refactor that touches one
package gets one shape; a migration that touches the schema and three services gets another.
Nobody wrote either shape in advance.

## Why the substrate is already most of the way there

Nothing here needs a new mechanism, which is what makes it worth writing down.

A charter is *data*, so an agent can emit one — a trigger is a tony document, and
`verse c -a` is how it lands. Installation is already a runtime act, not a deploy.

A [goal](../working/residuals.md) (`9by2kc0n`) owned a bundle of triggers, held a live status, and
reaped them on a terminal. That is exactly the lifetime a per-change charter wants: authored
for this change, alive while the change is, gone when it lands — and it is the one thing the
goal shape got right, which is why it is worth reading now that goals themselves are gone. The
missing piece was always the generator: something that AUTHORS the owned triggers rather than
taking them by hand. That is this, with an agent as the generator.

A [scope](scope-lifecycle.md) isolates it, so a charter authored for one change cannot
fire on another, and dropping the scope drops the whole program.

So the shape is roughly: a change arrives, a trigger fires an authoring agent, the agent
writes a charter into a scope, and something with the change's own lifetime reaps it when the
change lands. What plays that last part is open — it is the piece goals were reaching for.

## What is actually open

The hard part is not generation, it is *governance of* generation, and none of it is settled.

An authored charter is code the system will run on its own state, so authoring is the most
privileged act in the model — far beyond what any gate currently guards. The
[authority](authority.md) note argues an envelope should be derived from the request; here the
request produces rules rather than actions, and an envelope over "what rules may this agent
write" is a different and harder object than one over "what may this task do".

The prompt is also attacker-influenced text, which authority already flags as the sharp edge: a
diff is written by whoever opened the change. A charter authored from a hostile diff is a
hostile program, installed by us.

And there is a termination question the general case does not have. A hand-written charter is
reviewed once and reused; a generated one is new every time, so "does this program halt, and
does it halt on *this* change" has no prior evidence behind it. The runaway backstop meters
entities acted on, which bounds damage per firing but says nothing about a program that keeps
finding new work.

Worth building the smallest version first — an agent that proposes a charter a human installs
— because that is the same loop with the governance question answered by a person, and it
would show whether the generated rules are any good before anything runs them unattended.
