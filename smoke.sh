#!/usr/bin/env bash
#
# smoke.sh — end-to-end smoke of verse's core operator surfaces against an isolated,
# self-contained docd-backed server.
#
# It builds throwaway `verse` + `o` binaries (o is the pinned go-tony daemon runner, built
# from this module — so bringing up logd+docd needs no external dependency) and runs its own
# `verse up` on an ephemeral port with a private data dir. Hermetic: it forces the scripted
# driver, so no LLM creds and no network. It never touches a dev server on :9797, and a trap
# reaps the server (and its daemon tree) plus the temp dir on exit.
#
# Every check is fatal except the two noted as best-effort. Exits non-zero on the first failure.
#
#   ./smoke.sh              # or: make smoke
#   PORT=20000 ./smoke.sh   # pin the port (default: whichever one the kernel hands out)
#
set -euo pipefail

cd "$(cd "$(dirname "$0")" && pwd)"

checks=0
step() { printf '\n\033[1m# %s\033[0m\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; checks=$((checks + 1)); }
note() { printf '  \033[33m~\033[0m %s\n' "$*"; }
die()  { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }
has()  { printf '%s' "$1" | grep -qF -- "$2"; }
# haskv asserts a document carries key: value, in WHICHEVER dialect it is written —
# `"stage": "open"` and `stage: open` both match. The suite runs under both renderings
# (make smoke-all: the tony default, then VERSE_OUTPUT_FORMAT=yaml), so a shape-dependent
# assertion would only ever be testing whichever one happened to be configured.
#
# The second pass varies the OUTPUT format only. Input is a different question: JSON is
# read as tony, but YAML is a genuinely separate dialect, so a `VERSE_FORMAT=yaml` pass
# would be asserting that JSON payloads fail — which is not a property worth protecting.
haskv() { printf '%s' "$1" | grep -qE "\"?$2\"?[[:space:]]*:[[:space:]]*\"?$3\"?"; }

# ── build throwaway binaries (verse + the pinned daemon runner) ──────────────
step "build (verse + pinned o)"
tmp=$(mktemp -d)
BIN="$tmp/verse"
SRV=""
CONN=""
cleanup() {
	[ -n "$CONN" ] && kill -INT "$CONN" 2>/dev/null || true
	[ -n "$SRV" ] && kill "$SRV" 2>/dev/null || true # SIGTERM → up reaps logd+docd (own pgroup)
	wait 2>/dev/null || true
	rm -rf "$tmp"
}
trap cleanup EXIT INT TERM
go build -o "$BIN" ./cmd/verse || die "go build ./cmd/verse failed"
go build -o "$tmp/o" github.com/signadot/tony-format/go-tony/cmd/o || die "go build o failed"
ok "built verse + o"

# ── the listen address: an ephemeral port, from the kernel ───────────────────
# Reserve-and-release, the same trick cmd/verse/docd.go plays for the daemons: bind :0, read
# back whichever port the kernel picked, drop it, and hand it to `verse up` a moment later.
# It replaces a scan upward from a fixed 19797, which two smokes started side by side (two
# worktrees, say) would both walk to the same answer before either of them bound it. Go is
# already a hard dependency here, so this costs nothing a shell probe would not.
free_port() {
	cat >"$tmp/freeport.go" <<'GO'
package main

import (
	"fmt"
	"net"
)

func main() {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	fmt.Print(port)
}
GO
	go run "$tmp/freeport.go"
}
# PORT pins it outright — an escape hatch for a run you want to watch from another terminal.
if [ -n "${PORT:-}" ]; then
	ADDR="127.0.0.1:$PORT"
else
	ADDR="127.0.0.1:$(free_port)" || die "could not reserve an ephemeral port"
fi
v() { "$BIN" --addr "$ADDR" "$@"; }

# ── start the monolith on its own logd+docd ──────────────────────────────────
step "up (docd) on $ADDR"
# invoke the binary directly (not via v()) so $! is the verse process itself — a backgrounded
# shell function is a subshell, and kill would signal the wrapper, not verse.
"$BIN" --addr "$ADDR" up --data "$tmp/data" --o-bin "$tmp/o" >"$tmp/server.log" 2>&1 &
SRV=$!
ready=
for _ in $(seq 1 300); do
	if v status >/dev/null 2>&1; then
		ready=1
		break
	fi
	kill -0 "$SRV" 2>/dev/null || { tail -20 "$tmp/server.log" >&2; die "server exited during startup"; }
	sleep 0.5
done
[ -n "$ready" ] || { tail -20 "$tmp/server.log" >&2; die "server never became ready"; }
ok "docd-backed server ready"

# ── state: put/get roundtrip ─────────────────────────────────────────────────
step "state put/get"
v state put demo:pr:1 '{"stage":"open"}' >/dev/null || die "state put failed"
haskv "$(v state get demo:pr:1)" stage open || die "state get did not roundtrip"
ok "demo:pr:1 = {stage:open}"
# THE ANSWER IS A DOCUMENT, addressed by the same path at any depth: the id is a KEY, not a
# token inside a rendered ref, and `stage` is what stands under it.
haskv "$(v state demo.pr.1)" stage open || die "state demo.pr.1 did not answer the payload"
has "$(v state demo.pr)" '"1":' || die "state demo.pr did not key by id"
[ "$(v state --paths demo.pr)" = "demo:pr:1" ] || die "--paths did not name the entity"
# `has`, not `=`: a path can now answer with a BARE SCALAR at the document root, and the two
# renderings frame one differently — tony writes `open`, yaml writes it as a block scalar
# (`|` then the line). The value is verse's answer; the framing is the encoder's.
has "$(v state demo.pr.1.stage)" open || die "a path into the payload did not answer"
ok "one path, four depths: demo / demo.pr / demo.pr.1 / demo.pr.1.stage"
# ONE ADDRESS on the verbs too: the path reaches inside the payload, which `--at` used to.
v state put demo.pr.1.labels.urgent true >/dev/null || die "a deep put by path failed"
has "$(v state demo.pr.1.labels)" urgent || die "the deep put did not land"
v state rm demo.pr.1.labels.urgent >/dev/null || die "a deep rm by path failed"
case "$(v state get demo:pr:1)" in *urgent*) die "the deep rm did not take the field" ;; esac
# …and the two spellings are one grammar: a ref and its kinded path name the same place.
haskv "$(v state get demo.pr.1)" stage open || die "the kinded path did not reach the entity"
haskv "$(v state get demo:pr:1)" stage open || die "the ref spelling did not reach the entity"
has "$(v state get demo.pr.1.stage)" open || die "a path into the payload did not reach the field"
ok "one address: put/get/rm reach inside a payload, by ref or by path"

