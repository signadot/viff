# proposal: repo charters 3: the flow — verse decides with an ask when a change is ready, installs and tags

Part of j5vbwvja. **Built** (on main as of 3f2f5fd5).

**Verse decides with an ask when a change is ready, installs it, and tags the install.** The gate is the install, not the merge. Approval is keyed to the tree of the charters folder, not to the path the change took to main.

## As built

- **Ready means two things:** the ask settled `proceed`, and verse's charter check finds the tree installs over what runs. The check is `POST /charter/check`, carried by the git iod.
- **The ask** is a call-list task whose one call is `ask`, so it is on the docket like any ask, answerable from the CLI, Slack or the pull request. Its answer is written onto the candidate. Verse acts on it by its option, read anchored, so a person's words never read as a choice.
- **The normal path:**
  1. A pull request to main is asked about, and held until ready.
  2. People merge.
  3. Main then carries a tree already answered, so `verse/live` moves to it and it installs, with no second question.
- **Any other path,** a push, a pull request merged around verse, or CI: the change is put to the same ask on a pull request onto `verse/live`, whose diff is what is on main against what runs. Verse never merges. A yes moves `verse/live`.
- **Tagging:** the git iod tags `live-verse/<tree>` once, at the commit a tree first ran from. Tags are records, not triggers.
- **A merge, push or install made around verse** puts nothing live. It is a candidate, and it is asked about. `verse/live` not matching what is installed is an issue.
- **Questions nothing proposes any more** are settled `obsolete`, and asked again if their tree is proposed again.

## Open

- **What ask decides that a change is ready** is open; nothing about vetoes is concluded. The ask built is a plain `proceed`/`deny`/`obsolete`, with no waiting period or silence rule.
- **Does a change that reached main unasked install on silence,** or wait for a spoken yes?
- **A deny is final for its tree.**
- **Should only verse make `live-verse` tags?** (Tag protection.)
