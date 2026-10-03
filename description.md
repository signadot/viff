# verse/issue-next

Replace the per-issue triage judge (deploy/charters/verse/repo/verse/issue-triage.tony) with
issue-next: a sweep every three hours over verse's open git issues.

The judge spawned one coderunner task per issue event, which burns tokens on every comment and
label, and storms on install or restart (p0earts9h12ksy8nmxn0).

Plan, in order:

1. (this branch, first) issue-next writes a STUB proposal per open issue at
   `acme.git-issue.next-proposal.<id>`, with the label that proposal implies. It is rules
   only, with no task and no tokens. issue-triage, its seed script and its tests go.
2. a git-issue io out half (iod -out), so the proposal's label is applied to the issue.
3. the processing task: one coderunner checkout per sweep, which takes each changed issue to its
   proposed next step and proposes the step after. At most one `build` per sweep.
4. a reactive charter for issues labelled `question`. It fires when an issue carrying the label
   moves (a new issue, a comment, a close) and when the label is added or removed.

Label vocabulary: `verse-next=<verse|world>:<analyze|design|plan|build|push-build|report-build>`.