# ── the representation: verse READS three dialects and WRITES two ────────────
# The asymmetry is the design (see package codec): JSON input is parsed as tony, because
# valid JSON is valid tony, but nothing renders JSON — a tagged charter has no faithful
# JSON spelling. Both halves are asserted here rather than left to the double-run, because
# a refusal that quietly became a fallback would still pass a suite run only one way.
step "representation (-o / negotiation)"
haskv "$(v state get demo:pr:1 -o tony)" stage open || die "-o tony did not render"
has "$(v state get demo:pr:1 -o yaml)" 'stage: open' || die "-o yaml did not render"
v state get demo:pr:1 -o tny >/dev/null 2>&1 && die "a bad -o value was accepted"
v state get demo:pr:1 -o json >/dev/null 2>&1 && die "-o json was accepted"
has "$(v state get demo:pr:1 -o json 2>&1 || true)" 'does not render json' \
	|| die "-o json failed without explaining itself"
ok "-o tony | yaml render; a bad value and json are rejected"
# --ifmt says how to READ what the operator supplied; it must not decide how the REQUEST is
# rendered, because verse renders no json. A WRITE is the case that proves the two are
# separate: --ifmt json used to fail every command that sends a body — i.e. every write —
# complaining about rendering json to someone who had only asked to type it.
v --ifmt json state put demo:pr:7 '{"stage":"open"}' >/dev/null || die "--ifmt json write failed"
haskv "$(v state get demo:pr:7)" stage open || die "the --ifmt json write did not land"
ok "--ifmt json writes (the input dialect is not the wire rendering)"
# curl straight at the API: no Accept header at all gets the configured default...
haskv "$(curl -s "http://$ADDR/entity?ref=demo:pr:1")" stage open || die "the wire did not answer"
# And the document route, raw: the id is a KEY under the kind, not a `ref:` field beside it.
raw=$(curl -s "http://$ADDR/state?at=demo.pr")
has "$raw" '"1":' || die "GET /state?at=demo.pr did not key by id: $raw"
has "$raw" 'stage' || die "GET /state?at=demo.pr did not carry the payload: $raw"
case "$raw" in *ref:*) die "GET /state still renders a ref: $raw" ;; esac
# ...demanding JSON is 406, not tony wearing a JSON label...
code=$(curl -s -o /dev/null -w '%{http_code}' -H 'Accept: application/json' \
	"http://$ADDR/entity?ref=demo:pr:1")
