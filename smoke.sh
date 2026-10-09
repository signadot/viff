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
# Somebody, and the smoke's own: a request that writes names who makes it, and whoever runs this
# may have no `user:` configured. A step that is about another party says so with --as.
export VERSE_USER="${VERSE_USER:-user:smoke}"
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

# ── data: write/read roundtrip ─────────────────────────────────────────────────
step "data --put / read"
v data demo pr 1 --put '{"stage":"open"}' >/dev/null || die "data --put failed"
haskv "$(v data demo pr 1)" stage open || die "data did not roundtrip"
ok 'demo.pr."1" = {stage: open}'
# THE ANSWER IS A DOCUMENT, addressed by the same path at any depth: the id is a KEY, not a
# token inside a rendered ref, and `stage` is what stands under it.
haskv "$(v data demo pr 1)" stage open || die "data demo pr 1 did not answer the payload"
has "$(v data demo pr)" '"1":' || die "data demo pr did not key by id"
# AS STEPS, which is the spelling this command line TAKES — so the listing hands straight back.
# A step is a whole word, so a dot inside one needs no quoting at all; the shell's own quoting
# is applied only where a segment would otherwise be split or expanded.
#
# `--paths` WALKS, so it names `demo pr 1 stage` beside the entity — every address, which is
# what makes the listing one that cannot be wrong about depth. `--depth 1` is what asks for the
# records alone — one level BENEATH the address, whatever level that address stands at — and it
# is the bound this assertion needs rather than an assumption about where the walk stops.
[ "$(v data --paths --depth 1 demo pr)" = 'demo pr 1' ] || die "--paths did not name the entity"
has "$(v data --paths demo pr)" 'demo pr 1 stage' || die "--paths did not walk into the payload"
# …and the same listing as ONE TOKEN per line, which is what survives a shell variable: a line
# of steps only reaches a read if something splits it, and `"$ref"` does not.
[ "$(v data --kpaths --depth 1 demo pr)" = 'demo.pr."1"' ] || die "--kpaths did not name the entity"
kref=$(v data --kpaths --depth 1 demo pr | head -1)
haskv "$(v data --at "$kref")" stage open || die "--kpaths did not round-trip through --at"
# The round trip is the one steps are for: the shell splits the line into arguments, which is
# what `xargs -L1` does with it.
v data --paths --depth 1 demo pr | while read -r p; do
	# shellcheck disable=SC2086
	haskv "$(v data $p)" stage open || die "--paths did not round-trip back into a read"
done
v data --paths --depth 1 demo pr | xargs -L1 "$BIN" --addr "$ADDR" data >/dev/null ||
	die "--paths did not pipe through xargs"
# An address may be typed as steps, which is what a shell makes easy: no quoting for an id with
# a dot, since each step arrives as its own word.
v data demo pr a.b --put '{stage: open}' >/dev/null || die "data as steps --put failed"
haskv "$(v data demo pr a.b)" stage open || die "data as steps did not roundtrip"
# …and the printed form is the same steps, with the dot still needing no quoting.
[ "$(v data --paths --depth 1 demo pr | grep 'a.b')" = 'demo pr a.b' ] || die "--paths did not name the dotted id as a step"
v data demo pr a.b --rm >/dev/null || die "data --rm of the dotted id failed"
ok "steps on the way in, the same steps on the way out"
# `has`, not `=`: a path can now answer with a BARE SCALAR at the document root, and the two
# renderings frame one differently — tony writes `open`, yaml writes it as a block scalar
# (`|` then the line). The value is verse's answer; the framing is the encoder's.
has "$(v data demo pr 1 stage)" open || die "a path into the payload did not answer"
ok "one path, four depths: demo / demo.pr / demo.pr.1 / demo.pr.1.stage"
# ONE ADDRESS on the verbs too: the path reaches inside the payload, which `--at` used to.
v data demo pr 1 labels urgent --put true >/dev/null || die "a deep put by path failed"
has "$(v data demo pr 1 labels)" urgent || die "the deep put did not land"
v data demo pr 1 labels urgent --rm >/dev/null || die "a deep rm by path failed"
case "$(v data demo pr 1)" in *urgent*) die "the deep rm did not take the field" ;; esac
# …and the two spellings are one grammar: a ref and its kinded path name the same place.
haskv "$(v data demo pr 1)" stage open || die "the kinded path did not reach the entity"
haskv "$(v data demo pr 1)" stage open || die "the ref spelling did not reach the entity"
has "$(v data demo pr 1 stage)" open || die "a path into the payload did not reach the field"
ok "one address: put/get/rm reach inside a payload, by ref or by path"

# ── grants on create: a node that holds out what its creator says from its first commit ──
step "data --put --grants (a node created private)"
VERSE_USER=user:alice@example.com v data notes alice todo --put '{todo: "buy milk"}' --grants '[]' >/dev/null ||
	die "data --put --grants failed"
has "$(VERSE_USER=user:alice@example.com v data notes alice todo)" 'buy milk' ||
	die "alice cannot read the note she created"
