# versed: the package doc still calls the task runtime an unmounted scaffold

The package comment at the top of `versed/server.go` ends with a "scaffold status" paragraph saying the task runtime is not yet mounted and spawning actions no-op until it is wired. It also says the server assembles "a gate registry".

Neither holds: the task daemon is a field on `Server`, `/tasks` is mounted, three startup phases reconcile and resume tasks, and nothing in the struct is a gate registry. A newcomer running `go doc versed` meets the scaffold first.

Rewrite the package doc to what the server is: the monolith assembling store, engine, task runtime and the authentication door behind one HTTP surface, restored in phases at start. Comment-only change.