[ "$code" = "406" ] || die "Accept: application/json returned $code, want 406"
# ...and listing it as one preference among several is satisfiable, so it is satisfied.
haskv "$(curl -s -H 'Accept: application/json, application/tony;q=0.5' \
	"http://$ADDR/entity?ref=demo:pr:1")" stage open || die "a mixed Accept did not negotiate"
ok "the wire negotiates (Accept honored, json refused with 406)"

# ── scope: copy-on-write isolation (docd overlays) ───────────────────────────
# Note: a scope created here is not torn down — there is no general scope teardown yet, only
# connect's. The throwaway data dir reclaims the overlay. See docs/scope-lifecycle.md.
step "scope isolation (--scope)"
SC="iso$$"
v --scope "$SC" state put demo:pr:9 '{"stage":"scoped-only"}' >/dev/null || die "scoped state put failed"
has "$(v --scope "$SC" state get demo:pr:9)" 'scoped-only' || die "scope does not see its own write"
ok "scoped write visible inside scope $SC"
# the overlay must not leak into the baseline
if has "$(v state get demo:pr:9 2>/dev/null || true)" 'scoped-only'; then
	die "scoped write leaked into the baseline"
fi
ok "scoped write invisible in the baseline (isolation)"
# ...but a baseline entity IS visible from within the scope (copy-on-write read-through)
haskv "$(v --scope "$SC" state get demo:pr:1)" stage open || die "scope cannot read the baseline (no read-through)"
ok "baseline entity readable from the scope (read-through)"

# ── reactive core: a non-gated trigger auto-advances its subject ─────────────
step "trigger add + drive (reactive core)"
# The charter below is written in JSON — still accepted, and this is the check that says
# so. It arrives on stdin with no name, so nothing declares a dialect: it parses because
# JSON *is* tony, not because anything sniffed it.
v trigger add -f - >/dev/null <<'JSON' || die "trigger add failed"
[ { "name": "smoke-advance",
    "condition": { "subject": { "system": "demo", "kind": "pr", "value": { "stage": "open" } } },
    "gates":     [],
    "action":    { "patch": { "stage": "reviewed" } } } ]
JSON
ok "charter installed"
# putting a matching entity fires the trigger, which advances the matched subject.
v state put demo:pr:2 '{"stage":"open"}' >/dev/null || die "state put failed"
advanced=
for _ in $(seq 1 100); do
	if haskv "$(v state get demo:pr:2 2>/dev/null || true)" stage reviewed; then
		advanced=1
		break
	fi
	sleep 0.2
done
[ -n "$advanced" ] || die "trigger did not advance demo:pr:2 open → reviewed"
ok "demo:pr:2 auto-advanced open → reviewed"

# ── the same charter, written in tony: the FILE SUFFIX picks the dialect ─────
# The JSON case above pipes on stdin (no name, so the configured default applies); this one
# is a named .tony file, which must be read as tony whatever the default is. It also uses a
# block list rather than [ ... ], which the old "starts with [" sniff could not have seen.
# Between them they are the promise: author in either syntax, get one grammar.
step "trigger add reads a .tony charter (suffix dispatch)"
cat >"$tmp/charter.tony" <<'TONY'
- name: smoke-advance-tony
  condition:
    subject:
      system: demo
      kind: pr
      value:
        stage: queued
  gates: []
  action:
    patch:
      stage: reviewed
