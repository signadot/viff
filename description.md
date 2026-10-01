# proposal: repo charters 1: a system charter that keeps the approved charters installed, records what runs, and its place in the charter tree

Part of j5vbwvja. **Built** (on main as of 3f2f5fd5).

**What runs is the last state of a repository's charters folder on main that was answered yes.** It is kept by a system charter verse ships, `system/repocharters`: one package per repository at `repo-charters.<repo>`, rendered from the config's `repoCharters {repo, github, at, tree, answerers}`, and installed as the system once the git and github iods are registered.

## As built

- **States kept apart:**
  - main, `state.main {sha, tree}`;
  - `verse/live`, `state.live`;
  - one candidate per tree ever proposed, `candidate.<tree>`, with its answer and check;
  - what runs, `running {sha, tree}`.

  A tree is the git tree of `.verse/charters`, reflected by the git iod's `-tree` on every ref. An absent folder reflects as git's empty tree.
- **The install follows `verse/live`.** A yes on main's tree makes the git iod, pushing as the GitHub App, make or move `verse/live` to main's commit. The git iod installs the tree `verse/live` holds, if it was answered yes, at the configured address with prune, as itself. A commit that leaves the folder alone installs nothing. A rollback to a tree answered before installs with no second question.
- **The installed record:**
  - `running`, written from the git iod's status for `verse/live`'s commit;
  - `live-verse/<tree>`, tagged once, at the commit a tree first ran from.
- **Placement:** each repository installs under its own address, `repos.<repo>`. Every charter in the folder must be at or under it, or the check refuses the tree.
- **Drift:** the installed charters are checked against `verse/live`'s tree every minute, and an issue stands while they differ or `verse/live` is gone. Nothing is reverted.

## Open

- **Who the installed charters run as.** Decided, not built: a dedicated deployment user, with read-write for the repository's admins.
- **`verse charter repo [diff|install] [-C <path>]`,** with a bare `verse charter repo` listing. A repository's charters behave differently under the CLI verbs than other charters. Not built.
- **x4pfb0zf** (a whole-verse round trip refused over system-held records): whether a prune under a repository's address meets it is unchecked.
