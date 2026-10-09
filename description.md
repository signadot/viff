# repo-charters: the live-verse tag is derived under out.git.installed.…, where the git io does not read, so no tree is ever tagged

Seen on staging after PR #9 (main c1bc057a, charters tree a4621a0f) installed at `repos.verse` on 2026-10-09 10:04:33Z. The install succeeded (`repo-charters.verse.install.<tree>~status` says `charters: 3`; `running` names the tree; the minute livecheck says `installs`, `changes: 0`). The tag did not follow: `git ls-remote --tags origin | grep live-verse` is empty, and so is the Oct 5 empty-tree install's tag.

Where the tag asks went:

    verse data repo-charters verse out git
    …
    installed:
      repo-charters:
        verse:
          install:
            4b825dc6…~status:
              4b825dc6…: {create: true, sha: bb69c22c…, to: tag.live-verse/4b825dc6…}
            a4621a0f…~status:
              a4621a0f…: {create: true, sha: c1bc057a…, to: tag.live-verse/a4621a0f…}

The `installed` rule's tag derive (system/repocharters/charter.tony.tmpl) is keyed, `key: $subject.value.tree`, so it stands under (rule, subject, key): `out.git.installed.repo-charters.verse.install.<tree>~status.<tree>`. The rule's subject is the install's STATUS, whose name ends in `~status`, and the sink's walk over an outbox skips every name ending in `~status` without descending (io/out.go, `walk`: "a status, or one nobody asked for any more"), as docs/reference/io.md says: the sink never reads a name that ends in ~status as an input. So the tag input is never offered to the connector, gets no `~status` of its own, and no tag is pushed. The two sibling derives in the same action are named (`id: $subject.value.tree`) and land at `out.github.<tree>`, where they are read.

The fix is the same shape as the siblings: name the input by the tree, `id: $subject.value.tree`, so it stands at `out.git.<tree>`. The once-only tag survives the change: a later commit carrying the same tree rewrites the input with its sha, and the io, asked with `create: true`, leaves a standing tag alone (io/gitout.go). The versed test `tagInputFor` asserted the nested address and moves to the named one.

Related: 455pm5kvh12kr2dts1n0 (the pull request that exercised the path).