TONY
# --file here and `-f` on the stdin case above: both spellings are exercised on purpose,
# since smoke is the only thing that would catch the alias disappearing.
v trigger add --file "$tmp/charter.tony" >/dev/null || die "tony charter add failed"
has "$(v trigger)" 'smoke-advance-tony' || die "the tony charter did not install"
ok "installed a .tony charter (block list, suffix-dispatched)"

v state put demo:pr:3 '{"stage":"queued"}' >/dev/null || die "state put failed"
advanced=
for _ in $(seq 1 100); do
	if haskv "$(v state get demo:pr:3 2>/dev/null || true)" stage reviewed; then
		advanced=1
		break
	fi
	sleep 0.2
done
[ -n "$advanced" ] || die "the tony-authored trigger did not fire"
ok "a tony-authored trigger fires identically to its JSON twin"

# ── an operator on the subject id, written in the JSON spelling ──────────────
# `id` may carry a tony match rather than a string, and JSON reaches it through the
# single-key form — {"glob": …} for !glob. Installing it exercises the whole path the unit
# tests cover in pieces: sugar → tag → Validate → the engine's selection.
step "trigger subject with an id operator (JSON spelling)"
# demo:task, not demo:pr: the charters above match every demo:pr, and a subject the fan-out
# was supposed to exclude would then be indistinguishable from one they had already moved.
v trigger add -f - >/dev/null <<'JSON' || die "operator charter add failed"
[ { "name": "smoke-hotfix-only",
    "condition": { "when":    { "system": "demo", "kind": "note", "value": { "seen": true } },
                   "subject": { "system": "demo", "kind": "task", "id": { "glob": "hotfix-*" } } },
    "gates":     [],
    "action":    { "patch": { "stage": "escalated" } } } ]
JSON
has "$(v trigger show smoke-hotfix-only)" '!glob' || die "the operator did not survive install"
ok "installed; the sugar renders back as a tag"
# two candidates, one matching the glob and one not — the fan-out must pick exactly one.
v state put demo:task:hotfix-9 '{"stage":"open"}' >/dev/null || die "state put failed"
v state put demo:task:feature-9 '{"stage":"open"}' >/dev/null || die "state put failed"
v state put demo:note:1 '{"seen":true}' >/dev/null || die "state put failed"
escalated=
for _ in $(seq 1 100); do
	if haskv "$(v state get demo:task:hotfix-9 2>/dev/null || true)" stage escalated; then
		escalated=1
		break
	fi
	sleep 0.2
done
[ -n "$escalated" ] || die "the glob subject did not select demo:task:hotfix-9"
haskv "$(v state get demo:task:feature-9)" stage open || die "the glob subject escalated a task it excludes"
ok "!glob subject selected hotfix-9 and left feature-9 alone"

# ── `on`: a cause as the RISING EDGE of a store predicate ────────────────────
# The charters above dispatch on a change to their own subject. This one names its cause
# separately: it fires at the commit where `on` first holds, having not held before, and acts
# on whatever its subject selects then. Its own systems, so the charters above cannot reach it.
#
# The edge is the point, and it is what a unit test cannot check end to end: the write that
# makes the predicate FALSE must not fire it, and the one that makes it true must.
step "trigger with an \`on\` (a cause as a rising edge)"
v trigger add -f - >/dev/null <<'JSON' || die "on-charter add failed"
[ { "name": "smoke-on-green",
    "condition": { "on":      { "system": "smokeci", "kind": "status", "value": { "state": "green" } },
                   "subject": { "system": "demo", "kind": "build", "value": { "stage": "reviewed" } } },
    "gates":     [],
    "action":    { "patch": { "stage": "shipped" } } } ]
JSON
has "$(v trigger show smoke-on-green)" 'on' || die "the \`on\` did not survive install"
ok "installed a charter with an \`on\`"
v state put demo:build:1 '{"stage":"reviewed"}' >/dev/null || die "state put failed"
# The cause does not hold: no edge, no firing.
v state put smokeci:status:build '{"state":"red"}' >/dev/null || die "state put failed"
sleep 1
haskv "$(v state get demo:build:1)" stage reviewed || die "fired before its cause held"
ok "no firing while the cause is false"
# ...and now it rises.
v state put smokeci:status:build '{"state":"green"}' >/dev/null || die "state put failed"
shippedb=
for _ in $(seq 1 100); do
	if haskv "$(v state get demo:build:1 2>/dev/null || true)" stage shipped; then
		shippedb=1
		break
	fi
	sleep 0.2
