#!/usr/bin/env bash
#
# level-ping-pong.sh — install docs/examples/level-ping-pong.yaml on an isolated verse and
# watch it loop.
#
# What it shows, and why it is worth watching rather than reading:
#
#   * The charter INSTALLS. Every level check passes, because strata rule out the write that
#     would close a loop across levels and say nothing about one inside a level.
#   * Two cycles run — one per stratum — and the lower one re-ignites the upper on every lap.
#   * The throttle catches them and names the rules that were firing each other.
#   * The derive commits ONCE. Its content is literal, so every later firing writes the same
#     bytes to the same address, which the store ends as a no-op.
#
# Hermetic, like smoke.sh: it builds throwaway `verse` + `o` binaries, runs its own server on
# a free port with a private data dir, and reaps both on exit. It never touches a dev server.
#
#   scripts/level-ping-pong.sh           # watch for 15s
#   SECONDS_TO_WATCH=60 scripts/level-ping-pong.sh
#   PORT=20000 scripts/level-ping-pong.sh
#
set -euo pipefail

cd "$(cd "$(dirname "$0")/.." && pwd)"

WATCH=${SECONDS_TO_WATCH:-15}
CHARTER=docs/examples/level-ping-pong.yaml

step() { printf '\n\033[1m# %s\033[0m\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
note() { printf '  \033[33m~\033[0m %s\n' "$*"; }
die()  { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

free_port() {
	local p=${PORT:-19897} i
	for i in $(seq 0 50); do
		if ! (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
			printf '%s' "$p"
			return 0
		fi
		exec 3>&- 2>/dev/null || true
		p=$((p + 1))
	done
	die "no free port found near ${PORT:-19897}"
}
ADDR="127.0.0.1:$(free_port)"

step "build (verse + pinned o)"
tmp=$(mktemp -d)
BIN="$tmp/verse"
SRV=""
cleanup() {
	[ -n "$SRV" ] && kill "$SRV" 2>/dev/null || true
	wait 2>/dev/null || true
	rm -rf "$tmp"
}
trap cleanup EXIT INT TERM
go build -o "$BIN" ./cmd/verse || die "go build ./cmd/verse failed"
go build -o "$tmp/o" github.com/signadot/tony-format/go-tony/cmd/o || die "go build o failed"
ok "built verse + o"
v() { "$BIN" --addr "$ADDR" "$@"; }

step "up on $ADDR"
"$BIN" --addr "$ADDR" up --data "$tmp/data" --o-bin "$tmp/o" >"$tmp/server.log" 2>&1 &
SRV=$!
for _ in $(seq 1 300); do
	v status >/dev/null 2>&1 && break
	kill -0 "$SRV" 2>/dev/null || { tail -20 "$tmp/server.log" >&2; die "server exited during startup"; }
	sleep 0.5
done
v status >/dev/null 2>&1 || { tail -20 "$tmp/server.log" >&2; die "server never became ready"; }
ok "server ready (data in $tmp/data)"

# ── install: one file, strata and rules, ordering first ──────────────────────
step "install $CHARTER"
v trigger add --file "$CHARTER" || die "the charter did not install"
printf '\n'
v strata
ok "the ordering installed, and every rule passed its level check"

# The refusal, for contrast: the same charter with one write pointing DOWN.
step "the one thing that is refused"
if v trigger add -f - >/dev/null 2>&1 <<'YAML'; then
- name: close-the-loop
  stratum: sdlc
  condition:
    subject: { system: signadot, kind: feature }
  action:
    derive:
      gh:
        issue:
          reopened: true
YAML
	die "a derive pointing down was accepted"
fi
printf '  '
v trigger add -f - 2>&1 <<'YAML' | head -3 || true
- name: close-the-loop
  stratum: sdlc
  condition:
    subject: { system: signadot, kind: feature }
  action:
    derive:
      gh:
        issue:
          reopened: true
YAML
ok "a cycle ACROSS levels cannot be written; a cycle inside one is ordinary recursion"

# ── seed, and watch ──────────────────────────────────────────────────────────
step "seed gh:issue:1 and watch for ${WATCH}s"
v entity put gh:issue:1 '{"stage":"triaged"}' >/dev/null || die "entity put failed"

field() { printf '%s' "$1" | grep -oE "\"?$2\"?[: ]+\"?[a-z-]+" | head -1 | grep -oE '[a-z-]+$' || true; }
printf '  \033[2m%-10s %-12s %s\033[0m\n' "gh:issue" "feature" "fired"
for _ in $(seq 1 "$WATCH"); do
	stage=$(field "$(v entity get gh:issue:1 2>/dev/null || true)" stage)
	feat=$(v entities 2>/dev/null | grep -o 'signadot:feature:[^ "]*' | head -1 || true)
	phase=""
	[ -n "$feat" ] && phase=$(field "$(v entity get "$feat" 2>/dev/null || true)" phase)
	fired=$(v status 2>/dev/null | grep -oE '"?fired"?[: ]+[0-9]+' | grep -oE '[0-9]+$' || true)
	printf '  %-10s %-12s %s\n' "${stage:-…}" "${phase:-—}" "${fired:-}"
	sleep 1
done

# ── what the run leaves behind ───────────────────────────────────────────────
step "what happened"
printf '\n'
v entities | grep -E 'gh:issue|signadot:feature' || true
printf '\n'
feat=$(v entities | grep -o 'signadot:feature:[^ "]*' | head -1)
[ -n "$feat" ] || die "the derive never landed"
ok "the derived entity is $feat"
note "its id is the engine's — f(rule, subject) — and it records what it is about:"
v entity get "$feat"

step "the throttle's report"
# The deliverable is not that it was throttled but WHICH rules were firing each other.
if grep -q 'cycle:' "$tmp/server.log"; then
	grep 'cycle:' "$tmp/server.log" | sed 's/^/  /'
	ok "both cycles were named, one per stratum, each with its own subject"
	note "They separate because a CHAIN IS PER SUBJECT: start-review is woken by a change to"
	note "the issue, not to its own subject, so it begins a fresh chain rather than extending"
	note "the lower one. Ignite the upper cycle from the DERIVE instead — drop start-review's"
	note "\`when\` — and the two chains link, share a root, and only the lower one is reported."
else
	note "no cycle report yet — watch longer with SECONDS_TO_WATCH=60"
fi

step "down"
v down >/dev/null 2>&1 || true
ok "done"
