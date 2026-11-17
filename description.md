# runner: a workspace holding a read-only tree is not removed

A task that builds Go code leaves a module cache under its workspace (`go/pkg/mod/…`), whose directories Go makes read-only (0555). When the task ends the runner removes the workspace with `os.RemoveAll`, which cannot unlink entries of a directory it cannot write:

```
delivery: <id>: remove workspace: … go/pkg/mod/…/.gitignore: permission denied
```

The workspace then stays on disk, and the channel reports it as kept, though nothing in it is loose. The task directory under the runner's work root is removed the same way, with its error dropped.

Removal of a tree a task wrote in should make the tree's directories writable to the runner first, so what a task did to its own modes does not decide whether its directory goes.