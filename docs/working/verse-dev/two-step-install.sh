#!/usr/bin/env bash
# Does the two-step install actually keep the storm from arming?
#
#   1  install the RECORD rule alone         → one record per reflected commit, no reviews
#   2  baseline every record that exists     → the sync.Once, as a set rather than a point
#   3  install the review rules              → nothing arms, because nothing is pending
#   4  land one more commit                  → exactly one task
#
# Step 4 is the assertion. Steps 1-3 are cheap if the width measurement is right.
set -uo pipefail
BIN=${BIN:-./dist/darwin/arm64/verse}
O_BIN=${O_BIN:-./dist/darwin/arm64/o}
N=${N:-131}
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

# Two halves of one file: the record rule alone, and the review rules WITHOUT it. Step 3 must
# not re-apply rule 1 — a derive is total, so re-installing it rewrites every record it owns
# and puts the baseline back to `pending` (issue 2c6z0ahwh12kraxbgnn0).
python3 - "$SRC" "$tmp/step1.tony" "$tmp/step3.tony" <<'PY'
import sys
src, one, three = sys.argv[1], sys.argv[2], sys.argv[3]
parts = open(src).read().split('\n---\n')
# parts[0] header (at:/reads:), parts[1] strata, parts[2] rule 1, parts[3:] the rest.
open(one, 'w').write('\n---\n'.join(parts[:3]) + '\n')
open(three, 'w').write('\n---\n'.join(parts[:2] + parts[3:]) + '\n')
PY

VERSE_CONFIG=/dev/null "$BIN" --addr "$ADDR" up --data "$tmp/data" --broker \
	--broker-addr "127.0.0.1:$BROKER_PORT" --o-bin "$O_BIN" >"$tmp/verse.log" 2>&1 &
for _ in $(seq 1 120); do v status >/dev/null 2>&1 && break; sleep 0.5; done
v status >/dev/null 2>&1 || { tail -5 "$tmp/verse.log"; echo "verse did not come up"; exit 1; }

land()    { v data "git:commit:verse/$1" --put "{repo: verse, sha: $1, msg: \"commit $1\"}" >/dev/null 2>&1; }
records() { v data verse-dev.drift.commit --depth 4 --paths 2>/dev/null | wc -l | tr -d ' '; }
tasks()   { v task 2>/dev/null | grep -c "id: comment-drift"; }
states()  { v data verse-dev.drift.commit 2>/dev/null | grep -cE "^  reviewed: $1"; }

echo "== 1. the record rule alone, then $N commits"
v c -a -f "$tmp/step1.tony" >/dev/null || { echo "step1 charter did not install"; exit 1; }
for i in $(seq 1 "$N"); do land "$(printf 'a%08d' "$i")"; done
t0=$SECONDS
for _ in $(seq 1 60); do [ "$(records)" -ge "$N" ] && break; sleep 2; done
echo "   $(records)/$N records after $((SECONDS-t0))s, $(tasks) tasks"
[ "$(tasks)" = 0 ] && ok "no reviews armed — the review rules are not installed" \
	|| bad "$(tasks) tasks with only the record rule installed"

echo "== 2. baseline every record that exists"
t0=$SECONDS
# One command if a place-level --put narrowed by --if applies to each entity under it; a loop
# over --paths otherwise. Which one it is, is the thing to learn here.
if v data verse-dev.drift.commit --put '{reviewed: baseline}' --if '{reviewed: pending}' >/dev/null 2>&1 \
	&& [ "$(states baseline)" -ge "$N" ]; then
	ok "one command baselined $(states baseline) records in $((SECONDS-t0))s"
else
	for p in $(v data verse-dev.drift.commit --depth 4 --paths 2>/dev/null); do
		v data "$p" --put '{reviewed: baseline}' --update >/dev/null 2>&1
	done
	[ "$(states baseline)" -ge "$N" ] \
		&& ok "a loop baselined $(states baseline) records in $((SECONDS-t0))s (no bulk form)" \
		|| bad "baselined $(states baseline)/$N — pending: $(states pending)"
fi

echo "== 3. install the review rules, WITHOUT re-applying the record rule"
v c -a -f "$tmp/step3.tony" >/dev/null || { echo "   review charter did not install"; exit 1; }
# WATCHED, not sampled once. The first run of this checked at t+15s, saw every record still
# baselined, and called it held — and the sweep that put them back to `pending` had simply not
# reached them yet. A latch that survives for fifteen seconds is not a latch.
for i in $(seq 1 12); do
	printf '   t+%-3ds baseline=%-4s pending=%-4s gathered=%-4s tasks=%s\n' \
		$((i*5)) "$(states baseline)" "$(states pending)" "$(states gathered)" "$(tasks)"
	sleep 5
done
if [ "$(tasks)" = 0 ] && [ "$(states baseline)" -ge "$N" ]; then
	ok "nothing armed after a minute: all $(states baseline) baselined records stayed put"
else
	bad "$(tasks) tasks; baseline=$(states baseline) pending=$(states pending) gathered=$(states gathered)"
fi

echo "== 4. one more commit"
land zzzz9999
t0=$SECONDS
for _ in $(seq 1 30); do [ "$(tasks)" -ge 1 ] && break; sleep 1; done
[ "$(tasks)" = 1 ] \
	&& ok "exactly one task, $((SECONDS-t0))s after it landed" \
	|| bad "$(tasks) tasks after one commit"

echo; [ "$fails" = 0 ] && echo "the two-step install holds" || echo "$fails FAILED"
