# A person's ask takes options, and `then` runs on one of them

## The problem

A person's ask is yes/no, and nothing says why. `api.Ask` has no options, `verse ask` has no flag for them, and neither does the MCP's `verse_ask_create`. An ask's record carries options only as an onward ask, copied from the ask it serves (versed/asks.go). A task's own question has them (`question-ask -options`), and so does an autonomy row's ask, so a person can't ask what a task can.

## What decides `then` and `else` today

By the chosen option's id, not its place: `followField` (versed/consequence.go) runs `then` when the choice is `proceed` and `else` on any other. On a yes/no ask the options are `proceed` and `deny`. On an ask with options of its own, none would be `proceed`, so `then` would never run.

So `then` and `else` already mean "on option X" and "on any option but X". What is open is which option X is:

- **The first option.** The order of the options then carries meaning.
- **The one the ask names**, defaulting to the first.

## What it touches

`api.Ask` and `askRecord`; `verse ask` and `verse_ask_create`; the answer route (a choice among the options, and words where the ask takes a reply); `onSilence` (silence settles on an option, as `question-ask`'s `default` does); followField; the docket and Slack's rendering; asking.md.