#!/usr/bin/env bash
# Drive the comment-drift state machine locally, in seconds, without a model.
#
# WHY THIS EXISTS. The two defects it reproduces are about ORDER — a review landing after the
# head moved, and a push whose middle commits are never reviewed — and neither is reachable
# against staging in less than ten minutes: a real review is an llm task over a real checkout,
# behind a real git source polling a real remote. Iterating on the rules that way does not
# converge. Here the head is a `verse data git:head:verse --put` and the review is a brokered
# task this script completes with a fixture, so the whole cycle is a few seconds and the timing
# is the script's rather than the model's.
#
#   ./docs/working/verse-dev/drive-drift.sh            run every scenario
#   ./docs/working/verse-dev/drive-drift.sh --keep     …and leave the verse up to poke at
#
# The charter is drift-local.tony beside this, which is the real one's first half with the
# review spawn brokered. When they diverge, that file is the one that is wrong.
set -uo pipefail

BIN=${BIN:-./dist/darwin/arm64/verse}
O_BIN=${O_BIN:-./dist/darwin/arm64/o}
HERE=$(cd "$(dirname "$0")" && pwd)
KEEP=
[ "${1:-}" = "--keep" ] && KEEP=1

bold=$(tput bold 2>/dev/null || true); red=$(tput setaf 1 2>/dev/null || true)
grn=$(tput setaf 2 2>/dev/null || true); ylw=$(tput setaf 3 2>/dev/null || true)
rst=$(tput sgr0 2>/dev/null || true)
fails=0
step() { printf '\n%s# %s%s\n' "$bold" "$1" "$rst"; }
ok()   { printf '  %s✓%s %s\n' "$grn" "$rst" "$1"; }
bad()  { printf '  %s✗ %s%s\n' "$red" "$1" "$rst"; fails=$((fails+1)); }
known(){ printf '  %s~ KNOWN DEFECT: %s%s\n' "$ylw" "$1" "$rst"; }

free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }

tmp=$(mktemp -d)
API_PORT=$(free_port)
ADDR=127.0.0.1:$API_PORT
# NOT ADDR+1: the admin face derives to it. verse yields the derived face to a broker port that
# was asked for outright, but the ports the kernel hands back here are often CONSECUTIVE, and a
# harness that leans on that fix every run is testing the fix rather than the flow.
BROKER_PORT=$(free_port)
while [ "$BROKER_PORT" = "$((API_PORT+1))" ]; do BROKER_PORT=$(free_port); done
BROKER=127.0.0.1:$BROKER_PORT
v() { "$BIN" --addr "$ADDR" "$@"; }
cleanup() {
	[ -n "$KEEP" ] && { echo; echo "verse still up on $ADDR (data $tmp)"; return; }
	"$BIN" --addr "$ADDR" down >/dev/null 2>&1
	sleep 1; rm -rf "$tmp"
}
trap cleanup EXIT

step "up on $ADDR (broker $BROKER)"
VERSE_CONFIG=/dev/null "$BIN" --addr "$ADDR" up --data "$tmp/data" --broker --broker-addr "$BROKER" \
	--o-bin "$O_BIN" >"$tmp/verse.log" 2>&1 &
for _ in $(seq 1 120); do v status >/dev/null 2>&1 && break; sleep 0.5; done
v status >/dev/null 2>&1 || { tail -20 "$tmp/verse.log"; echo "verse did not come up"; exit 1; }
# The failure line says "broker face on <addr>: bind: ..." too, so match what SUCCESS looks
# like: the face is up only if nothing followed the address with an error.
grep -qE "broker face|dial-in broker" "$tmp/verse.log" && ! grep -q "broker face on .*: listen" "$tmp/verse.log" \
	|| { grep -i broker "$tmp/verse.log" >&2; echo "the broker face is not served"; exit 1; }
ok "up, with a dial-in broker"

v c -a -f "$HERE/drift-local.tony" >/dev/null || { echo "charter did not install"; exit 1; }
ok "the drift charter is installed"

