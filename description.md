# issue triage should never try to do everything on re-populate/start

issue triage is organised incorrectly, making it so that any restart
causes a storm of activity that must settle just to get to "current" state.

2 possibilities:
1. make it batch implicitly and work on a selection which may be large or small
2. when there are many issues, limit the total number of active at one time.