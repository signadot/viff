# A driver that proposes a list of calls: implementation plan

Status: **steps 1–3.5 are built** (branch `calls-driver`); step 4 is not. The driver, `fatal:`, `scripted`
as its degenerate case, the coding runner's extras, and `calls:` as a charter field are in the
tree and documented in [reference/tasks.md](../reference/tasks.md#the-drivers) and
[trigger-model.md](../trigger-model.md#calls--when-the-charter-knows-the-actions). What is written below for those steps
is kept as the reasoning they were built from, with what the building actually found noted under
each. The design is the section *"The mechanical tasks want a script, and a script cannot push"*
in [coding-runner-residual.md](coding-runner-residual.md) — why a shell is the wrong shape, why a
call list needs no new machinery, and why `scripted` is its degenerate case.

The change in one line: **work with one right answer stops being an LLM, without stopping being
supervised.**

## Why this lands in steps

Every step below leaves a working tree and a runnable verse, and everything up to 3.5 is worth
having even if the last is never built: it takes the LLM out of the merge task, which is four git
commands driven by a model today. Step 4 needs something that does not exist yet and says so.

**And step 2 clobbers.** `scripted`'s behaviour changes for every charter that uses it, with no
opt-in and no "absent means the old thing" fallback. That is deliberate: the only users of this
repo are the people writing it, a compatibility path is a second thing to maintain bought for
nobody, and the charters that break are in the tree and get fixed in the same commit. The rule
holds until there is a consumer outside the team, which is the same trigger
[contributing.md](contributing.md) already names for versioning.

## Order of work

### 1. The driver, in `task/`, with a fixed list

A driver whose `Next` walks a list of `{tool, input, fatal}` entries and returns each as a
ToolUse. The list arrives as a tony document — in the prompt, so no charter grammar changes yet.

It lives in `task/` rather than `versed/` because both verse and the coding runner need it, and
the whole point is the runner's mechanical tasks. `scriptDriver` is in `versed/server.go` today
and could not be reused by a second program without moving.

One rule it does not get to choose: **an answer contract fails the spawn.** There is no model to
ask again, and a dropped contract is worse than none.

**What `fatal:` is for.** The rule `scripted` inherits — an `Err` ends the run, a `Blocked` does
not — is right for two different reasons that do not always agree, so the list states it per call
rather than the driver deciding for everyone. A refusal is a decision about ONE call: the sweep
deleting ten refs where the remote declines one should delete the other nine. A merge is the
opposite: if `git merge --ff-only` did not merge, the push that follows it must not happen. Both
are call lists; only the author knows which.

So each entry names the outcomes that end the list, out of three:

- `err` — the step failed. `StepResult.Err`.
- `blocked` — it was refused, by a gate or by the executor itself. `StepResult.Blocked`, built
  for push.go and this is its second caller.
- `nonzero` — a command ran and exited non-zero.

Absent means all three: a list is ordered because its calls depend on each other, so stopping is
the safe read. The sweep is the one known caller that wants otherwise and writes `fatal: [err]`.

`nonzero` is a member because **the two executors already disagree about it**, and this driver is
the first thing that would notice. `versed`'s `shellExec` folds a non-zero exit into `Err`
deliberately (*"a failure nothing reports is a failure that reads as success"*); the harness's
`fsExec` reports the exit code as data, equally deliberately, because the agent reads it and
decides. Neither is wrong for its own driver, and a call list must not have to know which one it
is running on — so it names the outcome it cares about and both executors keep their reporting.

**Proves:** an action list is supervised per action, and each call says what ends it. Tests: two
calls with a vector that ASKs on the second — the first executes, exactly one question is posed,
and denying it leaves the first done and the third unrun. Then two calls again with `fatal:`
varied — a first call that is blocked stops the second under the default and does not under
`fatal: [err]`. Plus the answer-contract refusal on its own.

**Built, and what it found.** `nonzero` was not OBSERVABLE. A StepResult carried `Err`, `Blocked`
and `Note`; the harness's exit code lived in its own record and reached the model only as text in
a prompt, so a driver that is not a model could not ask whether the merge had failed. It grew an
`Exit *int`, set by both executors, neither of which changed its reading. The same gap swallowed
a command that never STARTED — `bash` missing from a task's PATH was prose too — so fsExec now
reports a failed action as `Err` as well as narrating it, which the llm driver already renders
("ERROR: …") and had simply never been given.

### 2. `scripted` becomes the degenerate case, and its exemption goes

`NewScriptedTask` builds a one-element list rather than its own driver, and the hardcoded auto
vector goes away with it. A spawn's `vector:` governs, exactly as it does for `llm` — including
the case where a spawn names none.

**This breaks charters, which is the point.** `NewScriptedTask` writes `&task.Vector{Default:
Auto}` unconditionally today: it does not merely default to auto, it *ignores* `l.Vector`, so a
charter that already says `vector: ask` on a scripted spawn is being silently disobeyed. Fixing
that means every scripted spawn gets the vector it actually asked for, and the ones that asked
for nothing get the default rather than a blanket exemption — so charters that ran ungated start
posing questions. The repair in each is one line, `vector: auto`, which `NamedVector` has
understood since it was written; the charters needing it are in `docs/tutorial.md`,
`docs/trigger-model.md` and the runner examples, and they move in this commit.

The exemption survives in exactly one place: `verse task run --driver scripted`, where "you
authorized it by typing it" is true. That is not a compatibility shim — it is an argument about
who addressed the request, and it was always the only ground the exemption had. It stops
travelling to charters that never typed anything.

Tier: `contract`, with `Breaks:` naming scripted spawns that carry no vector, and a `Notice:`
saying what to add.

**Proves:** the equivalence, and that the exemption stopped travelling further than its argument.
Test: `verse task run --driver scripted "echo hi"` runs one ungated `run_bash` as it always did;
the same command as a charter spawn with no vector poses a question, and with `vector: auto` does
not.

**Built, and what it found.** The whole test suite passed the clobber unchanged, which was not
reassurance: nothing in it spawned a scripted task from a charter, so the behaviour was untested
either way. `versed/scripted_test.go` pins it now. Two things fell out — versed's `askingBuilder`
existed only to hand-build an Ask supervisor around a driver that would not accept one, and a
DENIED scripted command used to complete rather than fail, since the old driver read a refusal as
"nothing more to do". `verse connect --driver scripted` needed the typed-it exemption too, on the
same argument: one typed command authorizing a validator for every file.

### 3. The coderunner offers its extras, so a call list can push

`buildCommandTask` gets the finisher and not the runtime's extra tools, so `push` is offered to
the `llm` driver alone. Give a call-list task the same extras, and the merge task stops being an
LLM: `[{run_bash: git merge --ff-only …}, {push: {destination: main}}]` — the first entry taking
the default `fatal`, which is what makes the second one safe.

**Proves:** the thing this is all for. Test: a call list naming `push` resolves the destination,
derives the same effect the llm driver's tool does, and is gated the same way — the runner cannot
tell which driver proposed it, which is the property that makes this cheap.

**Built, and what it found.** The extras are shared by `harness.Calls`, which is the llm build
minus everything belonging to a model. It also enforces the TOOL FLOOR against the list: the llm
driver enforces one by not offering what a spawn narrowed away, and a list has nobody to withhold
from, so a call naming a tool this spawn does not have fails the spawn rather than the fourth
call. Two things the plan did not say: the runner's `Brief` must not be prepended to a call
list's prompt (prose in front of a document does not make a document), and `verse up` learned the
same driver — it needs the four tools and no credentials, so a verse with no keys can run
mechanical work.

### 3.5. The list becomes a field, and stops being prose

`launch.Spec` gets `calls:` — a list of `{tool, input, fatal}` in the charter grammar — and the
prompt-carried form from steps 1–3 goes away with it. Not both: two ways to write a call list is
the thing this repo keeps deciding not to have.

**Steps 1–3 are what earn it.** The reason to carry the list in the prompt first is that it needs
no contract change and proves the driver; the reason not to leave it there is what step 3
produces — a merge charter whose action is a tony document embedded in a text field, which the
engine cannot check, `verse charter` cannot show, and a branch name containing a quote can
break. A field is expanded per value (`launch.go`'s `expand`, the same one `vector:` gets), so
`$ref` reaches `git merge --ff-only` as an argument rather than as text spliced into a document
that must then re-parse.

The field's presence IS the driver selection — `calls:` with `driver: llm` is a contradiction and
fails the firing — which is the shape `provider:` already has (*"Absent = today's fixed Driver"*).

**What the engine checks, and what it cannot.** Shape and the `fatal` vocabulary, at fire time,
alongside `answer.Parse` and `NamedVector` in `versed/server.go`: a charter that wrote something
it cannot mean fails now rather than after paying for a spawn. Tool NAMES it cannot check — `push`
belongs to the runner's vocabulary, not the engine's — so an unknown one is refused at the far
end, the same fork [expose.md](../roadmap/expose.md) names for `driver:` and `environment:`.

**Proves:** a call list is something a charter writes rather than something a prompt smuggles.
Tests: a charter carrying `calls:` fires and the list arrives at the driver intact; `$subject`
expands per input; a bad `fatal` member and a `calls:`+`driver: llm` spawn each fail the firing
with the charter named.

**Built, and what it found.** `fatal:` is a POINTER to a list, and getting there cost a round
trip through go-tony. The field has three states — absent (anything ends the list), `[]` (nothing
does, which is what a sweep says), and a named set — and a plain slice carries only two: with
`omitzero` an empty list never crosses the wire, and without it nil and empty both emit `[]`, so
every call that said nothing comes back saying nothing is fatal. Both measured.

A pointer was the answer and was not available: v0.0.129 refused to generate one (*"unsupported
pointer to primitive type: slice"*, aborting the whole package), and v0.0.130 generated one that
set the pointer even when the node was not an array — turning a type mismatch into a fabricated
`[]`, which is the most dangerous of the three readings. Both filed upstream; v0.0.131 errors on
the mismatch and distinguishes the states, and this branch requires it. The wire round trip is
pinned by a test rather than trusted: the distinction that does not cross is a charter meaning
different things depending on where it ran.

Nothing else moved: the CLI keeps `ParseCalls` for a document a person typed, but parses it
client-side into the field, so there is one wire form and the error lands on the terminal of
whoever wrote the list.

Stop here if the rest is deferred. The merge is the expensive task and the one with a
consequence; the sweep is housekeeping.

### 4. The sweep, which needs something that does not exist

The sweep's problem is DISCOVERY, not deleting. Its prompt tells a task to `git fetch origin
--prune` and enumerate branches; on its first real run the task had neither the objects nor a
route, and spent eighteen boundaries hunting the filesystem for a credential that is not there.

The charter already knows which branches are stale — `merge:proposal` carries `base`, and main
moving is itself the staleness test — so the calls should come from the charter and the task
should do no git at all.

The fan-out that seemed to be missing is not: a selector `subject:` with a `when` naming another
subtree fires once per open proposal already. What the grammar cannot say is the PREDICATE —
`base` differs from a value held by a different entity — and the shape that closes it is `!let`,
which is in the match vocabulary today, with the binding filled by the path read `!get-path`
already performs on the action side. See
[merge-gate-residual.md](merge-gate-residual.md), where the ask is now stated as the one
substitution it is.

**Proves:** nothing until a condition can resolve that binding, which is why it is last and why
steps 1–3.5 do not wait for it.

## What this plan deliberately does not do

- **Give a scripted task the credential directly.** The residual argues it would be safe — the
  invariant is about the MODEL, and there is none — but it is a second way a secret reaches a
  task, and the argument rests on a property nothing enforces yet. Steps 1–3 need none of it: a
  call list asks over the channel the llm driver already uses.
- **Reconcile the two executors' reading of a non-zero exit.** Each is right for its own driver,
  and `fatal: [nonzero]` spans both. Making them agree would change what a model sees in a
  `run_bash` result, which is a much larger change than this one.
- **Anything about retries.** A fatal call ends the list. Whether the list should be resumable is
  a question about the loop, not about this driver.

## Risks worth watching

- **A call list is a plan made before the work starts.** The llm driver decides what to do next
  after seeing each result; this cannot. That is the right trade for four git commands and the
  wrong one for anything that branches — and the failure will look like a list whose later calls
  were pointless, not like an error.
- **The break in step 2 lands when the binary does**, not when the charters are edited. The
  charters needing `vector: auto` live in this repo and on `use/verse-dev`, and the second ships
  on its own clock. Sequence it: charters first, binary after.
- **`fatal:` is per call, so a list can now be wrong in a way `scripted` could not be.** A list
  whose every entry says `fatal: [err]` because that is what the sweep did will push after a
  merge that failed. The default is the protection, and it is only protection if nothing copies
  a sweep list as a template.
