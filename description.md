# versed: a task's ask hands its then and else no charter-install

A task's charter is held within the task less `verse.charter-install` (86259b702), so nothing a task installs installs charters.

An ask the task raises (`verse-ask-create`) gets no such limit. Its `then` and `else` spawns are held within the task's environment as it stands (taskBound.holds). A task given `verse: {}` can therefore follow a yes, or a no, with a spawn that installs charters, and that spawn's asks can do the same. The chain through asks has no bound.

Fix: taskBound.holds holds a spawn's environment within the task's less `verse.charter-install`, as the charter hold does. The check is at the raise, which covers `then`, `else`, and the `then`/`else` of a `then: {ask}`. A `then`'s spawn is written by hand (CheckByHand) and never bound, so the raise sees it as it will run.