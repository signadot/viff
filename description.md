# repo-charters: the charter check's refusal is unreadable on the pull request

On signadot/verse#2 (2026-10-05), the charter check refused the charters three times in a row, and each time the author could not tell what was wrong or where to look.

What the pull request showed:
- `verse/check` failed with "would be refused; see the comment". The comment is verse's first one, edited in place at the top of the conversation, so a new refusal changes text far above where the author is reading, with no notification. "There's no comment" was the first reaction.
- The refusal is the engine's own sentence, written for someone who knows the grammar, not the file. For example:

  ```
  trigger "proposal-appeared": derive into repos.verse.merge.proposal matches no stratum: every derived entity has to belong to a level, and there is none that owns repos.verse.merge.proposal (declared strata: [reflected=0 [git] merge=30 [repos.verse.proposal] train=40 [repos.verse.train]])
  ```

  It names a rule, not a file or a line. It reports one fault at a time, so a tree with three faults takes three pushes and three rounds of about two minutes each. And it says nothing about what to change.

What would make it readable, to be decided:
- Name the file, and the line where the parser knows it, of each fault: `.verse/charters/comment-drift/merge.tony`, rule `proposal-appeared`.
- Report every fault the check finds, not just the first.
- Lead with what to change in plain words, with the engine's sentence after it as detail.
- Put the refusal where the author is: a reply, or a fresh comment, per refused tree, rather than an edit at the top. Or point the status's link at the comment.

Related: verse/repo-charters.