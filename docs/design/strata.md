# Why: Strata

The reasons behind [Strata](../strata.md). They are for somebody changing verse; the page itself says what verse does.

## Why the ordering is declared rather than inferred

The alternative is to infer the ordering. Build a graph over the installed rules, with an edge
from a rule that writes a key to a rule that selects on that key, and refuse a cycle. That
check is decidable for a literal patch. Through anything computed, it is best-effort only. A
patch whose values come from a template can satisfy a predicate that a check on keys cannot
see through.

A guarantee should not be best-effort. The check silently lacks every edge that a template
hides. A check that is right most of the time looks exactly like one that is right, until the
case where it matters.

With a declared ordering, a write across levels is ruled out by construction. Nothing is left
for a template to hide. For the same reason, verse constrains the content of a derive and does
not analyse it.

## Why a derive reads only its subject

The limit follows from what a re-fire must be. A `$reference` cannot reach another entity at
all. It reads the subject of the firing, or the result of a task. Nothing above can write the
subject, since a patch touches only its own subject and a derive only climbs.

So a read upward that wakes the rule without a change to the subject resolves to the same
bytes. The derive writes them to the same address, and the write is a no-op. That is what makes
a read upward safe. A rule may use `on` or `when` on anything at any level, because the extra
firings that a read causes cannot change what the derive writes.
