# flow show renders a packaged or scoped subject with no shape: the tree is built against the asking engine's charter, not the trigger's

`GET /flow/tree` (versed/server.go flowTree) resolves `engFor(scope)` and builds the tree there. The mirror it reads is shared, but the trigger NAME is resolved in that engine's own charter — so baseline renders a subject whose work a package's rule started with no hooks and no pending branches, only history.

A union of engines is the wrong fix (one mirror, two engines, the subject rendered twice). The tree wants building against whichever engine Knows the trigger the mirror names — which `flowEngines`/`charterHolders` in versed/owns.go can now answer — and, under 'a scope owns what it creates' (docs/reference/connect.md#what-a-scope-owns), a scoped `flow show` of a subject only baseline is working should say so rather than render an empty tree.

Left over from two earlier (since-closed) issues.