#!/usr/bin/env bash
# Does a rule that selects on the ABSENCE of a field survive a restart?
#
# comment-drift.tony carries a warning that cost a drive an evening: `!not {reviewed: null}`
# is the documented spelling of "not yet reviewed" and verse cannot store one — absence is
# written as a bare null and every bare null is hidden on read, so the durable rule comes back
# as `!not {}` and selects nothing. The charter works until the first restart and then silently
# stops.
#
# `gather` now selects on `!not.has-path stage`, a DIFFERENT spelling, and the same charter
# already leans on `!not.has-path` for `decided`, `state` and `says`. Probably safe is what that
# evening was made of, so: land a commit, restart, land another, and see whether the second one
# is still picked up.
set -uo pipefail
BIN=${BIN:-./dist/darwin/arm64/verse}
O_BIN=${O_BIN:-./dist/darwin/arm64/o}
HERE=$(cd "$(dirname "$0")" && pwd)
SRC=$HERE/drift-local.tony

free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }
tmp=$(mktemp -d)
API_PORT=$(free_port); ADDR=127.0.0.1:$API_PORT
BROKER_PORT=$(free_port)
while [ "$BROKER_PORT" = "$((API_PORT+1))" ]; do BROKER_PORT=$(free_port); done
v() { "$BIN" --addr "$ADDR" "$@"; }
cleanup() { "$BIN" --addr "$ADDR" down >/dev/null 2>&1; sleep 1; rm -rf "$tmp"; }
trap cleanup EXIT
fails=0
ok()  { printf '  ✓ %s\n' "$1"; }
bad() { printf '  ✗ %s\n' "$1"; fails=$((fails+1)); }

boot() {
	VERSE_CONFIG=/dev/null "$BIN" --addr "$ADDR" up --data "$tmp/data" --broker \
		--broker-addr "127.0.0.1:$BROKER_PORT" --o-bin "$O_BIN" >>"$tmp/verse.log" 2>&1 &
	for _ in $(seq 1 120); do v status >/dev/null 2>&1 && break; sleep 0.5; done
	v status >/dev/null 2>&1 || { tail -5 "$tmp/verse.log"; echo "verse did not come up"; exit 1; }
}
# A PUSH, not a commit: the review's unit is the range a push leaves behind, so what the flow
# reacts to is the tip moving. The cursor is seeded once, standing on the first tip.
land()  { v data git head verse --put "{repo: verse, sha: $1}" >/dev/null; }
seed()  { v data verse-dev.drift.at --put "{sha: $1, stage: idle}" >/dev/null; }
state() { v data "verse-dev.drift.range.$1" 2>/dev/null | sed -n 's/^stage: //p'; }
gathered() {  # <sha> <seconds>
	local end=$((SECONDS+$2))
	while [ $SECONDS -lt $end ]; do [ "$(state "$1")" = gathered ] && return 0; sleep 1; done
	return 1
}

boot
v c -a -f "$SRC" >/dev/null || { echo "charter did not install"; exit 1; }
land aaaa1111; seed aaaa1111
land bbbb2222
gathered bbbb2222 20 && ok "before the restart, the range to bbbb2222 was picked up" \
	|| bad "bbbb2222 never gathered even before a restart: $(state bbbb2222)"

echo "== restart"
v down >/dev/null 2>&1; sleep 2
boot
# RETRIED, because `up` answers before the charter is restored: the first read after a boot
# came back empty and the retry showed the rule intact, which is a flaky probe reporting a
# durability failure — the exact confusion this script exists to rule out.
rule=
for _ in $(seq 1 20); do
	rule=$(v c verse-dev.drift rules gather 2>/dev/null)
	[ -n "$rule" ] && break
	sleep 1
done
case "$rule" in
	*'!not.has-path stage'*) ok "the stored rule still spells the absence as !not.has-path" ;;
	'') bad "the charter never came back after the restart" ;;
	*)  bad "the stored rule lost its absence test: $(echo "$rule" | tr -d '\n')" ;;
esac

# ANSWER THE OUTSTANDING REVIEW FIRST. The review's unit is a range and there is one in flight
# at a time, so the cursor is still standing on the range it claimed before the restart: a new
# push waits for it, which is the flow working rather than the latch failing. What is under test
# is whether the NEXT range still gathers — that is the stored absence doing its job.
v task do complete "review-range@verse-dev.drift.range.bbbb2222" '{files: 0, findings: []}' \
	--as human:restart >/dev/null 2>&1
land cccc3333
gathered cccc3333 25 && ok "after the restart, the range to cccc3333 was picked up too" \
	|| bad "cccc3333 never gathered after the restart: $(state cccc3333) — the absence did not survive"

echo; [ "$fails" = 0 ] && echo "the absence survives a restart" || echo "$fails FAILED"
