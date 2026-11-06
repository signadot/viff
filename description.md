# slack tools: the request to the runner names no decision

Found in the review of 1k5art3x. It is on `main`, and that change did not make it.

## What is wrong

Every tool that asks the runner to act names the decision that let the call through
(`Decision: tu.Decision`): the repo tools, the issue tools, and the verse tools (`toolOf`,
`pkg/runner/runnerd/stock/versetools.go`). The Slack tools do not
(`slackTool`, `pkg/runner/runnerd/stock/slacktools.go`). Their request has an op, the channel
and the text, and no decision.

## What it does

Under an autonomy row that asks about `slack.post`, the runner looks for the answer it relayed
(`Server.refused`, then `Channel.answered` in `pkg/runner/runnerd/delivery/channel.go`). With a
decision's id it matches the answer by that id. With none it matches by the step and its
fields.

- **Two gated posts to one channel are the same step.** The fields of a post are the channel
  alone. An act can be performed on the answer that was given to the other question.
- **Where the runner kept no step with the answer,** nothing matches, and the post is refused
  ("the act names no decision") though a person said proceed.

Until 1k5art3x the client sent one request at a time, so the first case needed two answers
standing at once. With requests named and sent side by side it is easier to reach.

## The fix

`slackTool`'s `Do` sets `Decision: tu.Decision` on its request, as the others do.

## Tests

- A script's post under an asking row (`TestAScriptPostsToSlackUnderAnAskingPolicy`,
  `slacktools_test.go`) already runs such a post. It should also hold that the request carries
  the decision's id.
- Two gated posts to the same channel, answered in the other order: each is performed on its
  own answer.