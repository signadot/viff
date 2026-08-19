#!/usr/bin/env bash
# How wide can a rule over git:commit be? Measured in three phases, so a slowdown can be
# attributed rather than guessed at.
#
#   A  land N commits with NO charter installed      → the store's own write cost
#   B  install the charter, all N fire at once       → the install storm, which is what staging hit
#   C  land N more with the charter running          → the steady state
#
# The claim under test is comment-drift.tony's own note: "a rule over `git:commit` is as wide as
# the reflected history: 131 commits on staging, all 131 firings timing out inside the store,
# every derive lost."
set -uo pipefail
BIN=${BIN:-./dist/darwin/arm64/verse}
O_BIN=${O_BIN:-./dist/darwin/arm64/o}
N=${N:-131}
CHARTER=${CHARTER:-$(cd "$(dirname "$0")" && pwd)/drift-local.tony}

free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }
tmp=$(mktemp -d)
API_PORT=$(free_port); ADDR=127.0.0.1:$API_PORT
BROKER_PORT=$(free_port)
while [ "$BROKER_PORT" = "$((API_PORT+1))" ]; do BROKER_PORT=$(free_port); done
v() { "$BIN" --addr "$ADDR" "$@"; }
cleanup() { "$BIN" --addr "$ADDR" down >/dev/null 2>&1; sleep 1; rm -rf "$tmp"; }
trap cleanup EXIT

VERSE_CONFIG=/dev/null "$BIN" --addr "$ADDR" up --data "$tmp/data" --broker \
	--broker-addr "127.0.0.1:$BROKER_PORT" --o-bin "$O_BIN" >"$tmp/verse.log" 2>&1 &
for _ in $(seq 1 120); do v status >/dev/null 2>&1 && break; sleep 0.5; done
v status >/dev/null 2>&1 || { tail -5 "$tmp/verse.log"; echo "verse did not come up"; exit 1; }

land() { v data "git:commit:verse/$1" --put "{repo: verse, sha: $1, msg: \"commit $1\"}" >/dev/null 2>&1; }
# --depth 4: the record is verse-dev.drift.commit.<sha>, and --paths defaults to 3.
records() { v data verse-dev.drift.commit --depth 4 --paths 2>/dev/null | wc -l | tr -d ' '; }
tasks()   { v task 2>/dev/null | grep -c "id: comment-drift"; }
sample()  { printf '   %3ds  records=%-4s tasks=%s\n' "$1" "$(records)" "$(tasks)"; }

echo "== A: $N commits, no charter"
t0=$SECONDS
for i in $(seq 1 "$N"); do land "$(printf 'a%08d' "$i")"; done
a=$((SECONDS-t0))
echo "   landed $N in ${a}s ($((a * 1000 / N))ms each)"

echo "== B: install the charter; $N fire at once"
t0=$SECONDS
v c -a -f "$CHARTER" >/dev/null || { echo "   charter did not install"; exit 1; }
echo "   installed in $((SECONDS-t0))s"
t0=$SECONDS; got=0
for i in $(seq 1 90); do
	got=$(records)
	[ $((i % 5)) = 1 ] && sample $((SECONDS-t0))
	[ "$got" -ge "$N" ] && [ "$(tasks)" -ge "$N" ] && break
	sleep 2
done
echo "   settled: $got/$N records, $(tasks)/$N tasks, after $((SECONDS-t0))s"

echo "== C: $N more, with the charter running"
t0=$SECONDS
for i in $(seq 1 "$N"); do land "$(printf 'c%08d' "$i")"; done
c=$((SECONDS-t0))
echo "   landed $N in ${c}s ($((c * 1000 / N))ms each) — A was ${a}s"
t0=$SECONDS; got=0
for i in $(seq 1 90); do
	got=$(records)
	[ $((i % 5)) = 1 ] && sample $((SECONDS-t0))
	[ "$got" -ge "$((N*2))" ] && [ "$(tasks)" -ge "$((N*2))" ] && break
	sleep 2
done
echo "   settled: $got/$((N*2)) records, $(tasks)/$((N*2)) tasks, after $((SECONDS-t0))s"
grep -ciE "timeout|deadline exceeded|context canceled" "$tmp/verse.log" | sed 's/^/   log lines mentioning a timeout: /'
