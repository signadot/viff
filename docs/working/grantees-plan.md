# Grantees: implementation plan

Status: plan, nothing built. The design is git-issue `2z4eef8sh12kszscmxn0` ("permission groups
lack a well founded structure") and the comment on it of 2026-09-13; this is how it lands. Read
the issue first — this file restates its decisions in one line each, not its argument, and then
gives the order of work and what each step must prove.

The shape being built, in one line: one grammar names a principal and a grantee — a principal is
`<kind>:<id>` or `via:<path>`, a grantee is a principal, `*`, `<kind>:*` or `group:<name>` —
parsed in one place and matched by structure rather than by the order of string comparisons; a
group's members are walked exactly, with a visited set and no depth.

```
principal ::= <kind>:<id>                 # user, agent, runner, system
            | via:<path>                  # a rule, a source, a binding: whoever acts through it
grantee   ::= principal
            | *                           # everyone, the unattributed request included
            | <kind>:*                    # every principal of the kind
            | group:<name>                # the group's members, each as themselves
            | <email>                     # shorthand for user:<email>, at the keyboard only
```

The plan is written for the end state, not for the steps: the store is wiped when this lands
and nothing carries a name anyone depends on, so no step keeps an old spelling readable and no
step is shaped to land smaller than the design.

## What the plan rests on

Each of these is a decision the issue's comment argues; the plan only names them so a step can
be checked against them.

- **A principal's name is in the grammar.** `owner: user:walter@example.com`, `by:
  runner:coderunner`, a firing writes as `via:verse.trigger.rule.x`, a source as
  `via:verse.sources.git.cluster`, a binding as `via:verse.resolvers.<kind>.<name>`, versed as
  `system:default`. The collision the issue names then cannot occur: `agent:agent` and
  `user:agent` are different strings, and nothing consults a listing to tell a runner called
  `coderunner` from a person called that. A record reads as what it is — today `owner:
  coderunner` could be either — and the door refuses by kind rather than by "parses as an
  address". The kind header goes, since the kind rides the name.
- **`system:<part>` says which of versed's own acts wrote.** Versed writes on its own behalf
  in six places, and each is a name: `system:docket` poses, settles, escalates and reowns a
  question and releases a binding; `system:mirror` projects a task and grants its claimant;
  `system:perms` writes the perms mirror's records — ownership taken, a grant added, `under`,
  who has been met; `system:seed` writes the deployment's policy at start; `system:charter`
  installs the charter the deployment stands behind, so its rules resolve to it; and
  `system:default` is what remains — the restore at start and whatever else names no part.
  The parts are attribution, not authority: any principal of kind `system` owns the root and
  passes every check, as the one word does today. Nothing grants to the system, and a part
  narrower than the whole would be verse gating itself.
- **`<kind>:<id>` admits the principal named exactly that, or whose owner resolves to it.** A
  rule resolves to its charter's owner, so a grant to `user:ana@example.com` admits what ana
  owns acting as itself, which is the meaning a grant to a person has today. `<kind>:*` admits
  any principal of the kind, by its name. The listing `verse.principal.<kind>.<id>` is not
  consulted by the match; it stays as the record of who has been met, written from the name's
  kind.
- **`via:<path>` admits the principal acting as that address, and nothing else.** No
  resolution: the address is the identity, and a rule's owner is not the rule. A trailing `*`
  covers what is beneath (`via:verse.sources.*`). A runner is not a place — it acts as
  `runner:<identity>`, not as its address — so writing a grant refuses a `via:` path that nothing
  acts through.
- **`group:<name>` admits the group's members, walked with a visited set.** A cycle admits
  nobody because the walk ends, not because a counter ran out; a group nested five deep is
  admitted. `group:*` is refused: a group is a set to be named, not a kind to be listed under,
  and nothing acts as a group, so `group:` is never a principal.
- **A request may present `user:`, `agent:` or `runner:`.** `via:` and `system:` names are
  made by versed — when a rule fires, a source reflects, a binding answers, versed acts — and a
  name versed makes is not one a request presents. `group:` is nobody's name. All three are
  refused at the door, where today only the word `system` is. Identity stays self-asserted
  within the three, as it is today.
- **A bare email is shorthand for `user:<email>` at the keyboard** — the CLI's `user:` config,
  `--as`, a grant line, a member list, an `answerers` list — and is normalised where it is
  read. One form is stored, and one form comes back from every read.

## Order of work

Seven steps. Each leaves the tree green: `go test $(go list ./... | grep -v /scratchpad) &&
./smoke.sh && make check-docs`. 1 is the grammar; 2 moves every name to it and is the large
one; 3–5 are the match, the edges and the door, independent of each other once 2 is in; 6 and
7 are the landing.

### 1. `entity.Grantee`: the grammar, parsed once

New file `entity/grantee.go`:

- `type Grantee struct { Kind, ID string; Path []string }` — `Kind` is `user`, `agent`,
  `runner`, `system`, `group`, `via`, or `*` for everyone; `ID` is `*` for a whole kind; `Path`
  holds a `via:` address as segments, its trailing `*` kept as the last segment.
- `ParseGrantee(s string) (Grantee, error)`. A bare string containing `@` and no `:` is
  `user:<s>`. Everything else is `<kind>:<rest>` or refused, each with its own words: an unknown
  kind; `group:*`; a `via:` path that does not parse as an address in verse's own half, or
  carries a `*` anywhere but last; an empty id; and any bare word — which is where `user`,
  `agent`, `runner`, `coderunner` and `verse.principal.group.<name>` stop meaning anything.
- `ParsePrincipal(s string) (Grantee, error)`: a grantee that names one principal — not `*`,
  not `<kind>:*`, not `group:`. Both parse the same way; the second refuses the sets.
- `Via(ref Ref) string` spells a place's principal, so `via:` is written in one function.
- `String()` renders the canonical form; `Grammar()` is the one sentence every refusal quotes,
  so the hint is written once (the answerer check, `verse perms`, `verse group add` and the
  resolver say it today in four wordings, one of them wrong).
- The kind constants move here from `grant.go` (`UserKind`, `RunnerKind`, `AgentKind`,
  `GroupKind`) beside `SystemKind` and `ViaKind`; `Everyone` stays `*`.

**Proves:** the table of spellings round-trips through parse and render, and every refusal
above names the grammar. Pin the two that motivated the issue: `agent` bare is refused, and
`verse.principal.group.release` bare is refused. `ParsePrincipal("*")` and
`ParsePrincipal("group:x")` are refused.

### 2. Every name is spelled in the grammar

Every site that produces a principal's name, and the wire that carries one. `named` still
compares strings at this step, so both sides move together and the tree stays green.

- `SystemPrincipal` goes. `entity/grantee.go` names the six parts (`SystemDefault`,
  `SystemDocket`, `SystemMirror`, `SystemPerms`, `SystemSeed`, `SystemCharter`) and
  `IsSystem(name)` asks the kind; the twenty-odd comparisons against the one word
  (`entity/attribution.go` `accessOf`, `keep`, `keepAll`, `resolve`'s fallback; `versed/place.go`;
  the door) ask `IsSystem` instead. Each writer names its part: `mirror/question.go`'s
  `poseRecord` and the docket routes' `asSystem` (`settleOverdueIn`, `escalateOverdueIn`,
  `reown`, `releaseResolver`, `drainInto`'s question writes) are `system:docket`;
  `mirror.Reflect`'s claim grant and the task projection's system half are `system:mirror`;
  every `Principal: SystemPrincipal` in `entity/attribution.go` is `system:perms`;
  `versed/perms.go`'s seed and defaults are `system:seed`; the install at `verse up`
  (`installCharterAs` from `SystemCharter`) is `system:charter`; `NewWithStore`'s
  `entity.As(store, …)` is `system:default`. The nearest-owner line a refusal prints reads
  `system:default (the root)`.
- `Engine.Principal` (`trigger/causal.go`), the sources' `entity.As(store, ref.String())`
  (`versed/sources.go`) and `asBinding` (`versed/server.go`) go through `entity.Via`.
- `runner.Entity.Principal()` returns `runner:<identity>`; `--identity` on `verse runner add`
  keeps taking the bare id. A runner's reports arrive as that name because versed hands it to
  the spawn (`versed/runners.go`) and the claim (`mirror.Reflect`'s `As`) carries it.
- The CLI's `principal()` (`cmd/verse/task.go`) normalises: the config's `user:` and a bare
  `--as` email become `user:<email>`, the config's `agent:` becomes `agent:<name>`, and `--as`
  otherwise takes a principal in the grammar (`--as runner:coderunner` for a worker's claim;
  `verse task do --as coderunner` is refused with the grammar). `principalKind()` and
  `api.PrincipalKindHeader` are deleted.
- `notePrincipal` lists a principal under the kind its name carries and drops its "is this a
  runner" lookup, which existed to tell the two apart.
- What versed writes as grantees moves with the names: the seed (`versed/perms.go`) spells
  `user:*` and `agent:*`; the claim's grant (`mirror/mirror.go` `claimed`) names the runner as
  above; a `url` binding's hold-out line and an instance's are `via:verse.resolvers.<kind>.
  <name>`.
- The tests that assert a principal or a grantee string are respelled here:
  `versed/runneridentity_test.go`, `runnergrant_test.go`, `questionperms_test.go`,
  `systemperms_test.go`, `group_test.go`, `cmd/verse/principal_test.go`, `group_test.go`,
  `entity/read_test.go`, `grant_test.go`, `attribution_test.go`, and whatever `git grep -l
  'Principal: "'` finds beyond them; expect twenty to thirty files.

**Proves:** a firing's writes carry `via:verse.trigger.rule.<name>` and resolve to the
charter's owner; a rule of the system charter resolves to `system:charter` and passes the
check by kind; a source's reflections carry `via:verse.sources.<ref>`; a claimed worker's
reports carry `runner:<name>`; a question's record reads `owner: system:docket` and a seed row
`system:seed`; `verse ps`, `verse perms` and a task record read back with typed names; the
CLI refuses `--as coderunner`; the seed reads back as `user:*`, `agent:*`; every
`system:<part>` holds `owner` at the root.

### 3. The match: `named` over `Grantee`

`entity/attribution.go`. `named(names, principal, who)` parses each name and matches by
structure; `namedWithin` and its depth go, and so does `listedAs`.

- `*`: admitted.
- `<kind>:<id>`: `principal == g.String() || who == g.String()`.
- `<kind>:*`: the kind of `principal` or of `who` is `g.Kind`.
- `via:`: `principal` equals the path, or is beneath it under a trailing `*`. `who` is not
  consulted.
- `group:`: `members(GroupRef(name))`, recursed with a `seen map[string]bool` keyed by the
  group's name; a name already seen is skipped.
- A name that does not parse admits nobody, and is counted beside `refused` and `hidden` (not
  in `noteUnreadable`, which means the store) so `verse status` shows it, and logged once per
  record naming the line. That is the whole of what happens to a stale `--data` directory:
  nothing is rewritten, and what it holds out is nothing, said out loud.

`members` keeps its cache. `resolve` keys on `ViaKind` rather than on "parses as a reserved
address", which is the same test asked of the name instead of guessed from it.

**Proves:** `entity/read_test.go`'s cycle test passes with the depth gone; a chain of five
groups admits its leaf; `agent:*` does not admit `user:agent`; `user:agent` is admitted by
exactly that; `via:verse.sources.*` admits `via:verse.sources.git.cluster` and not
`via:verse.trigger.rule.x`; a grant naming `verse.principal.group.release` bare admits nobody,
is counted, and is named in the log.

### 4. The edges refuse what does not parse

Every place a grantee is written parses it with `ParseGrantee` and refuses with `Grammar()`.

- `ParseLevels` and `parseGrant` (`entity/grant.go`): each grantee parsed, so `verse perms
  --grant`, `--revoke`, `--grants` and `--default` refuse a bad spelling where it is typed. The
  perms route (`versed/server.go` `putPerms`) and `verse up`'s `perms.default` config read
  through the same functions and need nothing of their own.
- `verse group add` (`versed/server.go` `putGroup`): members parsed; the whitespace check goes.
- `checkGateAnswerers` (`trigger/trigger_api.go`): parse instead of the `@` heuristic, which
  reads a resolver instance's address (`…@<origin>`) as a person. `group:<name>` must stand;
  `via:<path>` must stand; a kind with `*` or a group is unbounded, as today.
- The resolver (`resolver/resolver.go`): `method.who` is parsed with `ParsePrincipal` and
  `method.group` as a `group:`; `Answerer()` returns the canonical string. The `verse.group.
  <name>` in its messages and its field comment goes with this.
- `api.Group.Members` and the CLI synopses (`cmd/verse/group.go`, `cmd/verse/perms.go`) say
  the grammar.

**Proves:** each edge refuses the old spelling and the error quotes the grammar; a group whose
member is `verse.principal.group.x` is refused at `verse group add`; a gate whose answerer is
an instance address is not read as a person.

### 5. The door: what a request may present

`versed/server.go`, the middleware that reads `Verse-Principal`.

- The header is parsed with `ParsePrincipal`; a bare email is normalised to `user:`. Anything
  that does not parse is refused 400 with the grammar.
- `via:` and `system:` are refused 400, with the reason: a name versed makes is not one a
  request presents. `group:` cannot parse as a principal and is refused by the first rule.
- `user:`, `agent:` and `runner:` pass, self-asserted as today.

**Proves:** `Verse-Principal: verse.principal.group.release`, `verse.trigger.rule.x`,
`via:verse.trigger.rule.x`, `system:default` and `coderunner` are all refused before any check
runs; `walter@example.com` passes and is recorded as `user:walter@example.com`;
`agent:harness` and `runner:coderunner` pass.

### 6. The docs say the grammar

The landing rewrites, not appends. The grammar is written down once, in `perms.md`, and every
other page links to it. What changes, by file:

- `docs/perms.md`: the grantee paragraph under "grants" and the seed table (`user`, `agent` →
  `user:*`, `agent:*`); the group paragraph loses "bounded depth" and says the walk; every
  `owner:` and `by:` in an example; the system paragraph lists the six parts and says they are
  one authority.
- `docs/perms-guide.md`: the opening ("A person is an email … a runner names itself") says the
  three kinds a request presents; "A group is not a principal" stays true and is respelled; the
  binding paragraph and "Groups, here and on a gate" are rewritten around the grammar — that
  paragraph is the one the issue quotes.
- `docs/reference/resolvers.md`: `method.who` and `method.group` rows.
- `docs/reference/README.md`: the `group` and `perms` lines in the command block; `--as` where
  the block shows it.
- `docs/reference/up.md`: the `user:` and `agent:` config fields say what goes on the wire;
  `api/version.go` loses the kind header and its comment.
- `docs/reference/examples/README.md` and `docs/runners.md`: `--identity` and what a runner
  acts as.
- `docs/trigger-model.md` §gates (`answerers`), `docs/tutorial.md`'s gate example and every
  `--as` in it: `verse.principal.group.payments` → `group:payments`.
- `docs/working/grants-residual.md` §6 loses "A group's cost … bounded by depth three" and
  keeps the instance-per-service point, which this does not touch.

**Proves:** `make check-docs`; every grantee list and every `--as` in the fenced blocks of
`docs/` and `docs/reference/` parses (a test that greps them is cheap and worth having, since
the examples are what people copy).

### 7. Landing

- **One branch, one notice.** The notice is a git-issue filed before the push and linked back to
  `2z4eef8sh12kszscmxn0`; every commit on the branch that moves a spelling is `contract` and
  names it, with `Breaks:` saying what each old spelling becomes — `--as <word>` → `--as
  runner:<word>` is the one a worker meets first, and `Verse-Principal-Kind` going is the one a
  harness meets.
- **Staging** is wiped and repopulated (`deploy/repopulate.sh`, then the arming commands it
  prints): every stored owner and grant is in the old spelling, and there are no users whose
  grants would be worth carrying. `deploy/repopulate.sh`'s `--identity coderunner` stands — the
  id is bare, the principal is `runner:coderunner`. Before the ship, run wipe + repopulate + the
  sources' first pass locally and read `verse status` for the step-3 count at zero.
- No charter changes: none of the live charters spells a grantee or a principal (`grant:` on a
  spawn is a task envelope, a different vocabulary).

## Not in this plan

- **A system part narrower than the whole.** The six parts are names on a write; none is
  checked against a grant, because nothing grants to the system.
- **A second identity source.** Identity stays self-asserted; the grammar says what a request
  may claim, not how the claim is checked.
- **A group binding minting an instance per service member** — `grants-residual.md` §6, unchanged.
