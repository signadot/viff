# perms: a place named for somebody can be taken by somebody else first — a grant cannot map a name to a principal

# A place named for somebody can be taken by somebody else first

Status: a gap in the model ([perms.md](../perms.md), "A place can be taken ahead of what goes in
it"), not a bug in the check. Nothing here is built.

## What happens

The pattern `spaces.<user>`, each owned by `<user>`, is held out the only way the model has:
the owner of `spaces` grants `child-owner` beneath it to everybody who should have a space.
`child-owner` says WHO may create beneath a node. It says nothing about WHICH node each of them
may create, so every grantee may create every name:

```
admin:  verse perms spaces -grants '[{read: ["*"]}, {below: true, child-owner: ["user:*"]}]'
bob:    verse perms spaces sally            # taken: spaces.sally is bob's
sally:  verse data spaces sally notes -put …   # refused: bob's
sally:  verse perms spaces sally               # refused: bob's
```

Probed at the entity level (2026-09-20, branch one-owner): exactly that. bob need not write
anything; taking a place ahead of what goes in it is the feature working as designed.

## What there is today, and why it is not enough

- **The owner of `spaces` evicts.** They co-own `spaces.sally`, delete it, and sally then takes
  it. Probed: works. It is a repair by hand, after the fact, and whatever bob put there goes too.
- **The owner of `spaces` makes each space.** Then it is THEIRS, not sally's: ownership moves
  only by creating and deleting, there is no transfer, and the most sally can be given is
  `read-write` and `child-owner` beneath a space she does not own. What she installs there acts
  as her, but she cannot set the space's own record.
- **Nothing ties a name to a principal.** `sally` the segment and `user:sally@example.com` the
  principal are unrelated strings. A grant's `at` takes literal segments and at most a trailing
  `*`.

Principals being self-asserted is a separate matter (bob can say `-as sally` today); this gap
stays when names are verified, which is when it starts to matter.

## What is wanted

A way for a grant to say "each grantee may create the node that is THEIRS beneath here, and only
that one" — a mapping from a name in an address to a principal. It has to stay a statement
about paths, said by the record that holds it out: no fixed depth, no position the check knows
on its own. Shapes worth weighing, none chosen:

1. **The segment is the principal's own id.** `spaces."sally@example.com"`, with a grant line
   whose `at` may carry one segment meaning "the creator's id": `{at: [spaces, !self],
   child-owner: ["user:*"]}`. No mapping to keep, since the name IS the principal; the address
   is uglier, and a kind has to be dropped or kept (`user:` is not a legal bare segment today).
2. **A name registry the line refers to.** A record mapping short names to principals
   (`verse.principal.name.sally -> user:sally@example.com`), owned by whoever administers
   names, and a grant line whose open segment is resolved through it. Short addresses; one more
   thing to administer, and who may register a name is this same problem one level up.
3. **Reserved ahead by the owner, claimable by one principal.** The owner of `spaces` writes a
   line per person — `{at: [spaces, sally], child-owner: [user:sally@…]}` with `child-owner`
   read as reaching the node at `at` itself and not only beneath it. Works with today's grammar
   plus one change (a `child-owner` that covers its own `at`); it is per-person upkeep, which is
   what a pattern was meant to avoid.

Whichever it is, the refusal bob gets should say whose the name is to take.

## Where it touches

`entity/grant.go` (the `at` grammar, `Covers`), `entity/attribution.go` (`accessFor`'s grant
walk, `mayTake`), `docs/perms.md` ("Grants"), `docs/reference/perms.md` ("Records", "Creating,
and taking a place").