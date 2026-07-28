# LLM memory: implementation plan

Status: plan, nothing built. The design is [llm-memory.md](llm-memory.md); this is how it lands.
Read that first — this file does not restate its reasoning, only the order of work and what each
step must prove.

The shape being built: per-key entities under `memory:note:<doer>:<key>`, a `recall`/`remember`
tool pair carrying derived effects, and the doer's key listing injected into the frame at spawn.

## Order of work

Five steps. Each is independently reviewable and leaves the tree green; steps 1–2 are invisible
to a user, step 3 is the first with observable behaviour. The dependency order is real — 4 needs
3's tools to exist to gate them, and 3 needs 2's store handle to reach the verse.

### 1. Thread `Doer` to the harness

The one piece of pure wiring. `Spec.Doer` exists and is set (`launch/fragment.go` `Overlay`,
resolved in `trigger/trigger.go`); its only reader is `mirror.go`. Add the field to
`process.Launch` (`process/runner.go`), copy it in `launchOf` (`versed/server.go`), add it to
`harness.Config` (`process/harness/build.go`), carry it onto `llmDriver`.

Note in the `Launch` field's doc *why* it is there — that comment block explains each field's
presence, and `Doer` is the first one that is identity rather than build input.

**Proves:** a spawn's selected doer reaches the process builder. Test: build a process from a
spec carrying `Provider`, assert `Doer` arrives at `harness.Config`. Also assert the negative —
a spawn with no provider leaves it empty, and empty must be handled everywhere downstream
(no provider selection ⇒ no memory, not memory under the key `""`).

### 2. The memory store: refs, read, write

New file, `harness` package (or its own small package if the harness's `entity` dependency turns
out to want isolating — decide when the import lands, not now):

- `memRef(doer, key) entity.Ref` — `{System: "memory", Kind: "note", ID: doer + ":" + key}`.
  Round-trip it through `entity.ParseRef` in a test: `ID` may contain colons, and that is the
  property the whole scheme rests on.
- `keys(store, doer) []string` — `store.List` over `System == "memory" && Kind == "note"` with
  the `<doer>:` prefix, returning the suffixes, sorted (deterministic frames).
- `read(store, doer, key) (Payload, bool, error)` — one `Get`. Propagate the `(ok, err)`
  distinction; do not collapse it. `entity.Store.Get`'s doc is emphatic about why, and a model
  told "you have no note called X" when the store was unreachable will go and write one.
- `write(store, doer, key, value) error` — `entity.Modify`, always. Wrap with jittered backoff
  and a retry cap (see step 5).

`harness.Config` gains a `Store entity.Store`. **Nil is legal and means no memory** — `verse up`
(`cmd/verse/up.go`) builds a harness directly and ad-hoc `process run` has no verse behind it.
Every call site must tolerate it; a nil store makes `recall`/`remember` unofferable rather than
panicking.

**Proves:** refs round-trip, listing is prefix-correct and sorted, a write survives a concurrent
write to a different key without retrying, two writers to the *same* key don't lose an update
(`concurrentIncrement` in `entity/write_test.go` is the existing pattern to copy).

### 3. The tools

`toolDefs()` in `process/harness/llm.go` gains two entries; `fsExec.Execute`
(`process/harness/exec.go`) gains two cases beside `read_file`/`write_file`.

- `recall(key)` → `read`, returns the payload rendered for the model, or a plain "no note under
  that key" when `ok` is false.
- `remember(key, value)` → `write`.

Two things to get right, both about not lying to the model:

