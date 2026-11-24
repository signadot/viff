# trigger: firings for many subjects written at one revision fail on a read deadline, and are not retried

When a rule's condition matches many subjects that are all written in one store revision, its firings are evaluated together and their reads of the subject can time out. Each such firing fails:

    WARN a rule's firing went wrong rule=<rule> kind=failed chain=<rule>
      why="subject.value: reading <subject> at commit <rev>: read <root> at rev <rev>: context deadline exceeded"

## When it happens

Seen with a rule over every open pull request an iod reflects (`github.*.*.pr.*`), with a derive as its action, and roughly a hundred matching records:

- when the charter is installed, since every standing subject fires at once;
- whenever the iod writes many of those records in one revision, for example after it restarts and reflects again.

`viff status` counts them under `firings: {failed: …}`. The count is since viffd started.

## What it costs

A subject whose firing failed gets nothing derived for it until its record next changes. Nothing retries the firing.

## Questions

- Is the read deadline too short for this many firings at one revision, or are the reads serialised behind something?
- Should a firing that fails on a read deadline be retried, where one that fails on its own terms is not?
- Should such a charter pace itself (a schedule, or a batch) in place of one firing per subject?