# D3 for a question a task on a runner raises: nobody is asked about what they can't read

Named in #59's "Not in this PR", and in the review's smaller points.

The task registry on runnerd has no `OnCheck` (runner/runnerd/inprocess.go), so the D3 audience rule is not applied to a question a task raises. Before #59 it was wired only for in-process tasks, so on staging it was already absent. Since #59 it is absent for `verse up`'s local tasks too.

Exposure:
- A task's `question-ask` takes any `answerers` (`user:*`). `poseRecord` grants them the question record (reason, action, fields, subject), so a task, or a prompt injection in its subject, can put subject content on anyone's docket. The check at answer time refuses their answers, but they can read it.
- `Launch.Timeout` pauses while a question is pending. A question posed to people who may not answer it holds the task until its owner steps in or a deadline settles it.