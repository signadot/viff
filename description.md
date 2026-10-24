# verse account -link github: link from a comment on any watched issue or pull request

**Today.** `verse account -link github -account <login>` claims the account and prints `/verse link <nonce>`. It says the line works "on GitHub, a comment on a pull request the io reflects". The CLI then ends, so the only route that binds is the github iod's `heard`. The iod reads the line only in replies to its own question comment, on a pull request with a question open (`answerReplies`, io/github/asks.go).

Reported in Slack (C0C3KHG9B0W, p1792682822435309):
- Ani's comment on signadot/verse#38 never bound: #38 had no question.
- A comment on #24 did bind: changing `.verse/charters` raises a question there.

Verse already has the route that works anywhere. `POST /accounts/github/verify {nonce}` binds from a `/verse link <nonce>` comment on any issue or pull request that a trusted github iod reflects, and docs/reference/perms.md describes it. Nothing calls it.

**Decided (see the plan comment):** the CLI finishes the link through `verify`. After printing the line, `-link github` asks `verify` until the comment is reflected and the account is bound. It ends when the claim expires, or when a binding made through the iod's `heard` stands. Its wording names only GitHub's venue, any issue or pull request in the repositories verse reflects, and lists them. The docs drop the "where the github iod is asking a question" condition. No versed or iod change.

**Done when:** `verse account -link github` followed by a `/verse link` comment on any issue or pull request in a watched repository ends with the account bound. The command's wording says where to say the line.

Not in this issue:
- the refusal for a nonce said through another provider (Judy's GitHub nonce said in Slack);
- the workshop text.
