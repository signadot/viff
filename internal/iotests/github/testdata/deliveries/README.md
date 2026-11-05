# Captured GitHub deliveries

Real webhook payloads, replayed through the handler by `TestCapturedDeliveries`. One file is
worth more than a page of reasoning about field paths.

One synthetic delivery is checked in, and it is synthetic on purpose: it exercises the harness
so this is not scaffolding that first runs the day somebody drops a real payload beside it. It
is not evidence about GitHub's JSON — that is what the captured ones are for, and the first real
one should replace nothing but the doubt.

**Why this exists.** The half of a webhook that can be wrong here is whether the handler reads a
real payload correctly: `issue.pull_request`, `workflow_run.id`, `repository.full_name` are
guesses about somebody else's JSON until one arrives. The other half — whether GitHub can reach
the endpoint — needs a public address and tests GitHub's configuration rather than this code. So
the risky half is made testable without one.

**A wrong field path does not fail loudly**, which is the whole reason for the assertion the test
makes. `hintFor` falls back to the repository when it cannot read a number, so a payload it
misreads still hints *something* and still converges — just eight requests instead of one, on
every delivery, forever. The test therefore requires a hint FINER than the repository for every
event whose kind carries a number, which is exactly what a misread degrades away from.

## Capturing one

No public address needed: GitHub keeps recent deliveries per hook and will hand them back.

```sh
gh api /repos/{org}/{repo}/hooks                       # find the hook id
gh api /repos/{org}/{repo}/hooks/{hook_id}/deliveries  # find a delivery id
gh api /repos/{org}/{repo}/hooks/{hook_id}/deliveries/{delivery_id} \
  > internal/iotests/github/testdata/deliveries/issues-opened.json
```

Save the response **verbatim** — the test reads GitHub's own delivery envelope (`event` and
`request.payload`), so nothing has to be edited or trimmed. Scrub anything private first; these
are real payloads and they land in the repository.

To pin an exact ref rather than only "finer than the repository", add a `verseWant` key at the
top level of the file:

```json
{ "event": "issues", "verseWant": "github.acme.app.issue.\"42\"", "request": { ... } }
```
