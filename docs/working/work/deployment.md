# Deploying staging: `deploy/staging.1`

Status: a plan. The code it deploys has landed ([done.md](../done.md), the permissions rows) and
none of this has been run against staging.
Claimed: walter@example.com until 2026-10-19

This is the `deployment` claim in [owners.tony](../owners.tony), worked the way the
[charter](../charter.md#deployment) says a shared instance is deployed. What is true of
permissions is [perms.md](../../perms.md); this is how it reaches a verse that is already
running, and what that verse loses on the way.

## Under the charter

- **It is owned work.** Deploying a shared instance has an owner, and the owner decides how it
  proceeds. That is this note.
- **What is deployed is what was communicated as reflecting the group.** An owner says what of
  their work reflects the group, where the charter owners can see it; the deployment owner
  deploys that, and reads the communication as they find it. For this deploy that is `main` at
  the commit shipped. **On a conflict the default is not to deploy**: contested work is left
  out and its owners are reached, since not deploying is cheap and reversible and a tag whose
  annotation says the group deployed something it did not agree to is neither.
- **The deploy is a tag.** `deploy/staging.N`, annotated, on `main`; the annotation names the
  components deployed. This one is `deploy/staging.1`: no `deploy/*` tag exists yet.
- **The tag is the settled state, not the road to it.** A rollout passes through states nobody
  means to stay in — versed scaled to 0 while docd rolls, a tier at the new image while the
  next still runs the old, a verse that is up and not yet repopulated. None of those is a
  partial deploy and none is tagged. The tag is cut once, for the state the rollout was always
  going to end in, after step 7 has verified that staging is in it.
- **Off `main` is the exception and says so.** If a deployment issue has to be worked around
  from a commit that is not on `main`, the tag goes there and its annotation names the nearest
  `main` commit.

What is settled here: all four tiers at one commit — docd, versed, the source connectors (git,
git-issue, github, linear) and the coderunner — on a wiped and repopulated store, with the top
of the tree as the staging profile says ([below](#open-or-administered-a-line-in-the-profile-committed)).

## It is a wipe, and why

Two ranges ship together. The first changed the check and not the data — the root's record, the
admin group, `child-owner` — and on its own it would have upgraded staging in place, keeping its
gates and its work in flight; that procedure was rehearsed, and is in the history of the note this
one replaces (`git log -- docs/working/perms-root-rollout.md`). The second changed the data: a
runner, a binding and a source stand one segment higher (`verse.runners.<name>`,
`verse.resolvers.<name>`, `verse.sources.<name>` with a `root` field), what watches a question
moved to `verse.watch`, a source's descriptor says `root`, and every hint url lost a segment. A
store written before it holds registrations the new binary does not find. So the pair ships as **a
wipe and a repopulate**, and "no wipe, no re-registering sources" is true of the first range and
false of the pair.

A wipe takes what no file describes. `deploy/repopulate.sh` says what it puts back and what is
gone: the drives' charters, the four sources and the runner come back; everything the sources
reflect re-populates; open gates, flows in flight, the firing history and what the drives
accumulated do not.

## The order

1. **Capture what only staging holds.** One place there is described by no file in this
   repository: `signadot.naming.for-verse` (below). Reading only:
   `deploy/naming.sh capture <staging-addr> <dir>`.
2. **Look at the open gates' deadlines before choosing the hour.** A gate whose deadline passes
   before the wipe settles by its default, and the capture then carries a decided proposal and
   no gate — a faithful restore of a different state. Capture after anything about to settle
   has settled, and as close to the wipe as is practical.
3. **The wipe.** *Unspecified here:* the command that empties staging's store is not written
   down anywhere in this repository (`deploy/README.md` has none), and this note does not invent
   one. Whoever did it for the paths range knows it; it belongs in `deploy/README.md` beside
   "Shipping one tier at a time".
4. **The ships, a tier at a time** (`deploy/README.md`, "Shipping one tier at a time": versed
   scaled to 0, then docd, versed, the sources, the coderunner). All four roll: the sources'
   descriptor and their hint urls changed with versed, and the two vintages must not meet — an
   old source's descriptor reads as having no root and is refused at install. **What ships is
   what is committed**, the admins included ("Open or administered" below): a wiped store seeds
   its top from the config at its first start.
5. **`deploy/repopulate.sh <staging-addr>`**, as the operator the places are to be: what it
   installs is its installer's. Two things differ from the last time it ran: the git-issue
   source is `issues`, since a source's name is its whole address and `git.cluster` and
   `git-issue.cluster` could not both be `cluster`; and the charters it installs live under
   `deploy/charters/` and name the runner `verse.runners.coderunner`. Arm the comment-drift
   cursors with the lines it prints.
6. **`deploy/naming.sh restore <staging-addr> <dir>`, as the naming place's owner** (below).
7. **Verify.** `verse perms --root` reads the admin form, owned by `user:admin@verse`; `verse status` shows the census repopulate expects and `perms.refused`
   does not climb over ten minutes; `verse source` shows four sources passing; a plain user
   reads `verse data verse schedule time` and cannot write it; the start's log names no place
   acting as nobody; a second principal setting a record at a new top-level place is refused.

8. **Tag it.** On the commit that was shipped, which `make ship-*` took from `HEAD`:

   ```sh
   git tag -a deploy/staging.1 -F - <<'EOF'
   deploy/staging.1

   staging (my-cluster.example.ts.net, namespace verse), settled <date>.

   components, all at <sha>:
     docd         example/verse-staging:<sha>
     versed       example/verse-staging:<sha>
     sources      git, git-issue, github, linear — example/verse-staging:<sha>
     coderunner   example/verse-coderunner:<sha>

   store: wiped and repopulated (deploy/repopulate.sh); signadot.naming.for-verse carried
   across (deploy/naming.sh). top of the tree: administered, permsAdmins as committed.
   EOF
   git push origin deploy/staging.1
   ```

   A later deploy that rolls one tier says so the same way: every component is listed with the
   sha it is running, which for the tiers that did not roll is the one from the tag before.

### Open or administered: a line in the profile, committed

Who may make a top-level place is `permsAdmins` in `deploy/profiles/staging.tony`, which the
build turns into `VERSE_PERMS_ADMINS` on versed. It is a checked-in knob and is changed by a
commit and a ship, like everything else staging runs: what is in git is what is deployed.

**It is committed ahead of the deploy**, so the commit that is shipped, and tagged, already says
it: `permsAdmins: "walter@example.com"`, the deployment's owner under the charter. The seed
writes the top ONCE, and on the store this rollout starts from — wiped — that once is the first
start: staging comes up administered, the root reading `child-owner: [group:admin]`, with no
reseed and no second roll. The repopulate and the naming restore are run as that admin, who is
who owns those places anyway. Rehearsed on a fresh store started with the admins set: the root
is seeded in the admin form at the first start, the naming restore runs as that admin, and a
colleague writes a proposal into the place.

The list only SEEDS the `admin` group. Letting another charter owner make top-level places
afterwards is `verse group add admin <them>` by `user:admin@verse`, live, as any group's
membership is; the profile is not edited for it.

On a store that STANDS, the same line changes nothing by itself, since a standing root record
is not rewritten by a config that moved; `deploy/build.tony` says what that takes
(`VERSE_RESEED_PERMS=true` for one roll, and unset after), and versed says at every start when
the root's record is not what its config would write. This rollout does not need it.

**Rollback** after a wipe is another wipe: the old image cannot read the new store's
registrations any more than the new one could read the old's, so going back is the previous
sha, a wiped store, and the previous `deploy/repopulate.sh`.

## The naming place across the wipe

`signadot.naming.for-verse` is a place a person took, installed one gated rule at, and
colleagues wrote proposals into. Read on staging on 2026-09-20: it is its owner's and holds
`read` out to everyone and `read-write` beneath it to four colleagues; one rule, `shortlist`,
whose `approve` gate names those five as answerers, needs three, and denies by default after
72h; seven proposals, six decided (`outcome: dropped`); one open gate, about `posit`, with one
`proceed` of three in; seven settled questions under `verse.gate.question`.

`deploy/naming.sh capture` writes `<dir>`: the place's record (`perms.tony`), the proposals
(`data.tony`), the open gates with the ballots cast so far (`gates.tony`), and every question
ever asked about the place (`questions.tony`). It holds people's addresses and what they wrote:
keep it out of the repository. The answerers are read from the captured record, so no address is
written in the script either.

`restore` refuses anybody but the place's owner, since whoever runs it takes the place. It takes
the place with the captured grants, writes the proposals back in one write, and then installs
the rule — in that order, so the decided proposals are never selected and each undecided one
raises its question once, at the install.

**What does not cross, and is not pretended to.** A question's record is verse's own and only
verse writes it, so the open gate is POSED AGAIN rather than restored: under the same id, since
an id is a function of what fired, with no ballots, and with 72 hours counted from the restore
rather than from the proposal. **The ballots are given again by whoever gave them** — the
restore prints what had been cast; an answer is by whoever gives it, the script gives none, and
nobody's is given for them. The settled questions — who voted what on the proposals already
decided — are in `questions.tony` to be read and nowhere in the new store; the proposals' own
`outcome` and `concerns` fields, which do cross, are the summary of them.

**Rehearsed**, capture from staging (reads only) and restore into a fresh verse of the branch's
binary: refused as anybody but the owner; the place's record as captured; the proposals
byte-identical (`diff`); exactly one gate, about `posit`, id unchanged, no ballots, 72h out; the
owner's answer and a colleague's taken, a stranger's refused 403. And under an administered
top, where `signadot` is an admin's to make: the restore run as the admin who owned the place.

## A verse somebody KEEPS across the range

Staging is wiped; a local verse may not be. The in-place rehearsal (a store the old binary
wrote, the new binary on the same data dir, the narrowing, the rollback and forward again) holds
on 51 of its 52 checks: gates, tasks, owners and the migration of `owner` to `child-owner` all
cross. What does not:

- **Registrations at the old addresses are not found**: sources and runners silently, bindings
  with a warning. A standing exclusive binding stops covering, so its questions fall to the open
  docket. Charters naming `verse.runners.pool.*` fail to spawn until re-applied. Install them
  again.
- **The derived calendar is unreadable to everybody but the system for a while.** The old
  `verse.schedule` row has no `read` line, since a seed row is written once. Each fact is
  readable again from the next time the base charter derives it under the new binary —
  `verse.schedule.time` within the minute, `date`, `weekday`, `timezone` and the rest at the
  next hourly tick — because the record the deriving write leaves copies `verse.schedule`'s
  default. A person's rule reading the calendar does not match in between. `user:admin@verse`
  adding the line to the row makes it immediate; whether `--reseed-perms` should reach the rows
  is the first item of the residual (`zf1ep7wr`).

## Open, and worth saying out loud

- **A principal is self-asserted.** `-as user:admin@verse` is available to anybody who can reach
  the api, as every principal is. What the identity buys today is a place for the authority to
  live and something to grant to; it is not a control until a name is a credential.
- **Administered is the first time staging runs shaped.** Every record there has been one
  principal's. From then a colleague who wants a top-level place asks an admin for one, and
  the naming place is the working example of what an admin then holds out.
- **The fence has not been measured under a source's first pass.** It is asked only where a
  grant would conclude ownership — a node with no owner record above it — so a source writing
  beneath its claim node never asks. The repopulate in step 5 is the measurement: read
  `verse status`' store timings across the first passes.