done
[ -n "$shippedb" ] || die "the rising edge did not fire"
ok "the rising edge fired: demo:build:1 reviewed → shipped"

# ── the human loop: an ask gate HOLDS, and answering it releases the action ───
# The charters above prove the engine reacts on its own. This is the other half — the dial
# turned down to `ask` — and it is the one surface with no unit-level end-to-end: the gate
# has to reach `verse gate`, and the answer has to travel back and unblock the action.
#
# Answering is a VERB (`gate approve <id>`), not a flag on a `resolve` command, so the two
# states that are not a decision cannot be typed. The short forms are exercised here rather
# than the long ones because they are what an operator actually types at a held release.
step "gate: an ask gate holds; answering it releases the action"
v trigger add --file - >/dev/null <<'JSON' || die "gated charter add failed"
[ { "name": "smoke-gated",
    "condition": { "subject": { "system": "demo", "kind": "release", "value": { "stage": "open" } } },
    "gates":     [ { "name": "ship", "mode": "ask", "onDenied": { "patch": { "stage": "held" } } } ],
    "action":    { "patch": { "stage": "shipped" } } } ]
JSON
v state put demo:release:1 '{"stage":"open"}' >/dev/null || die "state put failed"
# A gate id carries colons (trigger:smoke-gated:ship~1), so it is cut by prefix, not by field.
GID=
for _ in $(seq 1 100); do
	GID=$(v gate -o tony | sed -n 's/^[[:space:]]*id:[[:space:]]*//p' | head -1)
	[ -n "$GID" ] && break
	sleep 0.2
done
[ -n "$GID" ] || die "the ask gate never appeared in verse gate"
ok "the action is held; verse gate shows $GID"
# Still open: an ask gate that let the action through would be indistinguishable from auto.
haskv "$(v state get demo:release:1)" stage open || die "the ask gate did not hold the action"
ok "demo:release:1 still open while the gate is pending"
v gate y "$GID" -s "ship it" --as human:smoke >/dev/null || die "gate approve (y) failed"
shipped=
for _ in $(seq 1 100); do
	if haskv "$(v state get demo:release:1 2>/dev/null || true)" stage shipped; then
		shipped=1
		break
	fi
	sleep 0.2
done
[ -n "$shipped" ] || die "the approved gate did not release the action"
ok "gate y + -s released it: demo:release:1 open → shipped"
has "$(v gate)" "$GID" && die "an answered gate is still pending"
ok "the answered gate left the docket"

# ── task runtime + the re-vector surface (scripted driver: hermetic) ──────
# The scripted driver runs its task as a real shell command, so `sleep 30` is a genuinely
# live task to exercise the control surfaces against (and observe the slide on).
step "task run + control surfaces"
v task run --driver scripted "sleep 30" >/dev/null || die "task run failed"
ID=$(v task -o tony | awk -F'[:[:space:]]+' '/^[[:space:]]*-?[[:space:]]*id:/ {print $NF; exit}')
[ -n "$ID" ] || die "no task id from list"
ok "spawned $ID (sleep 30)"
has "$(v task get "$ID")" "$ID" || die "task get missing the id"
ok "task get"
v task say "$ID" "also mention the token TTL" >/dev/null || die "task say failed"
ok "task say (re-prompt)"
out=$(v task vector "$ID" ask --as human:smoke) || die "task vector failed"
has "$out" "ask" || die "task vector did not report the new mode"
ok "task vector → ask (as human:smoke)"
# the slide is observable on the read plane (the task is live, so the mirror projects it)
observed=
for _ in $(seq 1 25); do
	if haskv "$(v state get "task:instance:$ID" 2>/dev/null || true)" vectorBy human:smoke; then
		observed=1
		break
	fi
	sleep 0.2