VERSE_USER=user:bob@example.com v data notes alice todo >/dev/null 2>&1 &&
	die "bob read a note created holding out nothing"
ok "alice's note, created with --grants '[]', is refused to bob"

# ── the representation: verse READS three dialects and WRITES two ────────────
# The asymmetry is the design (see package codec): JSON input is parsed as tony, because
# valid JSON is valid tony, but nothing renders JSON — a tagged charter has no faithful
# JSON spelling. Both halves are asserted here rather than left to the double-run, because
# a refusal that quietly became a fallback would still pass a suite run only one way.
step "representation (-o / negotiation)"
haskv "$(v data demo pr 1 -o tony)" stage open || die "-o tony did not render"
has "$(v data demo pr 1 -o yaml)" 'stage: open' || die "-o yaml did not render"
v data demo pr 1 -o tny >/dev/null 2>&1 && die "a bad -o value was accepted"
v data demo pr 1 -o json >/dev/null 2>&1 && die "-o json was accepted"
has "$(v data demo pr 1 -o json 2>&1 || true)" 'does not render json' \
	|| die "-o json failed without explaining itself"
ok "-o tony | yaml render; a bad value and json are rejected"
# --ifmt says how to READ what the operator supplied; it must not decide how the REQUEST is
# rendered, because verse renders no json. A WRITE is the case that proves the two are
# separate: --ifmt json used to fail every command that sends a body — i.e. every write —
# complaining about rendering json to someone who had only asked to type it.
v --ifmt json data demo pr 7 --put '{"stage":"open"}' >/dev/null || die "--ifmt json write failed"
haskv "$(v data demo pr 7)" stage open || die "the --ifmt json write did not land"
ok "--ifmt json writes (the input dialect is not the wire rendering)"
# curl straight at the API: no Accept header at all gets the configured default...
haskv "$(curl -s "http://$ADDR/entity?at=demo.pr.1")" stage open || die "the wire did not answer"
# And the document route, raw: the id is a KEY under the kind, not an `at:` field beside it.
raw=$(curl -s "http://$ADDR/state?at=demo.pr")
has "$raw" '"1":' || die "GET /state?at=demo.pr did not key by id: $raw"
has "$raw" 'stage' || die "GET /state?at=demo.pr did not carry the payload: $raw"
case "$raw" in *at:*) die "GET /state still renders an address beside the payload: $raw" ;; esac
# ...demanding JSON is 406, not tony wearing a JSON label...
code=$(curl -s -o /dev/null -w '%{http_code}' -H 'Accept: application/json' \
	"http://$ADDR/entity?at=demo.pr.1")
[ "$code" = "406" ] || die "Accept: application/json returned $code, want 406"
# ...and listing it as one preference among several is satisfiable, so it is satisfied.
haskv "$(curl -s -H 'Accept: application/json, application/tony;q=0.5' \
	"http://$ADDR/entity?at=demo.pr.1")" stage open || die "a mixed Accept did not negotiate"
ok "the wire negotiates (Accept honored, json refused with 406)"

# ── scope: copy-on-write isolation (docd overlays) ───────────────────────────
# Note: a scope created here is not torn down — there is no general scope teardown yet, only
# connect's. The throwaway data dir reclaims the overlay. See docs/scope-lifecycle.md.
step "scope isolation (--scope)"
SC="iso$$"
v --scope "$SC" data demo pr 9 --put '{"stage":"scoped-only"}' >/dev/null || die "scoped data --put failed"
has "$(v --scope "$SC" data demo pr 9)" 'scoped-only' || die "scope does not see its own write"
ok "scoped write visible inside scope $SC"
# the overlay must not leak into the baseline
if has "$(v data demo pr 9 2>/dev/null || true)" 'scoped-only'; then
	die "scoped write leaked into the baseline"
fi
ok "scoped write invisible in the baseline (isolation)"
# ...but a baseline entity IS visible from within the scope (copy-on-write read-through)
haskv "$(v --scope "$SC" data demo pr 1)" stage open || die "scope cannot read the baseline (no read-through)"
ok "baseline entity readable from the scope (read-through)"

# ── reactive core: a non-gated trigger auto-advances its subject ─────────────
step "charter -a + drive (reactive core)"
# The charter below is written in JSON — still accepted, and this is the check that says
# so. It arrives on stdin with no name, so nothing declares a dialect: it parses because
# JSON *is* tony, not because anything sniffed it.
v charter -a -f - >/dev/null <<'JSON' || die "charter -a failed"
[ { "name": "smoke-advance",
    "condition": { "subject": { "path": "demo.pr.*", "value": { "stage": "open" } } },
    "gates":     [],
    "action":    { "patch": { "stage": "reviewed" } } } ]
JSON
ok "charter installed"
# putting a matching entity fires the trigger, which advances the matched subject.
v data demo pr 2 --put '{"stage":"open"}' >/dev/null || die "data --put failed"
advanced=
for _ in $(seq 1 100); do
	if haskv "$(v data demo pr 2 2>/dev/null || true)" stage reviewed; then
		advanced=1
		break
	fi
	sleep 0.2
