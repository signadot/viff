# Strict-mode parsing: deciding it once, at the boundary

Status: proposal, and it depends on a version that is not the one pinned — verse is on go-tony
v0.0.113, and `gomap.Strict()` arrives in v0.0.114. Nothing here runs. The probes in §2 do run,
on the pinned build, and are the reason the note exists.

The short version: verse already has a strictness policy for unknown fields. It has four of
them, they disagree, and which one a document gets is decided by whether somebody happened to
hand-write a codec for its type. Upstream's new option is the occasion to choose on purpose —
but the rule upstream suggests is not implementable here, for a reason worth knowing.

## 1. What upstream added

`gomap.Strict()` is an `UnmapOption`; `gomap.IsStrict(opts...)` is what a decoder asks. The
reflection path honours it (`gomap/from.go:876`), and the generator emits a `default:` case that
does (`gomap/codegen/generator.go:1587`):

```go
default:
    if gomap.IsStrict(opts...) {
        return fmt.Errorf("unknown field %q for PathData", fieldName.String)
    }
```

The default stays lenient, and upstream says why in `gomap/options.go:211`: leniency is what a
wire protocol wants, so an older peer can decode a newer peer's message; strictness is for
"inputs a human wrote and expects to have been read — a config file above all, where a
misspelled key is otherwise indistinguishable from one deliberately left out."

Hold onto that sentence. It is right, and §3 is about why verse cannot apply it as written.

## 2. Today: four policies, none of them chosen

| decoder | an unknown field is | decided by |
|---|---|---|
| generated `*_gen.go` | **dropped**, always | nobody — there is no `default:` case at all (0 across 6 files, 40 switches) |
| `Stratum.FromTonyIR` | **hard error**, always | hand-written, `trigger/stratum.go:166` |
| `Match`, via `Validate` | **hard error**, always | hand-written, `trigger/match.go:145` |
| `Condition`, `requirementFromNode`, `Formula.fromVerbose` | **dropped**, always | pull-style `ir.Get`, which never sees a field it did not ask for |

Three probes, all on the current pinned build, all in what could be one charter file:

**A stratum typo is a hard error.** `levle: 10` → `stratum: unknown field "levle" (want name,
level, match)`. Good error, names the alternatives.

**A condition typo is silent, and installs a different rule.** `whn: !until [...]` decodes to
`When == nil`; `Condition.validate` passes, because a subject alone is a complete condition
(`trigger/trigger.go:112`). The rule installs and fires on *every* change to its subject rather
than waiting for the formula. Nothing anywhere reports this.

**A requirement typo is silent, and inverts a gate.** `!quorum {n: 2, choise: reject}` decodes
to `Choice: ""`, and empty defaults to `approve` (`trigger/formula.go:189`, again at `:216`).
A deny-quorum becomes an approve-quorum. Two rejections were going to block; now two approvals
release.

Same file, same install, three different policies.

**And the option could not reach them anyway.** `codec.FromNode` (`codec/codec.go:206`) is the
single funnel under `Read`, `Unmarshal`, `Decode` and `ReadFile`, and it calls
`gomap.FromTonyIR(n, v)` with no options and no way to pass any. Below it, the hand-written
codecs take `opts` and then drop them on descent — `f.fromTagged(tag, node)`,
`m.FromTonyIR(node)` inside `Formula.FromTonyIR`, `matchFromObject(node)`. That compiles
cleanly and will keep compiling cleanly after the bump, which is why §5 step 3 needs a test
rather than a review.

## 3. The boundary that isn't there

The obvious rule is upstream's: strict for files, lenient for the wire. **It is unimplementable
as stated, because verse has no client-side decode of a charter.**

`verse charter add` reads bytes (`codec.ReadSource`), parses them to nodes, classifies each by
which fields it has *without decoding it* (`defKind` — a rule has a `condition`, a stratum has
a `level` and a `match`), and forwards each node verbatim (`postDefs` → `httpPostNode`,
`cmd/verse/client.go:88`). `verse charter add` does the same. The only place
a charter becomes a Go value is `addStratum`/`addTrigger` in versed.

That is deliberate and worth keeping. A CLI that decoded charters would be a CLI that refuses
charters a newer versed understands perfectly well — the skew failure, moved to the one place
that has no business having an opinion.

So the boundary that matters is not file-versus-wire. It is **which plane of the API a body
belongs to**:

- **Charter installation** — `POST /strata`, `POST /triggers`. Not a message
  between running parts: an authoring interface, whose body is a document a person wrote and a
  CLI forwarded unread. → **strict**. This is exactly upstream's config-file case; it just
  arrives over HTTP.
- **The rest of the mutation plane** — `POST /entity`, `POST /tasks`, the steer routes.
  Machine-written messages. → **lenient**.
- **Responses, decoded by the client** (`cmd/verse/client.go:210`). This is the skew-sensitive
  direction, newer versed to older CLI, and it is the one `api/version.go` already has a
  doctrine for: a minor difference is drift, "worth a word to the operator and nothing more."
  A strict client turns that word into a failure. → **lenient**.
- **The store, at restore** — `stratumOf` under `LoadStrata`, and `ruleOf`. → **lenient, most
  emphatically.** `versed/server.go:512` loads strata before rules precisely so that a rule is
  not held for want of its stratum; a strict decode there turns one unknown field into a
  charter that will not load, on a path where nobody is present to fix the input.

## 4. The exception that must not be an option

`Match` stays unconditionally strict — on the lenient routes too.