done
[ -n "$observed" ] || die "slide not observable on the entity (vector/vectorBy not projected)"
ok "slide observable on the entity (vector/vectorBy projected)"
v task cancel "$ID" >/dev/null 2>&1 || true # stop the sleep (frees the runtime promptly)
ok "task cancel (kills the running command)"

# ── connect: federate a local tree into a scope (docd-only path) ─────────────
step "connect (federate a local tree)"
mkdir -p "$tmp/tree"
printf 'hello from a\n' >"$tmp/tree/a.txt"
printf 'hello from b\n' >"$tmp/tree/b.txt"
SCOPE="smoke$$"
# direct invocation (see the up launch above) so kill -INT reaches connect, not a wrapper.
"$BIN" --addr "$ADDR" --scope "$SCOPE" connect "$tmp/tree" --driver scripted \
	--prompt 'echo checked $path' >"$tmp/connect.log" 2>&1 &
CONN=$!
fed=
for _ in $(seq 1 150); do
	out=$(v --scope "$SCOPE" state local 2>/dev/null || true)
	if has "$out" 'a.txt' && has "$out" 'b.txt'; then
		fed=1
		break
	fi
	kill -0 "$CONN" 2>/dev/null || break
	sleep 0.2
done
[ -n "$fed" ] || { tail -20 "$tmp/connect.log" >&2; die "connect did not federate the tree"; }
ok "federated a.txt + b.txt into scope $SCOPE"
# content is served on demand off a baseline mount (no --scope) — the subtree-watch path.
if has "$(v state get "local:dir-content-$SCOPE:a.txt" 2>/dev/null || true)" 'hello from a'; then
	ok "file content served through the verse (content mount)"
else
	note "content read did not match (mount timing) — federation verified above"
fi
# THE VALIDATOR RAN. Federation is only half of what connect promises — its startup line says
# "validator per file" — and this half was dead for as long as it existed while every check
# above passed: the rule's subject predicate selected nothing, and the completion hook under it
# failed on a reference a hook cannot reach. Both were silent. So the assertion is the end of
# the loop rather than any part of its middle: the file's own record says it was validated.
val=
for _ in $(seq 1 60); do
	if has "$(v --scope "$SCOPE" state get "local:dir-status-$SCOPE:a.txt" 2>/dev/null || true)" 'validated'; then
		val=1
		break
	fi
	sleep 0.5
done
[ -n "$val" ] || { v --scope "$SCOPE" state task >&2; die "the per-file validator never validated a.txt"; }
ok "the validator ran and latched: a.txt is marked validated"
# And it LATCHES: a validated file is not selected again, which is the whole reason the
# subject says what is outstanding rather than what exists.
# Counted with --paths, one address per line, rather than by grepping `ref:` out of a
# rendering: the answer is a document now, and an id is a key in it.
before=$(v --scope "$SCOPE" state --paths task | grep -c . || true)
sleep 3
after=$(v --scope "$SCOPE" state --paths task | grep -c . || true)
[ "$before" = "$after" ] || die "the validator re-spawned after latching ($before → $after)"
ok "a validated file is not validated again ($after tasks)"

kill -INT "$CONN" 2>/dev/null || true # ctrl-c tears the scope down
wait "$CONN" 2>/dev/null || true
CONN=""
ok "connect disconnected"

# ── status ───────────────────────────────────────────────────────────────────
step "status"
has "$(v status)" 'triggers' || die "status missing engine counts"
ok "status reports engine counts"

# ── strata: a declared level ordering, and a derive that crosses it ──────────
# The one path the unit tests cannot cover: a charter file holding BOTH strata and rules,
# fanned out by the CLI (strata first), installed over the wire, and firing across a level.
step "strata + derive (a write that is not the subject)"
cat >"$tmp/strata.tony" <<'TONY'
- name: smoke-observe
  level: 0
  match: "smokegh"
- name: smoke-sdlc
  level: 10
  match: "smokesd"
---
- name: smoke-derive
  stratum: smoke-observe
  condition:
    subject:
      system: smokegh
      kind: issue
      value:
        label: urgent
  action:
    patch:
      triaged: true
    derive:
      smokesd:
        feature:
          priority: urgent
- name: smoke-consume
  stratum: smoke-sdlc
  condition:
    subject:
      system: smokesd
      kind: feature
      value:
        priority: urgent
  action:
    patch:
      stage: awaiting-approval