done
[ -n "$advanced" ] || die "trigger did not advance demo.pr.2 open → reviewed"
ok "demo.pr.2 auto-advanced open → reviewed"

# ── the same charter, written in tony: the FILE SUFFIX picks the dialect ─────
# The JSON case above pipes on stdin (no name, so the configured default applies); this one
# is a named .tony file, which must be read as tony whatever the default is. It also uses a
# block list rather than [ ... ], which the old "starts with [" sniff could not have seen.
# Between them they are the promise: author in either syntax, get one grammar.
step "charter -a reads a .tony charter (suffix dispatch)"
cat >"$tmp/charter.tony" <<'TONY'
- name: smoke-advance-tony
  condition:
    subject:
      path: "demo.pr.*"
      value:
        stage: queued
  gates: []
  action:
    patch:
      stage: reviewed
TONY
# --file here and `-f` on the stdin case above: both spellings are exercised on purpose,
# since smoke is the only thing that would catch the alias disappearing.
v charter -a --file "$tmp/charter.tony" >/dev/null || die "tony charter -a failed"
has "$(v charter)" 'smoke-advance-tony' || die "the tony charter did not install"
ok "installed a .tony charter (block list, suffix-dispatched)"

v data demo pr 3 --put '{"stage":"queued"}' >/dev/null || die "data --put failed"
advanced=
for _ in $(seq 1 100); do
	if haskv "$(v data demo pr 3 2>/dev/null || true)" stage reviewed; then
		advanced=1
		break
	fi
	sleep 0.2
done
[ -n "$advanced" ] || die "the tony-authored trigger did not fire"
ok "a tony-authored trigger fires identically to its JSON twin"

# ── an operator on a path segment ────────────────────────────────────────────
# A subject's PREDICATE may carry a tony match, which is what selects a family at a place — the
# path says where, the predicate says which. Installing one exercises the whole path the unit
# tests cover in pieces: tag → Validate → the engine's selection, and the rendering back.
step "trigger subject with an operator in its predicate"
# demo.task, not demo.pr: the charters above match every demo.pr, and a subject the fan-out
# was supposed to exclude would then be indistinguishable from one they had already moved.
v charter -a -f - >/dev/null <<'TONY' || die "operator charter -a failed"
- name: smoke-hotfix-only
  condition:
    when: {path: "demo.note.*", value: {seen: true}}
    subject: {path: "demo.task.*", value: {branch: !glob "hotfix-*"}}
  gates: []
  action:
    patch: {stage: escalated}
TONY
has "$(v charter rules smoke-hotfix-only)" '!glob' || die "the operator did not survive install"
ok "installed; the operator renders back as a tag"
# two candidates, one matching the glob and one not — the fan-out must pick exactly one.
v data demo task hotfix-9 --put '{"stage":"open","branch":"hotfix-9"}' >/dev/null || die "data --put failed"
v data demo task feature-9 --put '{"stage":"open","branch":"feature-9"}' >/dev/null || die "data --put failed"
v data demo note 1 --put '{"seen":true}' >/dev/null || die "data --put failed"
escalated=
for _ in $(seq 1 100); do
	if haskv "$(v data demo task hotfix-9 2>/dev/null || true)" stage escalated; then
		escalated=1
		break
	fi
	sleep 0.2
done
[ -n "$escalated" ] || die "the glob predicate did not select demo.task.hotfix-9"
haskv "$(v data demo task feature-9)" stage open || die "the glob predicate escalated a task it excludes"
ok "!glob predicate selected hotfix-9 and left feature-9 alone"

# ── `on`: a cause as the RISING EDGE of a store predicate ────────────────────
# The charters above dispatch on a change to their own subject. This one names its cause
# separately: it fires at the commit where `on` first holds, having not held before, and acts
# on whatever its subject selects then. Its own places, so the charters above cannot reach it.
#
# The edge is the point, and it is what a unit test cannot check end to end: the write that
# makes the predicate FALSE must not fire it, and the one that makes it true must.
step "trigger with an \`on\` (a cause as a rising edge)"
v charter -a -f - >/dev/null <<'JSON' || die "on-charter -a failed"
[ { "name": "smoke-on-green",
    "condition": { "on":      { "path": "smokeci.status.*", "value": { "state": "green" } },
                   "subject": { "path": "demo.build.*", "value": { "stage": "reviewed" } } },
    "gates":     [],
    "action":    { "patch": { "stage": "shipped" } } } ]
JSON
has "$(v charter rules smoke-on-green)" 'on' || die "the \`on\` did not survive install"
ok "installed a charter with an \`on\`"
v data demo build 1 --put '{"stage":"reviewed"}' >/dev/null || die "data --put failed"
# The cause does not hold: no edge, no firing.
v data smokeci status build --put '{"state":"red"}' >/dev/null || die "data --put failed"
sleep 1
haskv "$(v data demo build 1)" stage reviewed || die "fired before its cause held"
ok "no firing while the cause is false"
# ...and now it rises.
v data smokeci status build --put '{"state":"green"}' >/dev/null || die "data --put failed"
shippedb=
for _ in $(seq 1 100); do
	if haskv "$(v data demo build 1 2>/dev/null || true)" stage shipped; then
		shippedb=1
		break
	fi
	sleep 0.2
