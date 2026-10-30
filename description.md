# A pre-#59 settled question met by another run of its id is not posed

Seen reading 6de161210 (the fix for review points 3 and 4). It's a transitional edge, so low priority.

A question record written before #59 names no `posedRun`. PostFrameQuestion takes any frame met there for its replay. That's the safe side for a live task re-attached after the deploy.

But a NEW run of the same task id (rule ids `<chain>@<subject>` re-fire) restarts its question counter. Its first question can land on that old, settled record:
- `again` is false, so nothing is cleared;
- the `!not` precondition refuses the write over `answered`/`orphaned`;
- `finishSettle` does nothing on a settled record.

So the new run's question is never posed, and the task waits on it until its deadline, or until its owner steps in.

It only bites for ids whose pre-#59 record is settled and that run again after the deploy. One way to tell the two apart: compare the record's task `startedAt` with the frame's run when `posedRun` is absent.