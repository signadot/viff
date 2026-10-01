# charter: a package the system installs, so a package can read what only the system may

A package's rules act as its installer: what they see is what the installer may read. That stops a package from doing a job only the system can see, such as gate-ask on `verse/ask-next`, where a person's ask is read only by its asker and the people it asks.

That is why gate-ask on `verse/ask-next` is the **system charter** (deploy's `systemCharter`, `VERSE_SYSTEM_CHARTER`): a baseline charter installed as the system. On `verse/declared-outboxes` (84a83gkw), gate-ask became a package, `acme.gate-ask`, which sends through an outbox, because a package is what can declare one (`sends:` belongs to a charter with an address). As a package installed by a person, it would not see a person's asks once the two branches meet.

Wanted: a package installed by the system. It has an address, its own strata, `reads:` and `sends:`, and its rules act as the system. Then a deployment's system-level jobs (gate-ask, and anything else that must read across every person) can be packages that send through outboxes, like the rest.

## Open questions

- How a deployment says a package is the system's: `systemCharter` naming a package file rather than a baseline one, or a list of them.
- Whether a system package's address is reserved, so a person cannot install over it.
- What a system package may read and write: the same confinement to its address, but reading as the system.

Related: 84a83gkw (outboxes); `verse/ask-next`, where gate-ask is the system charter.