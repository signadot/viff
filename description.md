# verse status: the local runner section is asked live

Follow-up from the #59 review (smaller points).

`verse status`'s runner section returns the stored description whenever the registration is described (versed/localrunner.go, `e.Current()`), and asks the runner only when it is undescribed. That ask then blocks `/status` for up to 3 s.

Because `respawn` keeps the url, a runnerd crash-looping on its own port (a bad binary or config) never has its description cleared. Status keeps saying `described: true` with its offers, and no reason why.