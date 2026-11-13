# verse agent: a rate-limited model call fails the task without a retry

A task run by the `verse` agent ends on the first rate-limit response from the model, and its error says it tried three times.

## What happened

On staging, 2026-11-03 18:15 UTC, the comment-drift review of the verse repository
(`repos.verse.drift:review-range`, range `79070320..13e46605`) failed with:

```
the model call failed 3 time(s) and this task cannot continue without it: api: anthropic error:
This request would exceed your organization's rate limit of 10,000,000 input tokens per minute …
```

A failed range holds the drift cursor, so nothing was reviewed for ten days.

## Why

`pkg/runner/runnerd/verse/llm.go`:

1. **The body is read before the status.** `complete` returns `api: <message>` as a plain error
   whenever the response body carries an `error` object. Only the branch below it marks 429 and
   5xx as `transient`. A rate limit arrives with an error body, so it is never transient and
   `completeWithRetry` stops at the first attempt. The same holds for a body that does not decode,
   such as a gateway's HTML 502.
2. **The count is a constant.** The final error prints `len(modelBackoff)+1` whatever the number
   of attempts made, so a call tried once reads as tried three times.
3. **The waits are shorter than the limit's window.** The schedule is 2s then 8s. The limit is per
   minute, so a retried call meets the same window. `modelTotalBudget` is eight minutes and the
   waits use ten seconds of it.
4. **`Retry-After` is not read.**

## The fix

Inside the loop that is there:

- the status decides whether a failure is transient, whatever the body holds, and the error names
  the status;
- the schedule spans a rate-limit window, with jitter, since the limit is the organisation's and
  several tasks meet it together;
- a `Retry-After` on the response is the least the next wait is;
- a wait the budget cannot cover is not begun;
- the error says how many attempts were made.

## Not covered

- Whether Portkey passes the provider's 429 and its `Retry-After` through is not checked here. If
  it answers 200 or 400 with the provider's error in the body, the status says nothing, and the
  error's `type` would have to decide.
- The `claude` and `codex` agents run their own CLIs, which retry themselves.
- The charter-level retry of a failed range stays manual.