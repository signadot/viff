# scopes: a scope name nobody made opens a store session and an attribution, and nothing ever deletes them

Any request may carry `?scope=<name>`, and the name does not have to be a scope anybody made.
`Server.scopedStore` calls `s.Store.Scope(name)`, which for a name it has not seen:

  - `LogdStore.Scope` (entity/logdstore.go:2003) calls `OpenLogdStore` with
    `ClientID: <parent>-scope-<name>` — a session of its own against logd/docd — and keeps the
    child in `s.scopes[name]`;
  - `attributed.Scope` (entity/attribution.go:453) makes an `Attribution` for the name through
    `Attribution.scope` and keeps it in `children[name]`, with its own record cache, places and
    fence tries, group memo and counters.

Both are freed in one place only: `POST /scope/delete` -> `LogdStore.DeleteScope` and
`Attribution.DropScope` (versed/server.go:1522). A name that was only ever READ under is never
deleted, because nobody made it and so nobody deletes it. One GET per distinct name is one
session and one attribution for the life of the process, and a GET names nobody, so the door
(`Verse-Principal is required`) does not stand in front of it.

What should happen — to be decided here, not assumed:

  - a scope name that does not exist should not open anything: answer it as baseline read
    through no overlay, or refuse it (404 "no such scope"), but do not mint state for it. That
    needs a way to ask whether a scope exists, which verse does not have today (scopes cannot be
    enumerated — the reason the `child-owner` migration is baseline-only);
  - failing that, children that hold no engine and have not been used for a while should be
    closed and dropped, both halves together (the store child and the attribution), so that the
    two maps cannot disagree about which scopes stand.

Related, read and NOT tested:

  - `LogdStore.Scope` answers `return s // degrade to baseline` when the child cannot be opened.
    A request that named a scope is then served by BASELINE: a read shows baseline as though it
    were the scope, and a WRITE meant for the overlay lands in baseline. If that is reachable it
    is worse than the leak; it wants a probe (make OpenLogdStore fail for one name) and either a
    refusal or an error in its place.
  - whether a first read under a new name creates anything durable in logd, or only the session.

Context: cfe5ca21 made a record that moves in baseline drop from every unwatched scope's cache
(`dropInScopes`), so these children no longer go STALE — they are still never freed, and every
baseline record move now walks all of them.

Before closing, write down here: what a request under a name nobody made answers, what frees a
child that is no longer used, and what the degrade-to-baseline branch does under a probe.