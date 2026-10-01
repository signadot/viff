# proposal: system charter for repo charters

# proposal: system charter for repo charters

**Built.** It is on main as of 3f2f5fd5. The issues under label `system-repo-charter` track the in-repository work.

## The idea, as built

A repository keeps its verse charters in `.verse/charters/` next to its code. **What runs in verse is the last state of that folder on main that was answered yes,** however it reached main.

- The question is asked where the change is reviewed, on the pull request to main.
- Verse holds the merge with its own review until the question is answered yes and its charter check passes. People merge.
- A change that reached main unasked is asked about on a pull request onto `verse/live`, the branch verse keeps at what runs.
- Approval is keyed to the folder's git tree, so a yes stands for exactly those charters.
- The install follows `verse/live`, and the tag `live-verse/<tree>` records each install.

## The parts

1. **The reconciler and what runs** (j9vbwvja): built, as `system/repocharters`.
2. **The review environment** (jdvbwvja): built for GitHub, as the reconciler's own rules over the github iod. There is no separate review package.
3. **The flow** (jhvbwvja): built. A change is ready when the ask settles `proceed` and the check passes.
4. **A trial run** (jnvbwvja): not built.

## What we'd like to discuss

- **The ask:**
  - What ask decides that a change is ready? Nothing about vetoes is concluded.
  - Does a change that reached main unasked install on silence, or wait for a spoken yes?
- **Tooling:**
  - Should only verse make `live-verse` tags?
  - Gerrit or GitHub first, and a GitHub App iod.

## Gaps filed

yjn5qagj, f80cejye (scopes); x4pfb0zf (a whole-verse round trip refused over system-held records); 2dchqs8j (a charter authored per change); he301t4x (a task edits the running charter, gated); 84a83gkw (outboxes, built); nj9fce9dh (a merge's lost status read as a merge around verse); g32ed7ha (the check's refusal is unreadable).