TONY
v trigger add --file "$tmp/strata.tony" >/dev/null || die "mixed strata+rules charter did not install"
has "$(v stratum)" 'smoke-sdlc' || die "the ordering did not install"
ok "one file installs strata and rules, strata first"

v state put smokegh:issue:1 '{"label":"urgent"}' >/dev/null || die "state put failed"
derived=
for _ in $(seq 1 100); do
	out=$(v state --paths 2>/dev/null || true)
	if has "$out" 'smokesd:feature:'; then
		derived=1
		break
	fi
	sleep 0.2
done
[ -n "$derived" ] || die "the derive never landed"
ok "a rule at level 0 wrote an entity at level 10"

# The derived id is the engine's — f(rule, subject) — and the subject linkage with it.
dref=$(v state --paths smokesd.feature | head -1)
haskv "$(v state get "$dref")" priority urgent || die "the derived entity lost its literal content"
has "$(v state get "$dref")" 'smokegh:issue:1' || die "the derived entity does not record its subject"
ok "the engine wrote the linkage: $dref is about smokegh:issue:1"

# And the level above acted on it, which is the whole point of the ordering.
crossed=
for _ in $(seq 1 100); do
	if haskv "$(v state get "$dref" 2>/dev/null || true)" stage awaiting-approval; then
		crossed=1
		break
	fi
	sleep 0.2
done
[ -n "$crossed" ] || die "the stratum above did not act on what was derived"
ok "the cascade crossed the level"

# The refusals, over the wire: a derive that does not climb, and a stratum with rules in it.
v trigger add -f - >/dev/null 2>&1 <<'TONY' && die "a derive that does not climb was accepted"
- name: smoke-downward
  stratum: smoke-sdlc
  condition:
    subject: { system: smokesd, kind: feature }
  action:
    derive:
      smokegh:
        issue:
          a: 1
TONY
ok "a derive landing at or below its own level is refused at install"
v stratum remove smoke-observe >/dev/null 2>&1 && die "removed a stratum still holding rules"
ok "removing a stratum with rules in it is refused"

# ── restart: the charter is durable ──────────────────────────────────────────
# The rules installed above are entities (trigger:rule:<name>), so a second `up` over the
# same data dir comes back running the same program. This is the one check that exercises
# the real path end to end — down, up, and a change that arrives only afterwards.
step "restart (the charter survives)"
v down >/dev/null 2>&1 || true
wait "$SRV" 2>/dev/null || true
"$BIN" --addr "$ADDR" up --data "$tmp/data" --o-bin "$tmp/o" >"$tmp/server2.log" 2>&1 &
SRV=$!
ready=
for _ in $(seq 1 300); do
	if v status >/dev/null 2>&1; then
		ready=1
		break
	fi
	kill -0 "$SRV" 2>/dev/null || { tail -20 "$tmp/server2.log" >&2; die "server exited during restart"; }
	sleep 0.5
done
[ -n "$ready" ] || { tail -20 "$tmp/server2.log" >&2; die "server never became ready after restart" ; }
has "$(v trigger)" 'smoke-advance' || die "the charter did not survive the restart"
has "$(v stratum)" 'smoke-sdlc' || die "the ordering did not survive the restart"
has "$(v trigger)" 'smoke-derive' || die "a stratified rule did not survive the restart"
ok "the installed charter is listed again after a restart"
# And it is live, not merely listed: a PR that opens only now still advances.
v state put demo:pr:restart '{"stage":"open"}' >/dev/null || die "post-restart put failed"
advanced=
for _ in $(seq 1 40); do
	if haskv "$(v state get demo:pr:restart 2>/dev/null || true)" stage reviewed; then
		advanced=1
		break
	fi
	sleep 0.25
done
[ -n "$advanced" ] || die "the restored trigger did not fire on a new change"
ok "a restored trigger fires on a change that arrived after the restart"

# ── shutdown ─────────────────────────────────────────────────────────────────
step "down"
v down >/dev/null 2>&1 || true # the trap also reaps the server + its daemons
ok "down"

printf '\n\033[32mSMOKE PASSED\033[0m — %d checks\n' "$checks"
