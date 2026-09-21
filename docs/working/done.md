# Done

Work that landed, so its note could go. A headline and the commits — the prose is in the
history, and what is true now is in [`docs/`](../README.md).

A line is added when a note leaves: the note is deleted in the same commit that adds the line,
so `git log -- docs/working/work/<name>.md` is the rest of the story. What a landing did *not*
build stays behind as a residual ([residuals.md](residuals.md)); this file is only the part that
is finished.

A note can also be finished by being overtaken — what it proposed is no longer what anybody
would build. That is a row like any other, with no commits and a line saying what serves the
need instead, because a reader looking for it deserves to find out that it is settled rather
than that it is missing.

| what landed | commits | described in |
|---|---|---|
| absence is `!delete`, so a bare null is data again | `baa0c459` `d9819b88` | [reference/store.md](../reference/store.md), [change-events.md](../change-events.md) |
| the `calls` driver: a charter's own list of tool calls | `467cbf1b` `6b848c04` | [reference/tasks.md](../reference/tasks.md), [trigger-model.md](../trigger-model.md) |
| `!each`: one answer holding many facts | `53791863` `adcb41df` | [strata.md](../strata.md) |
| the entity tree, superseded by one path model | `f6423e74` `aa2e5e96` | [reference/entities.md](../reference/entities.md) |
| the grantee grammar, spelled once everywhere | `79e5af24` `6e3ee884` | [perms.md](../perms.md), [perms-guide.md](../perms-guide.md) |
| the loop as a state machine, with its contract and its input precedence | `d3669047` `848252df` | [task-control.md](../task-control.md#the-loop-is-a-state-machine) |
| one path grammar for addresses, and `..` | `70217850` `f6423e74` `c2924703` | [reference/entities.md](../reference/entities.md) — the model moved there, descent marked pending |
| the paths listing, answered by the store | `86d6cd28` `6678028b` | [reference/entities.md](../reference/entities.md) |
| the root record, the admin group and the fence | `7a896521` `139d7bfe` `2a763c2c` | [perms.md](../perms.md), [reference/up.md](../reference/up.md) |
| what a scope owns, and a mirror per scope | `4a6d2034` `ba0da24e` | [reference/connect.md](../reference/connect.md), [perms.md](../perms.md) |
| task gates as one question record, answerable by a binding | `ee2e4c7c` | [attached-resolvers.md](../attached-resolvers.md), [reference/tasks.md](../reference/tasks.md) |
| `verse connect`: a working tree federated into the verse | `2e552901` `931fd51b` | [content-federation.md](../content-federation.md), [reference/connect.md](../reference/connect.md) |
| `verse state` is `verse data`, and its verbs are flags | `c92dd96f` `6fd9003b` | [reference/entities.md](../reference/entities.md) |
| the merge gate drive: a push as a capability, gated | `c8a6cf3c` `d53a9219` | [reference/examples/README.md](../reference/examples/README.md) |
| the `verse-dev` drive: verse coordinating verse, which bootstrapped the dogfooding | `2e552901` `c8a6cf3c` `d3669047` | the charters under [deploy/charters](../../deploy/charters/README.md); what it left is [the verse dev residual](residuals.md) (`rd9c5f8r`) |
| the coderunner drive: twelve findings from running a foreign agent under verse, each by a run that hit it | `2e552901` `c8a6cf3c` | [reference/examples/README.md](../reference/examples/README.md#running-the-coderunner); the fixes landed and the two that did not are [residuals](residuals.md) (`1qdmzhac`, `e3b4qmac`) — finding 11, why the drive moved to `driver: llm`, is the reasoning behind [task-control.md](../task-control.md)'s guarantee table |
| sharing a thing by owning a place inside it: a claim is an address, and the last owner's delete climbs | `c856163f` | [sources.md](../sources.md) — what it did not build is the container above a source's claim nodes, `zf1ep7wr` |
| a charter installed at an address, confined to it, with strata per package | `1943a7b2` `c8c67a08` | [trigger-model.md](../trigger-model.md#a-charter-can-be-installed-at-an-address) |
| who owns the base slices — the question charter spaces raised and did not answer | — | ownership is a place now, so the answer is [perms.md](../perms.md)'s rather than a namespace's |
| the permissions notes — attribution as a parameter at install, the root record's reach, one owner per node | — | built, in the rows below; getting it onto staging is [work/deployment.md](work/deployment.md), and what it left is `zf1ep7wr` |
| every node has an owner and the root is one of them: `user:admin@verse` owns the root, `owner` is held out as `child-owner`, a request that writes names who makes it, a place nobody owns acts as nobody | the commit that added this row | [perms.md](../perms.md), [perms-guide.md](../perms-guide.md), [reference/up.md](../reference/up.md) |
| a registry's address is a name: `verse.runners.<name>`, `verse.resolvers.<name>`, `verse.sources.<name>` with a `root`, and what watches a question at `verse.watch` | the commit that added this row | [reference/runners.md](../reference/runners.md), [reference/resolvers.md](../reference/resolvers.md), [reference/sources.md](../reference/sources.md), [reference/sourced.md](../reference/sourced.md) |
| a `child-owner` line confers `owner` on a creation and on nothing that stands: the write carries the absence condition, the store settles it, and the seed holds `read` out beside each creation row | the commit that added this row | [perms.md](../perms.md), [reference/perms.md](../reference/perms.md) — measured first, and the grant itself is not re-checked at the commit (`zf1ep7wr`) |
| what the system makes in verse's half carries the system's record, because a record also stops the grants above it — deleted as redundant, and put back when a review showed what it was stopping | the commit that added this row | [perms.md](../perms.md), [reference/perms.md](../reference/perms.md) |
| a place that acts is installed as somebody's or not at all, and whose a rule is, is said once, on its owner record (`by` and the engine's own account of installers went) | the commit that added this row | [perms.md](../perms.md), [reference/perms.md](../reference/perms.md) |
| no rule but the system's writes under `verse` | the commit that added this row | [reference/entities.md](../reference/entities.md), [perms.md](../perms.md) |
| a worker's frame does not say who answered, what they chose, or who slid the vector: a relayed decision is settled from what verse holds | the commit that added this row | [perms.md](../perms.md), [reference/perms.md](../reference/perms.md) — the slide's actor is remembered by task id alone (`zf1ep7wr`) |
| a scope that holds the whole mirror hears of baseline's record when it moves, rather than when its watch says so | the commit that added this row | [perms.md](../perms.md), [reference/perms.md](../reference/perms.md) |
| what a node created beneath holds out is `child-grants`, named for what it is beside `child-owner`; it was `default`, which promised a fallback | the commit that added this row | [perms.md](../perms.md#grants), [reference/perms.md](../reference/perms.md#records) — a store is migrated at start, a config file is not |
| charter funcs — done by being obsolete | — | the call node, `$args` and named fragments were written before packages and places; the need they named is better served from the world half, by a structured defs node anything can refer to |
| the charter command: one operation at a time, a path into a charter, and `rules:` | `f4ad92e9` | [reference/connect.md](../reference/connect.md), [reference/entities.md](../reference/entities.md) — the refusal that should name a path is `wxgxnd13` |
| every call on the store has a budget, and readiness answers from the process alone | `6b018743` | [reference/up.md](../reference/up.md) — what boot still does its own way is `pd32ag16` |
| a run leaves a record, addressed `<id>@<occurrence>` | `76da3b25` `565dad24` | [reference/tasks.md](../reference/tasks.md#one-id-many-runs) — the listing answered what a `--history` flag would have |
| a path read may carry a predicate: `!list-paths` takes `value:` | `77af10c9` | [trigger-model.md](../trigger-model.md) — the bound it still wants is `vd1egj16` |
| a depth is counted from the address, and 0 is the address alone | `596d2891` `386bee3e` | [reference/entities.md](../reference/entities.md#--depth-how-far-beneath-the-address) — the store-side descent is `qftkwm16` |
| the broker's retained window is the charter's to set, so a gap frame means something | `3675f8fd` | [reference/tasks.md](../reference/tasks.md) |
| strict-mode parsing — done by being obsolete | — | an unknown field is named before a document is decoded, per authoring route (`CheckFields` for a rule, a runner, a source, a binding), rather than by threading a strict option through a generated codec |
| the open questions note — one answered, one filed, one moved | — | who may write a field is [perms.md](../perms.md)'s; a capability nobody provides is `5m80ff13`; tracing a flow is `0p5tgch7` |
| the seams: a control call says which failure it was, a reader resumes, a claim is a lease | `3675f8fd` | [reference/runners.md](../reference/runners.md), [task-control.md](../task-control.md) — what is left is `04xtth11` (keepalive), `cjx20k11` (a refused write is invisible), `mrm86n11` (an undelivered answer on the docket), `mt36ses1` (what Handle should carry) |