# ONE RECORD AND ONE TASK PER COMMIT, which is what `key: $subject.value.sha` buys: the
# derive's address is (rule, subject, key), so every sha keeps its own state and its own task
# id instead of sharing one slot with every other commit.
KEY='head-moved@git:head:verse'
record()  { v data "verse-dev.drift.head.\"$KEY@$1\"" 2>/dev/null; }
field()   { record "$1" | grep -E "^$2:" | awk '{print $2}'; }
task_for(){ echo "comment-drift@verse-dev.drift.head.$KEY@$1"; }
move_head(){ v data git:head:verse --put "{repo: verse, sha: $1}" >/dev/null; }

# wait_field <sha> <field> <value> <seconds>
wait_field() {
	local end=$((SECONDS+$4))
	while [ $SECONDS -lt $end ]; do [ "$(field "$1" "$2")" = "$3" ] && return 0; sleep 0.3; done
	return 1
}
# wait_task <sha> <seconds> — a task of that commit's own
wait_task() {
	local end=$((SECONDS+$2))
	while [ $SECONDS -lt $end ]; do
		v task 2>/dev/null | grep -q "id: $(task_for "$1")$" && return 0
		sleep 0.3
	done
	return 1
}
# review <sha> [symbol] — answer THAT commit's task, about that commit
review() {
	local sha=$1 sym=${2:-pkg.Thing}
	v task do complete "$(task_for "$sha")" \
		"{at: $sha, findings: [{symbol: $sym, status: open, says: \"a comment disagrees with its code\", at: $sha}]}" \
		--as human:drive >/dev/null 2>&1
}
# wait_finding <symbol> <seconds>
wait_finding() {
	local end=$((SECONDS+$2))
	while [ $SECONDS -lt $end ]; do
		v data verse-dev.drift.finding 2>/dev/null | grep -q "symbol: $1" && return 0
		sleep 0.3
	done
	return 1
}

step "1. one commit, reviewed once"
move_head aaaa1111
wait_task aaaa1111 20 && ok "a task of aaaa1111's own, in $SECONDS s" || bad "no task for aaaa1111"
review aaaa1111 || bad "could not complete aaaa1111's review"
wait_field aaaa1111 reviewedSha aaaa1111 20 && ok "aaaa1111 is reviewed, and says so" \
	|| bad "aaaa1111 reviewedSha=$(field aaaa1111 reviewedSha)"
wait_finding pkg.Thing 15 && ok "the finding is in the ledger" || bad "no finding was derived"

step "2. a commit that lands WHILE a review is running"
move_head bbbb2222
wait_task bbbb2222 20 || bad "no task for bbbb2222"
move_head cccc3333                       # arrives before bbbb2222's review answers
wait_task cccc3333 20 && ok "cccc3333 got a task of its own while bbbb2222 was still open" \
	|| bad "cccc3333 never got a task"
review bbbb2222 || bad "could not complete bbbb2222's review"
wait_field bbbb2222 reviewedSha bbbb2222 20 || bad "bbbb2222 did not record its own review"
sleep 2
if [ "$(field cccc3333 reviewed)" = done ]; then
	bad "cccc3333 was marked done by bbbb2222's answer"
else
	ok "the late answer landed on bbbb2222 alone; cccc3333 is still $(field cccc3333 reviewed)"
fi
review cccc3333 || bad "could not complete cccc3333's review"
wait_field cccc3333 reviewedSha cccc3333 20 && ok "cccc3333 is reviewed too" \
	|| bad "cccc3333 reviewedSha=$(field cccc3333 reviewedSha)"

step "3. two commits landing together, as one push does"
move_head dddd4444
move_head eeee5555                       # the tip moves again immediately — one push, two commits
both=1
for sha in dddd4444 eeee5555; do
	wait_task "$sha" 20 || { bad "no task for $sha"; both=; continue; }
	review "$sha" || { bad "could not complete $sha's review"; both=; continue; }
	wait_field "$sha" reviewedSha "$sha" 20 || { bad "$sha did not record its own review"; both=; }
done
[ -n "$both" ] && ok "both commits of the push were reviewed, each about itself" \
	|| known "a commit of the push was lost"
v data verse-dev.drift.head --paths 2>/dev/null | sed 's/^/     /'

step "result"
if [ "$fails" -gt 0 ]; then
	printf '%s%s ASSERTION(S) FAILED%s\n' "$red" "$fails" "$rst"; exit 1
fi
printf '%sthe harness works%s — the ~ lines above are the defects it exists to hold still\n' "$grn" "$rst"
