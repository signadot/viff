# residual: one owner — a kept store's rows, the grant at the commit, the container above a source, a name the system collides on

# One owner: what is left

Status: residual of a landed change. What runs is described in [perms.md](../perms.md): every
node has an owner, the nearest owner record at or above it; a `child-owner` line confers `owner`
on a creation and on nothing that stands; a place that acts is installed as somebody's or not
at all; whose a rule is, is said once, on its record; no rule but the system's writes under
`verse`. It landed on branch `one-owner`, from `7b84b8be` to `f6ff7227`.

What is here is what that work named and did not build, so a known gap does not become an
unknown one.

## 1. A store kept across the range does not gain the rows' `read` line

Each `child-owner` row of the seed now also holds `read: ["*"]` out beneath itself, for what
stands there with no record of its own. A seed row is one record written under an absence
precondition, and `grants` is a list, so a row that already stands is left as it is: a kept
store's rows have no `read` line. Measured on a store the old binary wrote: what the system
made there before the upgrade is unreadable to everybody but the system until the base charter
next DERIVES it under the new binary (the maker's record comes back with the derive and copies
`verse.schedule`'s default) — `verse.schedule.time` within the minute, `date`, `weekday`,
`timezone` and the rest at the next hourly tick. A person's rule reading the calendar stops
matching in between, silently.

`user:admin@verse` can add the line by hand (`verse perms verse schedule --grant '{read: ["*"]}'
--below`). The design question is whether `--reseed-perms` should reach the rows: today it
rewrites the root, `verse` and the admin group and says it touches nothing else, which is
deliberate, and a reseed that replaced the rows would undo an operator's narrowing of one.

## 2. The grant is not re-checked at the commit

A write admitted by a `child-owner` line goes to the store conditional on nothing standing
where it lands, and the store settles that at the commit. Whether the GRANT still stands at the
commit is not asked: a line withdrawn between the check and the write is honoured once more,
which is the race every write has. The condition that would close it is on the mirror's record
and the store folds a write's preconditions into one match at their common prefix — the root,
for a world write and a mirror record. Measured on the store alone, a create with and without
it: 1.0 ms against 1.9 ms at 1,500 entities, 2.5–3.0 ms against 9.0–9.6 ms at 15,000. It grows
with the store and only grant-admitted creates would pay it. Not added. The two ways to make it
cheap are a mirror keyed so that a node's record shares a prefix with the node (a change to
where the mirror lives, which is the system's to decide), or a match at more than one path
upstream in go-tony.

## 3. The container above a source's claim nodes is the root owner's

A source's records stand at its claim nodes (`git.<repo>`). The node above them comes into
being with the first pass and has no record, so it is the nearest owner's — the root's,
`user:admin@verse` — and its installer holds nothing at it: they cannot hold `git` out to
anybody or remove it. Anybody may still create beside the claim, where the root's line admits
it. Whether a source's first pass should record the topmost node it made for its installer, as
an ordinary write does, was left to decide; nothing depends on it today.

## 4. The system installing at a name somebody holds

`ClaimForSystem` displaces: where a person's record stands at the name a system charter
installs a rule at — anybody may install a rule named `calendar` before a release adds one —
the system's install takes the record, and says so in the log. Refusing the system's install
loudly may be the better answer, since a collision between a deployment's charter and a
person's rule is somebody's mistake and the log is not where they will look. Not decided.

## 5. A relayed slide is remembered by task id alone

The control route hands the actor of a relayed vector slide to the projection through a map
keyed by the task's id (`Server.slides`), which is what both ends have. The same id relayed in
two scopes at once would cross, and an entry is never removed. Small, and written down because
it is an attribution.

## Related, and tracked on their own

- `/broker/local` serves the whole local runtime to anybody who can reach the port:
  `jp9dy8an`.
- A scope name nobody made opens a store session and an attribution, and nothing deletes them:
  `f80cejye`.
- A task's snapshot, events and inbox are served with no permission check.
- Principals are self-asserted; policy edits are not gated: `e5cmt50r`.