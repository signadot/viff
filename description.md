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

The `installed` rule's tag derive (system/repocharters/charter.tony.tmpl, `path: repo-charters.{{.Repo}}.out.git`, `key: $subject.value.tree`) lands at `out.git.installed.<subject path>.<tree>`, which is what a keyed derive's address is (rule, subject, key). The io reads outbox entries at `out.git.<name>`, one level down, so these never get a `~status` and never reach the remote. The two sibling derives in the same action use `id: $subject.value.tree` and land at `out.github.<tree>`, where they are read.

Not touched here: this is the record of the first real run, filed for whoever owns the template. Related: 455pm5kvh12kr2dts1n0 (the pull request that exercised the path).