**Offer them only when they exist.** A nil store, or an empty `Doer`, means neither tool is in
the offered set. `selectTools` already refuses an unknown name rather than silently shrinking
the set — so a spawn whose `tools:` names `remember` while memory is unavailable must **fail the
spawn**, not launch without it. That is the existing discipline (`selectTools`'s doc: "quietly
offering fewer tools than the charter asked for is the failure mode this change exists to
remove"), and memory is the first tool whose availability is conditional on runtime wiring
rather than on the name being real. Expect this to need a small change to how `selectTools`
decides what exists.

**A refused write is a tool error, not a silence.** `resultContent` already renders gate denials
and errors back to the model; a `remember` that exhausted its retries comes back the same way, so
the model can decide whether to retry or move on.

**Proves:** a `remember` then `recall` round-trips through a real store in a driver test (the
`llm_test.go` fake-client pattern); tools absent under a nil store; a spawn naming `remember`
with no store fails rather than launching short.

### 4. Effects and authority

Without this, step 3 is refused under every named grant except `all` — see the design note's
authority section. Two edits:

- `process/effect.go`: `KindMemory ResourceKind = "memory"` and a `Memory(key) Resource`
  constructor beside `File`/`AgentRes`.
- `Derive`: cases for both tools, emitting `Effect{Verb: Read, Resource: Memory(key)}` and
  `Effect{Verb: Write, Resource: Memory(key)}`.

`Derive` lives in **two** places — `demoDeriver` (`process/harness/exec.go`) and `BasicDeriver`
(`process/deriver.go`, the `NewSupervisor` default). Update both, or the envelope differs by
which deriver a process happened to get, which is the exact failure `Grant.Permits`'s doc warns
about ("a Grant has no deriver, and one that classified actions with a different deriver than
the vector uses would be enforcing a different envelope").

`namedGrants` needs **no change** — the names match on verb alone. Pin that as a test rather
than a comment: assert all four names against both tools, matching the design note's table.
`trigger/providerceiling_test.go` pins all sixteen grant pairs and is the precedent for this
kind of exhaustive vocabulary test.

**Proves:** the table in the design note, executably. `read-only` permits `recall` and refuses
`remember`; `none` refuses both; `all` and `no-delegate` permit both. Plus: a gate vector with
`ResourcePred{Kind: KindMemory}` can gate memory writes without touching file writes — the
separation the named grants can't express.

### 5. Frame injection, then the throttle

`harness.Build`, right after `frame` resolves and beside the `contractClause` append: list the
doer's keys and add a clause naming them. Empty listing adds nothing — no doer, no store, or no
notes yet must all produce a byte-identical frame to today's, or every existing frame test
starts failing for a reason unrelated to what it tests.

Then the retry throttle from step 2's `write`: jittered backoff, a cap, and the cap surfacing as
a tool error. Do it last because it is only observable under contention, and contention needs
the tools to exist to provoke.

**Proves:** a frame naming existing keys; an unchanged frame when there is no memory; a
contended write that gives up rather than spinning (fake store whose `Write` always refuses the
precondition — assert it terminates and reports).

## What this plan deliberately does not do

- **No `!key` associative merge.** Documented, zero mileage in verse, and unnecessary until a key's
  value is a collection wanting element-wise merge.
- **No memory for inline `eligible` doers.** Registry doers only; see the design note.
- **No similarity search.** A key listing tells a model what topics exist, not which is relevant.
- **No grant name separating memory writes from file writes.** Gates can express it; the
  vocabulary question runs into the structured-grant frontier and should not be settled here.
- **No `forget`.** Deletion is `Write` with a nil value and costs nothing to add — but nothing
  needs it yet, and a doer that can prune its own memory is a different policy question
  (a doer erasing what it learned about its own failures) worth asking separately.

## Risks worth watching

**The `entity` dependency on `harness`.** New edge in the graph; `harness` has no `entity`
import today. If it turns out to drag more than `Store` along, that is the signal to put the
memory accessor in its own package and give the harness an interface instead.

**Frame growth.** The listing is key names only, which is what keeps this cheap. If it ever
grows to include content "just for convenience", the scaling argument in the design note is lost
and every spawn pays for every note the doer ever wrote.

**A doer writing its own future instructions.** `remember` persists content that re-enters this
doer's later runs via `recall`. A doer that ingested hostile text (a PR body, an issue
description) and wrote it to memory has planted it in its own future context — persistent
injection across process boundaries. The grant boundary in step 4 bounds *who* may write memory;
it says nothing about *what*. Not a reason to withhold the feature, and not solvable by this
plan, but it should be written down somewhere with authority's name on it before memory is handed
to a doer that reads untrusted input.