done
[ -n "$shippedb" ] || die "the rising edge did not fire"
ok "the rising edge fired: demo.build.1 reviewed → shipped"

# ── the human loop: an ask gate HOLDS, and answering it releases the action ───
# The charters above prove the engine reacts on its own. This is the other half — the dial
# turned down to `ask` — and it is the one surface with no unit-level end-to-end: the gate
# has to reach `verse question`, and the answer has to travel back and unblock the action.
#
# Answering is a VERB (`question approve <id>`), not a flag on a `resolve` command, so the two
# states that are not a decision cannot be typed. The short forms are exercised here rather
# than the long ones because they are what an operator actually types at a held release.
step "gate: an ask gate holds; answering it releases the action"
v charter -a --file - >/dev/null <<'JSON' || die "gated charter -a failed"
[ { "name": "smoke-gated",
    "condition": { "subject": { "path": "demo.release.*", "value": { "stage": "open" } } },
    "gates":     [ { "name": "ship", "mode": "ask", "onDenied": { "patch": { "stage": "held" } } } ],
    "action":    { "patch": { "stage": "shipped" } } } ]
JSON
v data demo release 1 --put '{"stage":"open"}' >/dev/null || die "data --put failed"
# A gate id is a whole path with a `~` in it (verse.trigger.smoke-gated.ship~1), so it is cut by
# prefix, not by field.
GID=
for _ in $(seq 1 100); do
	GID=$(v question -o tony | sed -n 's/^[[:space:]]*id:[[:space:]]*//p' | head -1)
	[ -n "$GID" ] && break
	sleep 0.2
done
[ -n "$GID" ] || die "the ask gate never appeared in verse question"
ok "the action is held; verse question shows $GID"
# Still open: an ask gate that let the action through would be indistinguishable from auto.
haskv "$(v data demo release 1)" stage open || die "the ask gate did not hold the action"
ok "demo.release.1 still open while the gate is pending"
v question y "$GID" -s "ship it" --as user:smoke >/dev/null || die "question approve (y) failed"
shipped=
for _ in $(seq 1 100); do
	if haskv "$(v data demo release 1 2>/dev/null || true)" stage shipped; then
		shipped=1
		break
	fi
	sleep 0.2
done
[ -n "$shipped" ] || die "the approved gate did not release the action"
ok "question y + -s released it: demo.release.1 open → shipped"
has "$(v question --all)" "$GID" && die "an answered gate is still pending"
ok "the answered gate left the docket"

# ── the tally: a gate that takes two approvals, settled on the second ────────
# The same docket as the ask gate above and the same verb. What differs is that one approval is
# recorded and does not release, the answerer is told where the tally stands, and the record
# settles in the same write as the ballot that reaches the threshold.
step "gate: a threshold gate holds until enough approve"
v charter -a --file - >/dev/null <<'TONY' || die "threshold charter -a failed"
- name: smoke-quorum
  condition:
    subject: {path: "demo.motion.*", value: {stage: open}}
  gates:
  - {name: approvals, need: 2}
  action:
    patch: {stage: shipped}
TONY
# alice and bob may write the motion, so they vote on it: a gate naming nobody is answered by
# whoever may write what it is about.
v data demo motion 1 --put '{"stage":"open"}' --grants '[{read: ["*"], read-write: [user:alice, user:bob]}]' >/dev/null ||
	die "data --put failed"
QID=
for _ in $(seq 1 100); do
	# `|| true`: under pipefail a poll that finds nothing yet is grep's exit 1, and set -e would end
	# the script on it — the retry never ran, and the step failed silently whenever the question
	# was not on the docket at the first poll.
	QID=$(v question --all -o tony | sed -n 's/^[[:space:]]*id:[[:space:]]*//p' | grep smoke-quorum | head -1 || true)
	[ -n "$QID" ] && break
	sleep 0.2
done
[ -n "$QID" ] || die "the threshold gate never reached the docket"
has "$(v question --all)" 'demo.motion."1"' || die "the docket does not say what is being decided"
ok "the question is on the docket as $QID, about demo.motion.1"
out=$(v question y "$QID" --as user:alice) || die "the first ballot was refused"
has "$out" "1 of 2" || die "the answerer was not told where the tally stands: $out"
sleep 0.5
haskv "$(v data demo motion 1)" stage open || die "one approval released a gate that needs two"
has "$(v question --all)" "$QID" || die "a question one short of its threshold left the docket"
ok "one of two: recorded, and the action is still held"
v question y "$QID" --as user:bob >/dev/null || die "the second ballot was refused"
carried=
for _ in $(seq 1 100); do
	if haskv "$(v data demo motion 1 2>/dev/null || true)" stage shipped; then
		carried=1
		break
	fi
	sleep 0.2
