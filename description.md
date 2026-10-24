# proposal: the chat face — a mention in Slack starts governed work as the person who wrote it

Status: a proposal, for discussion. The chat face of the three-faces proposal (55crejnfh12ksb4kq5n0), which listed it as not filed. It takes over the Slack conversation stub (1yhjy61ph12krkxjpdn0).

## The use case

A thread in Slack, 2026-10-23: somebody writes "@Claude evaluate our current setup and see if we can use this. If so open a PR and send to Adewale and Naomi for review." David asked for the same through verse: a mention in a thread starts the work, the work is governed, and the people named are asked.

## What exists

- The Slack iod reflects every message in its window at `slack.<ws>.message.<C>.<ts>`, with its text, author, thread and reactions. A charter can select a message that mentions the app.
- A task has `slack-read`, `slack-reply`, `slack-post` and `slack-react`, `git` and `github create-pr`, and `question-ask`. A question to named people reaches them as Slack DMs through the deployed gate-ask charter, and their reply in the DM answers it.
- An account binding (`verse account -link slack`) says which principal a Slack member is.
- `verse mcp` (#35) is the user face: a server a host runs as one person.

So a charter over mentions, spawning a claude task with those verbs, would do the sentence above end to end today, with the review request as an ask to the two reviewers.

## What is missing

1. **Identity.** The three-faces rule for the chat face is that the work acts as the person who wrote each message. A charter's spawn acts as the charter's owner. The binding says who the author is; nothing lets a firing act as them. A task that opens a PR and asks two reviewers under the charter owner's name is what the rule exists to prevent.
2. **The loop.** A follow-up in the thread does not reach a running task. The out half carries question replies back and nothing else. The task's `say` is the mechanism on the task side; the thread is the place (n85fbhrjh12kr1s0pnn0, channels).
3. **The face.** 55crejnf's open question 4: is the chat face an MCP server at all? The task face grew as runner tools, not a server (its comment of 2026-10-20). The chat face can likely grow the same way: a charter and the runner's slack tools, with the two pieces above, and no server. A server is needed only for a bot that runs outside verse.

## Proposed

Smallest version first, in this order:

1. A spawn may act as the author of its subject where the subject is a reflected message whose author is bound: `as: $subject.value.author`, honoured only for a binding the system wrote, refused otherwise. The task's asks and writes carry that principal. This is the chat-face rule, applied at the spawn.
2. A charter over `slack.<ws>.message.*` mentioning the app, in a deployed package, spawning a claude task on the claude runner with slack, git and github verbs, autonomy asking before `create-pr`, and a prompt that reads the thread with `slack-read`. The first use is the sentence above.
3. The loop: a reflected reply in the task's thread, by a bound author, is said to the task (`say`) while it runs. Whether the thread is then a channel in n85fbhrj's sense is that issue's.

Charter generation, the other half of David's ask, is 2dchqs8jh12kr1s0pnn0 (an agent proposes, a person installs) with 3gf9wqb3h12kr2ymtnn0 (`verse-charters`) as its mechanism, and is not this issue.

## Open

- Whether 1 is a spawn field or a property of the package (every spawn of this charter acts as its subject's author).
- What an unbound author gets: a reply from the task saying how to link, or silence.
- Rate: a mention is one spawn; a thread of mentions must not be a spawn each.