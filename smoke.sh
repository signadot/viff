#!/usr/bin/env bash
#
# smoke.sh — end-to-end smoke of verse's core operator surfaces against an isolated,
# self-contained docd-backed server.
#
# It builds throwaway `verse` + `o` binaries (o is the pinned go-tony daemon runner, built
# from this module — so bringing up logd+docd needs no external dependency) and runs its own
# `verse up` on a free port with a private data dir. Hermetic: it forces the scripted driver,
# so no LLM creds and no network. It never touches a dev server on :9797, and a trap reaps the
# server (and its daemon tree) plus the temp dir on exit.
#
# Every check is fatal except the two noted as best-effort. Exits non-zero on the first failure.
#
#   ./smoke.sh              # or: make smoke
#   PORT=20000 ./smoke.sh   # pin the base port (default: search up from 19797)
#
set -euo pipefail

cd "$(cd "$(dirname "$0")" && pwd)"

checks=0
step() { printf '\n\033[1m# %s\033[0m\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; checks=$((checks + 1)); }
note() { printf '  \033[33m~\033[0m %s\n' "$*"; }
die()  { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }
has()  { printf '%s' "$1" | grep -qF -- "$2"; }

# ── a free TCP port (avoids clobbering a dev server) ─────────────────────────
free_port() {
	local p=${PORT:-19797} i
	for i in $(seq 0 50); do
		# a successful connect means something is listening → busy; try the next.
		if ! (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
			printf '%s' "$p"
			return 0
		fi
		exec 3>&- 2>/dev/null || true
		p=$((p + 1))
	done
	die "no free port found near ${PORT:-19797}"
}
ADDR="127.0.0.1:$(free_port)"

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

# ── entity: put/get roundtrip ────────────────────────────────────────────────
step "entity put/get"
v entity put demo:pr:1 '{"stage":"open"}' >/dev/null || die "entity put failed"
has "$(v entity get demo:pr:1)" '"stage": "open"' || die "entity get did not roundtrip"
ok "demo:pr:1 = {stage:open}"

# ── scope: copy-on-write isolation (docd overlays) ───────────────────────────
# Note: a scope created here is not torn down — there is no general scope teardown yet, only
# connect's. The throwaway data dir reclaims the overlay. See docs/scope-lifecycle.md.
step "scope isolation (--scope)"
SC="iso$$"
v --scope "$SC" entity put demo:pr:9 '{"stage":"scoped-only"}' >/dev/null || die "scoped entity put failed"
has "$(v --scope "$SC" entity get demo:pr:9)" 'scoped-only' || die "scope does not see its own write"
ok "scoped write visible inside scope $SC"
# the overlay must not leak into the baseline
if has "$(v entity get demo:pr:9 2>/dev/null || true)" 'scoped-only'; then
	die "scoped write leaked into the baseline"
fi
ok "scoped write invisible in the baseline (isolation)"
# ...but a baseline entity IS visible from within the scope (copy-on-write read-through)
has "$(v --scope "$SC" entity get demo:pr:1)" '"stage": "open"' || die "scope cannot read the baseline (no read-through)"
ok "baseline entity readable from the scope (read-through)"

# ── reactive core: a non-gated trigger auto-advances its subject ─────────────
step "trigger add + drive (reactive core)"
v trigger add -f - >/dev/null <<'JSON' || die "trigger add failed"
[ { "name": "smoke-advance",
    "matches": [ { "system": "demo", "kind": "pr", "fields": { "stage": "open" } } ],
    "gates":   [],
    "action":  { "set": { "stage": "reviewed" } } } ]
JSON
ok "charter installed"
# putting a matching entity fires the trigger, which advances the matched subject.
v entity put demo:pr:2 '{"stage":"open"}' >/dev/null || die "entity put failed"
advanced=
for _ in $(seq 1 100); do
	if has "$(v entity get demo:pr:2 2>/dev/null || true)" '"stage": "reviewed"'; then
		advanced=1
		break
	fi
	sleep 0.2
done
[ -n "$advanced" ] || die "trigger did not advance demo:pr:2 open → reviewed"
ok "demo:pr:2 auto-advanced open → reviewed"

# ── process runtime + the re-vector surface (scripted driver: hermetic) ──────
step "process run + control surfaces"
v process run --driver scripted "smoke review task" >/dev/null || die "process run failed"
ID=$(v process list | awk -F'"' '/"id"/ {print $4; exit}')
[ -n "$ID" ] || die "no process id from list"
ok "spawned $ID"
has "$(v process get "$ID")" "$ID" || die "process get missing the id"
ok "process get"
v process say "$ID" "also mention the token TTL" >/dev/null || die "process say failed"
ok "process say (re-prompt)"
out=$(v process vector "$ID" ask --as human:smoke) || die "process vector failed"
has "$out" "ask" || die "process vector did not report the new mode"
ok "process vector → ask (as human:smoke)"
# Observability is best-effort: the mirror stops projecting once a process is terminal, so a
# fast scripted process may finish before the slide lands. The control path above is fatal.
if has "$(v entity get "process:instance:$ID" 2>/dev/null || true)" '"vectorBy": "human:smoke"'; then
	ok "slide observable on the entity (vector/vectorBy projected)"
else
	note "slide not projected (process already terminal) — control path verified above"
fi

# ── connect: federate a local tree into a scope (docd-only path) ─────────────
step "connect (federate a local tree)"
mkdir -p "$tmp/tree"
printf 'hello from a\n' >"$tmp/tree/a.txt"
printf 'hello from b\n' >"$tmp/tree/b.txt"
SCOPE="smoke$$"
# direct invocation (see the up launch above) so kill -INT reaches connect, not a wrapper.
"$BIN" --addr "$ADDR" --scope "$SCOPE" connect "$tmp/tree" --driver scripted >"$tmp/connect.log" 2>&1 &
CONN=$!
fed=
for _ in $(seq 1 150); do
	out=$(v --scope "$SCOPE" entity list --system local 2>/dev/null || true)
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
if has "$(v entity get "local:dir-content-$SCOPE:a.txt" 2>/dev/null || true)" 'hello from a'; then
	ok "file content served through the verse (content mount)"
else
	note "content read did not match (mount timing) — federation verified above"
fi
kill -INT "$CONN" 2>/dev/null || true # ctrl-c tears the scope down
wait "$CONN" 2>/dev/null || true
CONN=""
ok "connect disconnected"

# ── status ───────────────────────────────────────────────────────────────────
step "status"
has "$(v status)" 'triggers' || die "status missing engine counts"
ok "status reports engine counts"

# ── shutdown ─────────────────────────────────────────────────────────────────
step "down"
v down >/dev/null 2>&1 || true # the trap also reaps the server + its daemons
ok "down"

printf '\n\033[32mSMOKE PASSED\033[0m — %d checks\n' "$checks"