done
[ -n "$carried" ] || die "the second approval did not release the action"
ok "two of two: demo.motion.1 open → shipped"
rec=$(v data verse ask "$QID")
has "$rec" "user:alice" && has "$rec" "user:bob" || die "the settled record does not carry both ballots: $rec"
haskv "$rec" verdict proceed || die "the settled record does not say what it settled on"
ok "the question is settled with both ballots on the record"

# ── ask: a person asks a person, and is told the answer ─────────────────────
# Every verse asks: nothing is installed for it. alice asks bob, bob answers on the docket, and
# alice reads the answer off her ask and off the watch.
step "ask: alice asks bob, bob answers, alice sees it"
LAND=$(VERSE_USER=user:alice v ask land -q "Review verse/ask?" --to user:bob --due 48h) || die "alice could not ask"
LAND=${LAND#asked }
LAND=${LAND%%:*}
# The ask is the question: its id is what bob answers, once it is posed and waits on him.
AID=$LAND
posed=
for _ in $(seq 1 100); do
	if has "$(VERSE_USER=user:alice v ask land 2>/dev/null || true)" "waitingOn"; then
		posed=1
		break
	fi
	sleep 0.2
done
[ -n "$posed" ] || die "alice's ask was never posed: $(VERSE_USER=user:alice v ask land)"
has "$(VERSE_USER=user:alice v ask land)" "user:bob" || die "alice's ask does not say it waits on bob"
ok "alice's ask is posed as $AID, waiting on bob"
v question "$AID" --answer proceed --as user:bob >/dev/null || die "bob could not answer"
answered=
for _ in $(seq 1 100); do
	if haskv "$(VERSE_USER=user:alice v ask land 2>/dev/null || true)" answer yes; then
		answered=1
		break
	fi
	sleep 0.2
done
[ -n "$answered" ] || die "alice's ask never showed bob's yes"
ok "alice sees bob's yes on her ask"
VERSE_USER=user:alice v ask --watch --since 0 >"$tmp/askwatch.out" 2>&1 &
wpid=$!
for _ in $(seq 1 50); do
	grep -q "answered $LAND yes" "$tmp/askwatch.out" && break
	sleep 0.2
done
kill "$wpid" 2>/dev/null || true
wait "$wpid" 2>/dev/null || true
has "$(cat "$tmp/askwatch.out")" "answered $LAND yes" || die "alice's watch did not say the answer: $(cat "$tmp/askwatch.out")"
ok "alice's watch, from the beginning, says it was answered"

# ── resolver: a service answers for whoever grants it, and asks onward first ─────
# dan runs a service, installs it as a resolver, and carol lets it answer for her. alice asks carol;
# the service, acting as the resolver, sees the ask it serves, asks erin onward, and erin's yes is
# carried back as carol's answer, through the resolver.
step "resolver: carol's desk answers for her, after asking erin"
VERSE_USER=user:dan v resolver add - >/dev/null <<'TONY' || die "dan could not install the resolver"
name: carols-desk
match: {}
method: {driver: service}
TONY
has "$(VERSE_USER=user:carol v resolver grant carols-desk)" "answers for you" || die "carol could not grant the desk"
SHIP=$(VERSE_USER=user:alice v ask ship -q "Ship it?" --to user:carol) || die "alice could not ask carol"
SHIP=${SHIP#asked }
SHIP=${SHIP%%:*}
desk() { curl -s -H 'Verse-Principal: user:dan' -H 'Verse-Resolver: carols-desk' "$@"; }
served=
for _ in $(seq 1 100); do
	if has "$(desk "http://$ADDR/resolvers/carols-desk/asks" || true)" "$SHIP"; then
		served=1
		break
	fi
	sleep 0.2
done
[ -n "$served" ] || die "the desk never served alice's ask: $(desk "http://$ADDR/resolvers/carols-desk/asks")"
has "$(desk "http://$ADDR/resolvers/carols-desk/asks")" "user:carol" || die "the desk does not say it serves the ask for carol"
ok "the desk, acting as the resolver, serves $SHIP for carol"
UP=$(desk -H 'Content-Type: application/tony' -d "{name: up, question: \"Ship alice's?\", answerers: [user:erin], serves: $SHIP}" \
	"http://$ADDR/asks" | sed -n 's/^id:[[:space:]]*//p')
[ -n "$UP" ] || die "the desk could not raise an onward ask"
upposed=
for _ in $(seq 1 100); do
	if has "$(VERSE_USER=user:erin v question 2>/dev/null || true)" "$UP"; then
		upposed=1
		break
	fi
	sleep 0.2
done
[ -n "$upposed" ] || die "the onward ask never reached erin's docket"
ok "the desk asked erin onward, as $UP"
v question "$UP" --answer proceed --as user:erin >/dev/null || die "erin could not answer the onward ask"
carried=
for _ in $(seq 1 100); do
	if haskv "$(VERSE_USER=user:alice v ask ship 2>/dev/null || true)" answer yes; then
		carried=1
		break
	fi
	sleep 0.2
done
[ -n "$carried" ] || die "erin's yes was never carried back to alice's ask: $(VERSE_USER=user:alice v data verse ask "$SHIP")"
rec=$(VERSE_USER=user:alice v data verse ask "$SHIP")
haskv "$rec" by "user:carol" && has "$rec" "via:verse.resolvers.carols-desk" ||
	die "alice's ask was not answered as carol's, through the desk: $rec"
ok "erin's yes came back on alice's ask as carol's, via the desk"

# ── suspend: a charter holds, and a resume acts once on the world as it stands ──
# A package, so the charter has an address to suspend by: a level rule and a gated one. While it
# is suspended, a write that would fire the one fires nothing and the other asks nobody; resumed,
# each acts on what matches it now, once.
step "suspend: a suspended charter fires nothing; resuming acts on the world as it stands"
v charter -a smoke-susp -f - >/dev/null <<'TONY' || die "suspend charter -a failed"
- name: settle
  condition:
    subject: {path: "smoke-susp.level.*", value: {stage: open}}
  action:
    patch: {stage: settled}
- name: approve
  condition:
    subject: {path: "smoke-susp.gated.*", value: {stage: open}}
  gates:
  - {name: ok, mode: ask}
  action:
    patch: {stage: approved}
TONY
out=$(v flow suspend 2>&1) && die "a bare verse flow suspend was accepted: $out"
has "$out" "--charter" || die "a bare verse flow suspend does not print its usage: $out"
has "$(v status)" "suspensions:" && die "a bare verse flow suspend wrote a suspension"
ok "a bare verse flow suspend prints its usage and suspends nothing"
v flow suspend --charter smoke-susp --why "smoke: hold the package" >/dev/null || die "verse flow suspend --charter smoke-susp failed"
has "$(v flow)" 'smoke: hold the package' || die "the listing does not say why it is held"
ok "suspended the charter at smoke-susp, and the listing says why"
v data smoke-susp level 1 --put '{stage: open}' >/dev/null || die "data --put failed"
v data smoke-susp gated 1 --put '{stage: open}' >/dev/null || die "data --put failed"
sleep 1
haskv "$(v data smoke-susp level 1)" stage open || die "a suspended level rule fired"
has "$(v question --all)" 'smoke-susp' && die "a suspended gated rule put a question on the docket"
st=$(v status)
has "$st" "suspensions:" && haskv "$st" count 1 || die "status does not count the suspension: $st"
ok "nothing fired, nothing was asked, and status counts suspensions: {count: 1}"
args=$(v flow --charter smoke-susp --args) || die "verse flow --charter smoke-susp --args failed"
[ "$args" = "-charter smoke-susp -subject '*'" ] || die "the listing's --args is not the flags that name it: $args"
printf '%s\n' "$args" | xargs -L1 "$BIN" --addr "$ADDR" flow resume >/dev/null || die "xargs -L1 verse flow resume over --args failed"
settled=
for _ in $(seq 1 100); do
	if haskv "$(v data smoke-susp level 1 2>/dev/null || true)" stage settled; then
		settled=1
		break
	fi
	sleep 0.2
done
[ -n "$settled" ] || die "the resumed level rule did not act on what matches it"
asked=
for _ in $(seq 1 100); do
	if has "$(v question --all)" 'smoke-susp'; then
		asked=1
		break
	fi
	sleep 0.2
done
[ -n "$asked" ] || die "the resumed gated rule did not ask"
has "$(v flow)" 'smoke-susp' && die "the lifted suspension is still listed"
ok "resumed through verse flow --args | xargs -L1 verse flow resume: settle acted, approve asked, and nothing is suspended"

# ── task runtime + the re-vector surface (scripted driver: hermetic) ──────
# The scripted driver runs its task as a real shell command, so `sleep 30` is a genuinely
# live task to exercise the control surfaces against (and observe the slide on).
# One principal for the run and its control: a task is its requester's, and control is held
# by the owner and the runner, so the party that spawned it is the party that steers it.
step "task run + control surfaces"
VERSE_USER=user:smoke v task run --driver scripted "sleep 30" >/dev/null || die "task run failed"
ID=$(v task -o tony | awk -F'[:[:space:]]+' '/^[[:space:]]*-?[[:space:]]*id:/ {print $NF; exit}')
[ -n "$ID" ] || die "no task id from list"
ok "spawned $ID (sleep 30)"
has "$(v task get "$ID")" "$ID" || die "task get missing the id"
ok "task get"
VERSE_USER=user:smoke v task say "$ID" "also mention the token TTL" >/dev/null || die "task say failed"
ok "task say (re-prompt)"
out=$(VERSE_USER=user:smoke v task vector "$ID" ask --as user:smoke) || die "task vector failed"
has "$out" "ask" || die "task vector did not report the new mode"
ok "task vector → ask (as user:smoke)"
# the slide is observable on the read plane (the task is live, so the mirror projects it)
observed=
for _ in $(seq 1 25); do
	if haskv "$(v data verse task instance "$ID" 2>/dev/null || true)" vectorBy user:smoke; then
		observed=1
		break
	fi
	sleep 0.2
done
[ -n "$observed" ] || die "slide not observable on the entity (vector/vectorBy not projected)"
ok "slide observable on the entity (vector/vectorBy projected)"
VERSE_USER=user:smoke v task cancel "$ID" >/dev/null 2>&1 || true # stop the sleep (frees the runtime promptly)
ok "task cancel (kills the running command)"

# ── scopes: a scoped write is the scope, and a scope owns only what it creates ───────
step "scopes"
SCOPE="smoke$$"
v --scope "$SCOPE" data smokescope x 1 --put '{k: v}' >/dev/null || die "a scoped --put failed"
haskv "$(v --scope "$SCOPE" data smokescope x 1)" k v || die "the scope does not read its own write"
v data smokescope x 1 >/dev/null 2>&1 && die "a scoped write is visible from baseline"
ok "a write under --scope $SCOPE lands in the scope and not in baseline"
# A suspension is a control write, and a scope owns what it creates: from the scope, baseline's
# package is refused with the sentence every control write refuses in.
out=$(v --scope "$SCOPE" flow suspend --charter smoke-susp --why "not mine" 2>&1) && die "a scope suspended baseline's package: $out"
has "$out" "baseline's" || die "the scope's refusal does not say whose the charter is: $out"
ok "--scope $SCOPE suspend of baseline's package is refused, naming baseline"

# ── status ───────────────────────────────────────────────────────────────────
step "status"
# `rules` is inside the charter census now — counted where each charter is, since the top-level
# count was baseline's alone and read 0 on a verse whose program is packaged.
has "$(v status)" 'rules' || die "status missing the charter census"
has "$(v status)" 'storeRev' || die "status missing engine counts"
ok "status reports engine counts"

# ── strata: a declared level ordering, and a derive that crosses it ──────────
# The one path the unit tests cannot cover: a charter file holding BOTH strata and rules,
# fanned out by the CLI (strata first), installed over the wire, and firing across a level.
step "strata + derive (a write that is not the subject)"
cat >"$tmp/strata.tony" <<'TONY'
strata:
- name: smoke-observe
  level: 0
  match: "smokegh"
- name: smoke-sdlc
  level: 10
  match: "smokesd"
rules:
- name: smoke-derive
  stratum: smoke-observe
  condition:
    subject:
      path: "smokegh.issue.*"
      value:
        label: urgent
  action:
    patch:
      triaged: true
    derive:
    - path: "smokesd.feature"
      value:
        priority: urgent
- name: smoke-consume
  stratum: smoke-sdlc
  condition:
    # SIX SEGMENTS: the derive's target, the rule that derived, and the three of the issue it
    # derived about. A derived entity is as deep as what it is about, so a rule that reads one
    # says how deep it is.
    subject:
      path: "smokesd.feature.*.*.*.*"
      value:
        priority: urgent
  action:
    patch:
      stage: awaiting-approval
TONY
v charter -a --file "$tmp/strata.tony" >/dev/null || die "mixed strata+rules charter did not install"
has "$(v charter)" 'smoke-sdlc' || die "the ordering did not install"
ok "one file installs strata and rules, strata first"

v data smokegh issue 1 --put '{"label":"urgent"}' >/dev/null || die "data --put failed"
derived=
for _ in $(seq 1 100); do
	out=$(v data --paths 2>/dev/null || true)
	if has "$out" 'smokesd feature smoke-derive smokegh issue 1'; then
		derived=1
		break
	fi
	sleep 0.2
done
[ -n "$derived" ] || die "the derive never landed"
ok "a rule at level 0 wrote an entity at level 10"

# The derived ADDRESS is the engine's — the target, the rule, then the subject's own
# segments — and the linkage record with it.
# BOTH WAYS, because `--paths` answers in steps and a script may hand the line over whole or let
# the shell split it. A whole line quoted as one token is the same address as the same line split
# into arguments, and a script that captured a path and quoted it used to read nothing.
dref=$(v data --paths --depth 4 smokesd feature | tail -1)
# shellcheck disable=SC2086
haskv "$(v data $dref)" priority urgent || die "the derived entity lost its literal content"
# shellcheck disable=SC2086
has "$(v data $dref)" 'smokegh.issue."1"' || die "unquoted steps did not reach the same entity"
ok "the address carries its provenance: $dref"

# And the level above acted on it, which is the whole point of the ordering.
crossed=
for _ in $(seq 1 100); do
	# shellcheck disable=SC2086
	if haskv "$(v data $dref 2>/dev/null || true)" stage awaiting-approval; then
		crossed=1
		break
	fi
	sleep 0.2
done
[ -n "$crossed" ] || die "the stratum above did not act on what was derived"
ok "the cascade crossed the level"

# The refusals, over the wire: a derive that does not climb, and a stratum with rules in it.
v charter -a -f - >/dev/null 2>&1 <<'TONY' && die "a derive that does not climb was accepted"
- name: smoke-downward
  stratum: smoke-sdlc
  condition:
    subject: {path: "smokesd.feature.*"}
  action:
    derive:
    - path: "smokegh.issue"
      value:
        a: 1
TONY
ok "a derive landing at or below its own level is refused at install"
v charter -r strata smoke-observe >/dev/null 2>&1 && die "removed a stratum still holding rules"
ok "removing a stratum with rules in it is refused"

# ── restart: the charter is durable ──────────────────────────────────────────
# The rules installed above are records (verse.trigger.rule.<name>), so a second `up` over the
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
has "$(v charter)" 'smoke-advance' || die "the charter did not survive the restart"
has "$(v charter)" 'smoke-sdlc' || die "the ordering did not survive the restart"
has "$(v charter)" 'smoke-derive' || die "a stratified rule did not survive the restart"
ok "the installed charter is listed again after a restart"
# And it is live, not merely listed: a PR that opens only now still advances.
v data demo pr restart --put '{"stage":"open"}' >/dev/null || die "post-restart put failed"
advanced=
for _ in $(seq 1 40); do
	if haskv "$(v data demo pr restart 2>/dev/null || true)" stage reviewed; then
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

# ── the quick start: a fresh verse, and nobody configured ────────────────────
# What the README tells a new developer to type, as they arrive: no config, no VERSE_USER, a
# verse on this machine. Every seed script writes, so each one running is a check that a CLI
# with nobody configured acts as somebody here (the local user), and that the scripts a
# newcomer meets first still say what the CLI takes.
#
# A verse of its own, because the scripts seed baseline: beside the rules the steps above
# installed, what a seeded entity does would be theirs to decide as much as the seed's.
step "quick start (a fresh verse, nobody configured)"
QADDR="127.0.0.1:$(free_port)" || die "could not reserve an ephemeral port"
# The environment a newcomer has: no user, no agent, and no config file in either layer.
q() { env -u VERSE_USER -u VERSE_AGENT VERSE_CONFIG="$tmp/none.tony" VERSE="$BIN" VERSE_ADDR="$QADDR" "$@"; }
env -u VERSE_USER -u VERSE_AGENT VERSE_CONFIG="$tmp/none.tony" \
	"$BIN" --addr "$QADDR" up --data "$tmp/quick" --o-bin "$tmp/o" >"$tmp/quick.log" 2>&1 &
SRV=$!
ready=
for _ in $(seq 1 300); do
	if q "$BIN" status >/dev/null 2>&1; then
		ready=1
		break
	fi
	kill -0 "$SRV" 2>/dev/null || { tail -20 "$tmp/quick.log" >&2; die "the quick start's server exited during startup"; }
	sleep 0.5
done
[ -n "$ready" ] || { tail -20 "$tmp/quick.log" >&2; die "the quick start's server never became ready"; }

q ./scripts/seed-demo.sh >"$tmp/seed-demo.out" 2>&1 ||
	{ cat "$tmp/seed-demo.out" >&2; die "seed-demo.sh failed with nobody configured"; }
haskv "$(q "$BIN" data demo task hotfix-1)" stage escalated || die "seed-demo.sh: demo-escalate did not advance demo.task.hotfix-1"
has "$(cat "$tmp/seed-demo.out")" 'is holding demo.pr.1 at a gate' || { cat "$tmp/seed-demo.out" >&2; die "seed-demo.sh: no gate is waiting"; }
ok "seed-demo.sh: the charter, its entities, and a gate waiting"
has "$(q "$BIN" perms demo)" '@localhost' || die "what the quick start wrote is not the local user's: $(q "$BIN" perms demo)"
ok "what it wrote is the local user's"
# The answer the script says to type next, as it says to type it: no --as.
GID=$(q "$BIN" question -o tony | sed -n 's/^[[:space:]]*id:[[:space:]]*//p' | head -1)
q "$BIN" question approve "$GID" >/dev/null || die "the gate could not be answered with nobody configured"
ok "the waiting gate is answered with no --as"
q ./scripts/seed-demo.sh --clean >/dev/null 2>&1 || die "seed-demo.sh --clean failed"

q ./scripts/seed-entities.sh >"$tmp/seed-entities.out" 2>&1 ||
	{ cat "$tmp/seed-entities.out" >&2; die "seed-entities.sh failed with nobody configured"; }
haskv "$(q "$BIN" data human person ana)" team platform || die "seed-entities.sh: human.person.ana is not there"
ok "seed-entities.sh: a world to select over"

for shape in patch derive; do
	q ./scripts/charter-shapes.sh run "$shape" >"$tmp/shape.out" 2>&1 ||
		{ cat "$tmp/shape.out" >&2; die "charter-shapes.sh run $shape failed with nobody configured"; }
done
ok "charter-shapes.sh: a shape installs and is driven"

# ── shutdown, again ──────────────────────────────────────────────────────────
step "down (the quick start's verse)"
q "$BIN" down >/dev/null 2>&1 || true # the trap also reaps the server + its daemons
ok "down"

printf '\n\033[32mSMOKE PASSED\033[0m — %d checks\n' "$checks"
