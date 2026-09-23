# cli: --if on `verse d` is a guard on the whole answer, where every operator reads it as a selector

Found on the first live run of both io halves (docs/working/work/io-live-run.md, finding 7).

`--if` is described as "keep only the items matching this tony pattern", and on `verse d` the
whole answer is ONE item — so it prints everything or nothing, and never selects within a
collection. What an operator writes first is the thing that cannot work:

    $ verse d slack example channel -if '{name: general}'
    {}
    $ verse d slack example channel -if '{}'
    C0123ABCDEF: {archived: false, name: general, …}

`--trim` in the same position DOES descend, given a list pattern, which is what makes the pair
read as though `--if` should too:

    $ verse d slack example message C0123ABCDEF -trim '[{text ts}]'
    - text: "3"
      …

`cmd/verse/filter.go` says all of this plainly — "anything else answers one document, which is
one item — so `data --trim` projects a payload, and `--if` on it prints the entity or nothing" —
so the behaviour is designed and the documentation is where it belongs. What is missing is a way
to ask the question everybody asks: which of these does X. Today the answer is `--paths` and a
grep, which is a different language for the one query the match grammar was chosen for.

Two things to decide:

1. Whether `verse d` should treat a collection the way `--trim` already does, so `--if` selects
   entities when the path reaches several. That would make the flags agree, at the cost of `--if`
   meaning something that depends on the shape of the answer — which is exactly what `--trim`
   already does, so the inconsistency is the current state rather than the change.
2. If not, whether `--if` on a collection should REFUSE rather than print `{}`. Printing nothing
   is indistinguishable from "nothing matched", which is the reading that sends somebody looking
   for missing data.

Also in that file: the doc comment describes a `state` command — "`state` answers a document keyed
by ADDRESS: an item is one entity's payload" — and no such command exists; it is `data` now. That
one is a straight correction.