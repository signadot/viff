# Paths and nodes: what is left

Status: the plan landed (steps 0–9, `84a72f95`…`f6423e74`). This is what it did not finish, each
item small enough to do on its own and none of it holding the model up.

The model is [paths-and-nodes.md](paths-and-nodes.md): one tree, one path grammar, the question
carries the depth.

## 1. Overlap does not read a descent

`segDisjoint` (trigger/stratum.go) reads a `..` segment as the NAME `..`, because
`entity.PathLiteral` answers with the string. So two patterns that both reach a node can be
judged disjoint: `verse-dev..finding` and `verse-dev.drift.finding` compare `..` against `drift`
and answer "no common address".

It fails toward ACCEPTING an install — a stratum overlap or a confinement that should be refused
is not — so a charter can be installed whose two strata both cover a node. Nothing in the tree
today writes a descending subject, which is why it is a residual rather than a defect with a
transcript.

The fix is the per-segment walk knowing the segment kind: a descent meets any run of segments, so
overlap becomes the same zero-or-more question `patternReaches` answers for one address. Reach is
already kpath's (`Matches`); this is the other question the model names.

Until it is done, a suspension's subject does not take `..` either (`entity.ExprSegs` refuses it),
since covering is decided by the same walk.

## 2. `entity.Depth` and `List` are the last fixed depth

`entity.List(pred)` walks three levels in the world's part of the tree and four under `verse`
(`walkEntityDoc`), and eight call sites spell a registry's own depth as `entity.Depth+1`
(runner, source registrations, charter packages, the perms mirror, the docket).

Each of those registries knows its own depth and can say it (`entity.Below`, which every other
registry listing now uses). What keeps `List` alive is its callers' convenience — a listing of
"everything" for a claim sweep (`versed/sources.go`) and about thirty tests.

Deleting it deletes `entity.Depth`, which is the last number in the package that is about the
store rather than about a question.

## 3. A descent is a read's and a subject's, not yet every surface's

- `!get-path` refuses `..` and should: it answers one node. `!list-paths` answers a list and
  could take one; it does not yet.
- The client's `-trim` under a descending read projects the answer as ONE document, because the
  nodes the descent reached are not recoverable from the answer alone — every node in it is
  either a match or a container of matches. The server's `-if` is what asks each node.
- `Doc.Reached` under a descent reports the containers as well, which is what makes `--paths`
  print every address in the answer. A caller that wants only the matched nodes cannot say so.

## 4. The mirror keys on a rendered address

A perms record stands at `verse.perms.node."<address>"` — the address as a STRING inside one
segment, parsed back on every check (`recordAt`, `ParseAddr`), which is why `recordsBeneath` and
`places` re-parse every key to find what stands beneath a node.

It is flat ON PURPOSE (`PermsRef`): nested under the node's own segments, a record's `owner`
field could not be told from a child node called `owner`, and the owner of a node is a walk up
its prefixes rather than down a second tree. So this is a tension rather than a defect — the one
place an address is text — and what would settle it is a record shape that cannot collide with a
child, not a rename. Its own comment still counts a record's depth against verse's part of the
tree, which is the kind of sentence the sweep was about.