Dropping a field from a match does not lose information. It **widens a selection**:
`{system: gh, kind: pr, stage: open}` minus an unread `stage` is every gh pr. The code already
knows this and says so at `trigger/tonycodec.go:183` — the unknown key is decoded and carried
so `Validate` can name it, "rather than dropping it here and reporting a match that constrains
less than it says." The quorum probe is the same failure one level down: dropping `choice` does
not narrow the gate, it flips it.

So the rule needs a second clause, and it is the load-bearing one:

> Strictness is a property of where a document came in, not of its type — **except** where an
> unread field could change what the document selects or authorizes, which is strict
> everywhere.

A `Trigger`'s unknown top-level field is inert; a `Match`'s and a `Requirement`'s are not.

I looked for a case that breaks this and did not find one, but the place to keep looking is
where the two clauses meet: a type that is both selection-shaped and skew-exposed — sent by a
newer peer, and widening if a field goes unread. `Match` would be that type if it appeared in a
response body a client decoded into. Today it does not (the CLI prints response documents; it
does not decode them into `Match`). If it ever does, that route is the counter-model and the
rule needs a third clause rather than a shrug.

## 5. The work

Six steps, in order, each landable on its own.

1. **Bump go-tony to v0.0.114 and regenerate.** No behaviour change: the emitted `default:` is
   inert until someone passes `Strict()`. What it buys is that the generator is wired, which
   the diff shows directly — six `*_gen.go` files gain a `default:` where they have none.

2. **Give `codec` a strict entry point.** `codec` keeps gomap entirely internal today —
   `codec.go:203` and `:206` are its only two uses — so taking `...gomap.UnmapOption` in its
   public API would be a new leak through the package whose job is to be verse's only door to
   go-tony. With exactly two policies, two names is the cheaper answer: `FromNodeStrict`
   beside `FromNode`, `ReadStrict` beside `Read`. Revisit if a third policy ever shows up;
   until then an option type is ceremony around a boolean.

3. **Thread `opts` through the hand-written codecs on descent.** Mechanical — the three call
   sites in §2 plus whatever the compiler does not catch, which is all of them. Needs a test
   that a `Strict()` passed at the top reaches a generated decoder underneath a `Formula`,
   because nothing else will notice if it stops.

4. **Convert the pull-style decoders to field iteration.** `Condition.FromTonyIR`,
   `requirementFromNode`, `Formula.fromVerbose`. Each becomes `for i, field := range
   node.Fields { switch field.String { … default: … } }` — the shape `Stratum.FromTonyIR`
   already has, and the shape the generator emits. This is the step where the option first buys
   anything, and it is what turns probes 2 and 3 into errors.

5. **Set the policy per route.** The three charter routes read strictly; everything else does
   not. Simplest is the handler calling `codec.ReadStrict`. Worth a moment's thought first:
   `router.handle` already records what each route *accepts* as a signature name
   (`versed/routes.go:47`), and strictness is a fact of the same kind — it would sit well in
   the route table at `versed/server.go:537`, where it is legible next to everything else the
   route promises, rather than in one line inside a handler body. That is only worth doing if
   it stays cheap; if it needs request-context plumbing to reach the decode, take the handler
   call.

6. **Resolve the two existing unconditional strictnesses.** `Match` keeps its, under §4, and
   grows a comment saying it is deliberately not opt-controlled — otherwise the next person to
   thread options through will "finish the job" and quietly widen every match on the lenient
   routes. `Stratum`'s becomes opt-controlled like everything else: strict in practice, since
   its only route is a charter route, and lenient on restore.

Step 6 is the only behaviour change to an existing path: a stratum entity in the store carrying
a field this build does not know stops being a startup error. That is the intended change — see
§3 on what a failed `LoadStrata` costs — but it is the line in the diff that deserves the
scrutiny.

## 6. Tests worth having

- The three probes as regression tests, each asserting both halves: error under strict, dropped
  under lenient. They are the whole content of the change.
- One test that goes through the *route* rather than the type. The dropped-`opts` bug in §2 is
  invisible at the type level — `Stratum.FromTonyIR` looks like it honours options, and does
  not.
- A pin on upstream's default, the way `TestUpstreamHonorsOmitzero` (`codec/upstream_test.go:210`)
  pins omitzero. Lenient-by-default is a decision upstream could revisit, and verse would find
  out through a charter that stopped installing.

## 7. Not yet

**Strictness chosen by version skew.** versed reads the `Verse-Version` header, and
`api.VersionSkew` already turns it into an answer: same version, or no header at all, means a
peer that should have known better or a human with curl → strict; drift means a newer client
that may legitimately know a field this build does not → lenient. It is coherent, it needs
nothing new, and it makes §3's four-way split fall out of one rule.

It is also a decode policy that varies per request, which is a miserable thing to reconstruct
from a bug report six months later. Park it until a real skew incident asks for it, and then it
will have an example to be designed against.

## 8. Open

- **`POST /tasks`.** A launch spec is authored sometimes and generated sometimes. §3 leaves
  it lenient; the first person to typo a `grant` will disagree, and they will be right, because
  §4's clause plainly covers it — a grant is an authorization, and an unread one widens. Wants
  a read of `launch/` to say which of its fields are selection-shaped.
- **A `--check` for `verse charter add`.** Decode locally, strictly, and report at the file with
  a line number, before anything is sent. It reintroduces exactly the client-side interpretation
  §3 argues against — but as an opt-in check rather than as the install path, which may be the
  whole difference. Cheap to try once step 4 lands.
