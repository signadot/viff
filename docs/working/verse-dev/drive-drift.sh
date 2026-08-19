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

TASK='comment-drift@verse-dev.drift.head.head-moved@git:head:verse'
head_field() { v data verse-dev.drift.head 2>/dev/null | grep -E "^  $1:" | awk '{print $2}'; }
move_head()  { v data git:head:verse --put "{repo: verse, sha: $1}" >/dev/null; }
# wait_for <field> <value> <seconds>
wait_for() {
	local end=$((SECONDS+$3))
	while [ $SECONDS -lt $end ]; do [ "$(head_field "$1")" = "$2" ] && return 0; sleep 0.3; done
	return 1
}
task_open() { v task 2>/dev/null | grep -q "id: $TASK"; }
wait_task() {
	local end=$((SECONDS+$1))
	while [ $SECONDS -lt $end ]; do task_open && return 0; sleep 0.3; done
	return 1
}
# task_sha — WHICH COMMIT the open task was asked about, read off its prompt.
#
# The worker has to answer about the commit it was given rather than one the script names,
# or the harness proves nothing: an answer carrying a sha the task never saw is exactly the
# confusion `reviewedSha` exists to prevent, and a test that fakes it would pass while the
# flow lost commits.
task_sha() { v task 2>/dev/null | grep -oE 'Review [0-9a-f]+' | head -1 | awk '{print $2}'; }

# review [symbol] — answer the open task as its worker would, about its own commit
review() {
	local sha sym=${1:-pkg.Thing}
	sha=$(task_sha)
	[ -n "$sha" ] || return 1
	# THE COMPLETION HAS TO SUCCEED to count. The task id is (rule, subject) and the subject is
	# one entity, so every head shares ONE id — and a terminal task answers `410 Gone` to the
	# next claim. A review() that ignored that reported shas nobody reviewed.
	v task do complete "$TASK" \
		"{at: $sha, findings: [{symbol: $sym, status: open, says: \"a comment disagrees with its code\", at: $sha}]}" \
		--as human:drive >/dev/null 2>&1 || return 1
	echo "$sha"
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
wait_for reviewed gathered 20 || bad "the head did not reach `gathered`"
wait_task 20 || bad "no review task was spawned"
ok "head-moved → gather → a review task, in $SECONDS s"
asked=$(task_sha)
[ "$asked" = aaaa1111 ] && ok "the task was asked about aaaa1111" || bad "the task is about ${asked:-nothing}"
review >/dev/null
wait_for reviewedSha aaaa1111 20 && ok "the review landed and the head says which commit" \
	|| bad "reviewedSha did not become aaaa1111"
[ "$(head_field reviewed)" = done ] && ok "reviewed: done" || bad "reviewed = $(head_field reviewed)"
wait_finding pkg.Thing 15 && ok "the finding is in the ledger" || bad "no finding was derived"

step "2. a commit that lands WHILE a review is running"
move_head bbbb2222
wait_task 20 || bad "no review task for bbbb2222"
[ "$(task_sha)" = bbbb2222 ] || bad "the task is about $(task_sha), not bbbb2222"
move_head cccc3333          # a second commit arrives before that review answers
wait_for sha cccc3333 20 || bad "the head fact did not follow the source"
review >/dev/null           # …and the older review lands afterwards, about ITS own commit
sleep 3
if [ "$(head_field reviewedSha)" = cccc3333 ]; then
	ok "the newer commit was reviewed"
else
	known "cccc3333 is the head and reviewedSha=$(head_field reviewedSha), reviewed=$(head_field reviewed) — the late answer wrote over the pending state, so this commit is never reviewed"
fi

step "3. two commits landing together, as one push does"
# Unstick whatever scenario 2 parked, so this measures its own thing: what the flow does with
# a push, not what it did with the last one.
v data 'verse-dev.drift.head."head-moved@git:head:verse".reviewed' --put pending >/dev/null
reviewed=""
move_head dddd4444
move_head eeee5555          # the tip moves again immediately — one `git push`, two commits
for _ in $(seq 1 4); do
	wait_task 15 || break
	sha=$(review) || break          # a terminal task cannot be claimed again, and says so
	reviewed="$reviewed $sha"
	sleep 2
done
# BOTH, or the push lost one. Which one it loses depends on the race — the first commit's
# review can land over the second's pending state, or the second can overwrite the first's
# before a task exists — and either way a commit that nobody reviewed is a fix nobody sees.
case "$reviewed" in
	*dddd4444*eeee5555* | *eeee5555*dddd4444*) ok "both commits of the push were reviewed ($reviewed)" ;;
	*) known "one commit of the push was reviewed ($reviewed), the other never was: the head is a single slot and the review a single task id, so a push of two leaves one commit unreviewed and any finding fixed in it open forever" ;;
esac
echo "     head now: sha=$(head_field sha) reviewed=$(head_field reviewed) reviewedSha=$(head_field reviewedSha)"

step "result"
if [ "$fails" -gt 0 ]; then
	printf '%s%s ASSERTION(S) FAILED%s\n' "$red" "$fails" "$rst"; exit 1
fi
printf '%sthe harness works%s — the ~ lines above are the defects it exists to hold still\n' "$grn" "$rst"
