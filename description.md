# github iod: hear `/verse link` on any pull request it reflects, not only in replies to its own question

**Today.** The github iod reads `/verse link <nonce>` only in `answerReplies` (io/github/asks.go). That covers only replies after the iod's own question comment, on a pull request where an input carries `asks`. It stops reading once every question there is settled.

So linking a GitHub account works only on a pull request where verse has an open question. `verse account -link github` says otherwise: "a comment on a pull request the io reflects" (cmd/verse/login.go). The staging workshop's "a pull request that verse watches" says the same.

Reported in Slack (C0C3KHG9B0W, p1792682822435309):
- Ani's link comment on signadot/verse#38 never bound, since #38 had no question.
- A comment on #24 did bind, since a change to `.verse/charters` raises a question there.

docs/perms-guide.md states the narrow rule correctly. The CLI and the workshop are wrong about it.

**What it would do.** It would hear a `/verse link` line in any comment on any pull request of a repository it reflects. It would report it through `POST /accounts/github/heard` as today, and reply once with the outcome. It would no longer depend on a question being open. The comments are already reflected per repository, within the `-comments` window, so the iod would act on what it reflects rather than on the asks path's replies. Each link comment acted on would be remembered, so a comment is answered once, as `handled` does for answers.

**Done when:** a `/verse link` comment on any pull request in a watched repository binds the account and gets one reply. The CLI's wording is then true as written. docs/perms-guide.md and docs/reference/iod.md would drop the "where the github iod is asking a question" condition.

Not in this issue, though suggested alongside it in the same thread:
- the CLI naming only the linked provider's venue;
- a refusal that says a nonce belongs to another provider, as with Judy's GitHub nonce said in Slack;
- the workshop text.