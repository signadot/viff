# Grants: what is left

Status: residual of a landed change. What runs is described in [perms.md](../perms.md): one
owner per node in a mirror written by versed, grants on the records, a check in the store view
on every write and every read, and a charter reading as its owner. The design is the ownership
model in git-issue `yezdk01bh12kshx9m9n0`; the order it landed in was the plan that note's
follow-up became, seven commits on branch `grants` from `74b2d9d7`.

What is here is what that work named and did not build, and two places it departs from the
issue's literal spelling, so a known gap does not become an unknown one.

## 1. Policy edits as gated changes

`verse c -a` writes a rule directly, and a grant is written by `verse perms` and by the verbs
that make one — a claim, a binding, a gate's answerers — with nothing gating the edit. The issue's shape
is that a policy edit lands as a proposal under the installer's own node and a system charter
promotes it, posing an ask when a grant widens or a gate's mode moves from ask to auto. A
system charter can already *watch* `verse.perms` and `verse.trigger.rule` (the mirror is
readable by the system alone, and a rule of anyone else's reading it is refused at install), so
today it can pose a question about a widening after the fact, which is an audit and not a
gate. The verb, the proposal record and the promotion are one piece of work.

## 2. Evidence from outside the agent, checked at install

With owners on every node and a runner's grant confined to its task it is decidable, at
install, whether a rule that spawns onto a runner has a condition the runner could satisfy by
writing: a condition over the task's own report is the agent moving itself forward, a
condition over source-owned facts is not. Nothing asks it yet. It belongs with the install
check above.

## 3. A source's claim as an owner record

A source's `owns` claim is checked for disjointness against other registrations and nothing
else. The model says a claim is the list of paths the installer needs `owner` at, refused at
install if they lack it — and a claim written as an owner record at the claimed path would
also spare the mirror one record per reflected entity, since each would then be owned by the
record above it. Two things stopped it here: a claim may carry a glob or a path inside the
payload, which is no address to hang a record on, and the claimed node may stand already with
nobody's record, which is the system's and would need the install to take it. Both are
answerable; neither is answered.

## 4. What is not attributed

- **A resumed task's gates.** A runner reached by url now acts as the identity on its entity
  from the first reflection, and a brokered task as its claimant. What a resume cannot recover
  is the spawn's own gates, which the task record does not carry, so a gate a worker raises
  after a restart is held out to whom the frame names or to the docket's default, not to the
  spawn's answerers for that name.
- **The first toucher owns.** A node that exists with no record — one written before the
  mirror was, or by an unattributed request — is owned by the first principal holding `owner`
  who writes it, since the store cannot tell a create from a merge into what stands. Staging's
  pre-ship entities are the case, and the ~90 records owned by a rule's *address* from before
  the resolution fix are still as written; a wipe (git-issue `hd63vzmfh12krqkhm1n0`) settles
  both, and a migration for one deployment was deliberately not built.
- **The cache and a second replica.** The view drops what it cached about a node on every
  write this process makes to the mirror, and nothing else. One versed runs against one store;
  a replica writing the mirror would have to be watched for, and is not.

## 5. What a default is not yet

What a new record holds out comes from the nearest default up the tree, the root's from
config. Three things around it are not built. There is no `read` row in the seed for a
source's slice, so a private root default hides every reflected entity from everybody but the
installer, which is §3's record again. A default is copied at creation and nothing revisits a
record when the default above it changes, which is the model's stance and also means a place
made private after the fact has to be walked — `verse perms` takes one node. And there is no
listing of what a person holds anywhere: the verb reads one node's record.

## 6. Directed gates: what is left

Built from the directed-gates note, which landed here: the question's owner record and the docket's default, a gate's
`answerers`, a person and a group as a binding's resolver, `verse gate escalate`, the ladder
walking every standing binding in name order, and groups. Not built:

- **A hook's spawn gates are not checked at install.** The action's own spawn gates are; a
  spawn beneath `onDone` and the rest is not walked by the answerers check.
- **A person's silence.** A person on a rung escalates past their binding's `deadline` as a
  service does, when a reader arrives; nothing wakes them first.
- **Carrying gating through derivations**, the issue's second paragraph: a derived entity
  inheriting the answerers of what it was derived from. A question about `derive`.
- **A group of services.** A group of service bindings would mint an instance per service per
  question — which nothing does yet, since a service member of a group is admitted by the grant
  but is minted no instance to read the run through. A service that must read the run is bound
  directly.

## 7. Where the question stands

The issue says a question lives under its origin so that its co-owners fall out of the tree.
It stays at `verse.gate.question.<id>`, and the edge is computed: the record carries `under`,
and the check follows it for ownership. Moving the address would put a question at depth five
or six, inside the task or the rule entity for every depth-four reader, and make the docket a
cross-depth query nothing spells. If the address ever moves, `under` goes and the walk is the
plain one; the computed edge is the cheaper spelling of the same fact.

## 8. Smaller things

- A whole delete lists the mirror to forget the records beneath it, which costs the mirror's
  size per delete; the reaper deletes tasks routinely. Unmeasured on staging's mirror.
- A listing's filter walks the mirror once per entity, through the cache. `verse ps` on
  staging's mirror has not been measured under it; if it shows, the answer is a memo per
  distinct prefix, not a weaker check.
- `verse data verse perms` is refused a person, as a listing at a place they may not read into;
  `verse perms <path>` is how they read their own record.
- The runtime authority axis — a task's `Grant` and `Vector`, what a task may *do* — is
  untouched by any of this, and [authority.md](../roadmap/authority.md) is still its note.
