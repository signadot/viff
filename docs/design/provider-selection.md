# Why: Provider selection

The reasons behind [Provider selection](../provider-selection.md). They are for somebody changing verse; the page itself says what verse does.

## Why attributes and fragment fields share one namespace

Splitting the two would make a provider entity a schema. The free-form payload is what lets a
new attribute exist without a change to the code.

## Why `auto` poses no question

The docket is the place where a decision can be answered and has an author. An `auto` pick has
no answerer and casts no ballot. A record of it would put a decision on the docket, reported as
the group's, that nobody made. This is not an optimisation.

## Why an `auto` pick is not stored

A stored pick would outlive the doer it named. Picking again at each firing is deterministic
for the same registry, and it follows the registry when the registry changes.

## Why a credential is an address

A name would need a registry beside it. That registry would exist once per runtime, would have
to agree across deployments, and would carry no permissions of its own. An address uses the one
namespace verse already has.

Spending a credential has a cost. So who may spend it is a permission, and the permission
model that verse already has answers the question.

## Why the store holds the key

The other design is a reference to a name that each runtime allows. It gives less than it
seems to. Whoever may read a credential can already spend it, by naming it in a spawn. So the
reference only moves the place where the key is typed. It costs a third name, a list per
runtime to keep in step, and a deploy to rotate a key.

What remains is a smaller difference: a permitted reader can also copy the key out. The budget
and route of a Portkey virtual key limit what that copy can do.

## Why a copy by a rule is not checked

The read that lets a spawn spend a key is the same read that lets a rule copy it. A check that
refused the copy and allowed the spend would draw a line that the permissions model cannot
draw.

## Why a doer's grant has a ceiling

If verse took the `grant` of a doer as it is, writing an entity would be a privilege
escalation. The charter that spawned the task never allowed it.

An omitted grant is not a small ceiling. It is an unbounded one. That is why a spawn with
`provider:` must state its `grant:`.

## Why a widening doer is ineligible

Trimming a doer means dropping the field that widens and running the doer anyway. The task would
then run under a grant different from the one its own entity states. The named-grant vocabulary
exists to prevent exactly that.

A check after the pick would let a group deliberate, reach its threshold, and elect a doer that
then cannot run. That is a decision that cannot take effect, and no voter could know it was
making one.

An unknown grant name that resolved to nothing would let a typo run a task with no grant.

## Why a task records `doer`

Two doers on one driver is the case that provider selection serves. A task that showed only
`llm` would report very different work as the same agent.

## Why the docket shows each option's grant and tools

When options differ in what they may *do*, a voter who picks between two names picks between
two authorities. A listing that showed only names would have the group authorise a capability
that nobody showed them.

## Why a resumed spawn cannot recover everything

Both limits are deliberate. `$references` read the subject again rather than guess at the old
one. A restart is not a commit, so the run names no `occurrence`. A selection whose rule or
subject is gone is settled `orphaned`, because a ballot that nobody reads is worse than an
empty docket.
