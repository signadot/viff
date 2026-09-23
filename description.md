# creds: verse holds virtual keys in the store and a launch carries one — should it stay this way?

Status: a decision to take, not a defect. The code described here works and is tested; what is
open is whether this is the shape we want.

A model credential is now named by ADDRESS and the node standing there holds the key
(`verse data acme keys review -put '{key: "vk-abc123"}'`). versed resolves that address AS
WHOEVER NAMED IT, at spawn, and refuses the spawn when the read is not theirs — so who may spend
a credential is answered by the permissions model rather than by a group or a list invented for
keys. That much is deliberate and I would defend it.

What wants deciding is the consequence: **verse now keeps bearer strings in the store, and a
`task.Launch` now carries one.**

## Where the secret is, exactly

One-way, and it crosses at a single point.

    charter / provider entity / --virtual-key      an ADDRESS. a word.
      ↓  versed.credentialAt — reads as the namer, refuses if not theirs
    task.Launch.VirtualKey                          THE KEY
      ↓  runner/http.go, one field on the spawn body
    the runner → the child's stdin → x-<gateway>-virtual-key

Checked, because each of these would have leaked it back toward the store:

- **Nothing in `mirror/` writes `VirtualKey`.** The task record does not carry it. The only
  occurrence outside versed is `trigger/engine.go` reading the ADDRESS off a provider entity.
- **`meta`** — what is reflected for the flow — is built before the resolution and has no such
  field (`Subject, Runner, Chain, Prompt, Occurrence, Doer, Grant, Tools, Resolver, Gates`).
- **The spec is passed by value.** `trigger.Spawner` is `func(id string, spec launch.Spec)
  task.Handle`, so putting the key into `spec.VirtualKey` mutates a local copy; the engine's rule
  keeps the address in memory and on disk. Had that been a pointer, a live key would have been
  written into the charter.

On the coderunner the key reaches the child through a config blob marshalled to its STDIN, not to
disk.

## The two things that are actually open

**1. The store holds bearer strings.** Backups, snapshots, docd, and anyone with read above the
node. More sharply: a RULE that may read a credential node may also derive its contents somewhere
else — into `slack.acme.out`, where the io will post it to a channel. Perms on the node are
the whole of the protection, and nothing enforces that such a node is fenced; under an open root,
`verse data acme keys review` is readable by everyone by default.

**2. A launch carries a secret**, which it never did before. It crosses a wire that already
carries the prompt, the grant and the provenance, and it crosses it already authorised — but it
is a change in what a `task.Launch` IS, and a remote runner now receives a spendable credential
rather than a name for one.

## Why it was taken

The alternative — the node holds a reference to a name each runtime allowlists — was built first
and removed (`318c4d54` reverts it to this). It bought less than it looked: whoever may read a
credential can already SPEND it by naming it in a spawn, so the indirection only moved where the
key was typed, at the price of a third name, a list per runtime to keep in step with the store,
and a DEPLOY to rotate rather than a write. The residual difference is that a permitted reader
can also copy the key OUT — which a gateway virtual key's own budget and route bound, and which a
provider key would not.

## The question this closes on

Does this stay as it is — the store holds virtual keys, perms are the protection, a launch
carries one — or not; and if not, which of these instead:

- **Fence it by construction.** A credential node is created fenced (an owner and `grants: []`
  out loud, the way verse's own half is seeded), so a `--below` grant written for something else
  cannot reach it, and reading one is a deliberate act. Cheapest, and does not answer the rule
  that IS permitted to read.
- **Keep the value out of reach of charters.** The engine refuses to derive from, or expand,
  a node under a credential place — so a rule may cause a spawn to spend one without ever being
  able to copy it. Closes the exfiltration route and leaves rotation a write.
- **The runner fetches it.** verse checks the namer and sends the ADDRESS; the runner reads the
  node itself. The launch stops carrying a secret; the runner needs read on credential nodes, and
  a compromised runner can read all of them.
- **Back to a reference**, with the allowlist, accepting a deploy to rotate.
- **Verse is a secret store**, deliberately, with whatever that implies — its own place, its own
  fencing, not-in-backups, and a decision about provider keys, which this design says explicitly
  do not belong here.

Related: hm4rgsyvh12krm5cpdn0 (per-task llm creds, where this was decided in conversation), and
docs/working/work/config-secrets.md, which drew the "a config file, yes; an ENTITY, no" line for
`!env` and is the nearest thing to a precedent pointing the other way.