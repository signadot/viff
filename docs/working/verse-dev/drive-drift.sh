#!/usr/bin/env bash
# Drive the comment-drift state machine locally, in seconds, without a model.
#
# WHY THIS EXISTS. What it holds still is about ORDER — a commit landing while a review runs, a
# review that fails, a finding carried from one review to the next — and none of it is reachable
# against staging in less than ten minutes: a real review is an llm task over a real checkout,
# behind a real git source polling a real remote. Iterating on the rules that way does not
# converge. Here a push is a `verse data git head verse --put` and the review is a brokered task
# this script completes with a fixture, so the whole cycle is a few seconds and the timing is the
# script's rather than the model's.
#
#   ./docs/working/verse-dev/drive-drift.sh            run every scenario
#   ./docs/working/verse-dev/drive-drift.sh --keep     …and leave the verse up to poke at
#
# The charter is drift-local.tony beside this, which is the real one's review half with the
# review brokered. When they diverge, that file is the one that is wrong.
#
# THE WAITERS READ A HERE-STRING, not a pipe. `cmd | grep -q X` under `set -o pipefail` reports
# 141 on a MATCH — grep -q exits at the first hit, the writer dies on the closed pipe, and
# pipefail hands that up as the pipeline's status — so a hit read as a miss.
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
# NOT ADDR+1: the admin face derives to it, and the ports the kernel hands back here are often
# CONSECUTIVE — a harness that leans on that fix every run is testing the fix rather than the flow.
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
grep -qE "broker face|dial-in broker" "$tmp/verse.log" && ! grep -q "broker face on .*: listen" "$tmp/verse.log" \
	|| { grep -i broker "$tmp/verse.log" >&2; echo "the broker face is not served"; exit 1; }
ok "up, with a dial-in broker"

v c -a -f "$HERE/drift-local.tony" >/dev/null || { echo "charter did not install"; exit 1; }
ok "the drift charter is installed"

# ── the verse as the flow sees it ────────────────────────────────────────────
# A PUSH IS ONE MOVE OF THE TIP, whatever it carries: that is what makes a range the unit. The
# commits themselves need not be reflected at all — the review reads them out of a checkout, and
# nothing in the charter selects on `git:commit`.
# THE FIXTURE SHAS CARRY LETTERS on purpose: an all-digit id is quoted in a kinded path
# (`range."77777777"`), because the quoting is about tony TYPES rather than about the path — so a
# script composing a task id by hand would build one that matches nothing. Real shas are hex and
# almost always carry a letter; the harness should not be the one place that depends on it.
push()    { v data git head verse --put "{repo: verse, sha: $1}" >/dev/null; }
cursor()  { v data verse-dev.drift.at 2>/dev/null; }
at()      { sed -n 's/^sha: //p' <<<"$(cursor)"; }
stage()   { sed -n 's/^stage: //p' <<<"$(cursor)"; }
covering(){ sed -n 's/^covering: //p' <<<"$(cursor)"; }
range()   { v data "verse-dev.drift.range.$1" 2>/dev/null; }
task_for(){ echo "review-range@verse-dev.drift.range.$1"; }
ranges()  { v data verse-dev.drift.range --paths --depth 4 2>/dev/null | sed 's/.*\.//' | sort | tr '\n' ' '; }  # --depth bounds the walk to the records themselves

# review <to> [symbol] — answer THAT range's task, carrying one finding
review() {
	local to=$1 sym=${2:-pkg.Thing}
	v task do complete "$(task_for "$to")" \
		"{files: 1, findings: [{symbol: $sym, status: open, says: \"a comment disagrees with its code\", at: $to, proposedAt: $to}]}" \
		--as human:drive >/dev/null 2>&1
}
# review_carrying <to> — answer with the standing set repeated verbatim and nothing new
review_verbatim() {
	local to=$1 sym=$2
	v task do complete "$(task_for "$to")" \
		"{files: 0, findings: [{symbol: $sym, status: open, says: \"a comment disagrees with its code\", at: $to, proposedAt: $to}]}" \
		--as human:drive >/dev/null 2>&1
}
# resolve <to> <symbol> — the fix landed, so the carried finding is right in this tree now
resolve() {
	local to=$1 sym=$2
	v task do complete "$(task_for "$to")" \
		"{files: 1, findings: [{symbol: $sym, status: resolved, says: \"the comment agrees with the code now\", at: $to, proposedAt: $to}]}" \
		--as human:drive >/dev/null 2>&1
}
waitfor() { local end=$((SECONDS+$2)); while [ $SECONDS -lt $end ]; do eval "$1" && return 0; sleep 0.3; done; return 1; }
has()     { case "$1" in *"$2"*) return 0;; esac; return 1; }

# ── the seed: ONE write, which is what the real one takes too ────────────────
step "seeding: a cursor standing on the tip"
push aaaa1111
v data verse-dev.drift.at --put '{sha: aaaa1111, stage: idle}' >/dev/null
sleep 2
[ "$(stage)" = idle ] && [ -z "$(covering)" ] \
	&& ok "nothing fires: the cursor is standing on the tip" \
	|| bad "after seeding the cursor is $(at)/$(stage) covering=$(covering)"

step "1. a push is reviewed once, as a range"
push bbbb2222
waitfor '[ -n "$(covering)" ]' 20 && ok "claimed $(covering), from $(at)" || bad "nothing was claimed"
waitfor 'has "$(v task 2>/dev/null)" "id: $(task_for bbbb2222)"' 20 \
	&& ok "a task of that range's own: $(task_for bbbb2222)" || bad "no task for the range"
review bbbb2222
waitfor '[ "$(at)" = bbbb2222 ] && [ "$(stage)" = idle ]' 20 \
	&& ok "the review landed and the cursor stepped to bbbb2222" || bad "cursor is $(at)/$(stage)"
waitfor 'has "$(v data verse-dev.drift.finding 2>/dev/null)" "symbol: pkg.Thing"' 15 \
	&& ok "the finding is in the ledger" || bad "no finding was derived"

step "2. a push of TWO commits — one range, and the middle commit is in it"
# NOT two pushes: a push of two moves the tip ONCE. Under the older per-commit charter this was
# the defect the harness existed to hold — verse saw only the tip and the middle commit was never
# a subject. A range is `bbbb2222..dddd4444`, so both are inside it by construction.
push dddd4444
waitfor 'has "$(v task 2>/dev/null)" "id: $(task_for dddd4444)"' 20 || bad "no task for the push"
r=$(range dddd4444)
if has "$r" "from: bbbb2222" && has "$r" "to: dddd4444"; then
	ok "one review, covering bbbb2222..dddd4444 — cccc3333 is inside it, not skipped"
else
	bad "the range is not bbbb2222..dddd4444: $(tr '\n' ' ' <<<"$r")"
fi
review_verbatim dddd4444 pkg.Thing
waitfor '[ "$(at)" = dddd4444 ]' 20 && ok "…and the cursor stepped over the whole push at once" \
	|| bad "cursor is $(at)/$(stage)"

step "3. a commit that lands WHILE a review is running"
push eeee5555
waitfor '[ "$(covering)" = eeee5555 ]' 20 || bad "eeee5555 was not claimed"
push ffff6666                                   # arrives before eeee5555's review answers
sleep 2
[ "$(covering)" = eeee5555 ] && ok "the claim is unmoved: still covering eeee5555" \
	|| bad "the claim followed the head to $(covering)"
review_verbatim eeee5555 pkg.Thing
waitfor '[ "$(at)" = eeee5555 ]' 20 \
	&& ok "the cursor advanced to what was CLAIMED, not to the head that moved under it" \
	|| bad "cursor is $(at)"
waitfor 'has "$(range ffff6666)" "from: eeee5555"' 20 \
	&& ok "…and the next range picks up exactly there: eeee5555..ffff6666, nothing skipped" \
	|| bad "the next range is $(tr '\n' ' ' <<<"$(range ffff6666)")"

step "4. a finding is handed on until somebody says it is fixed — and then it closes"
# THE LEDGER IS ADDRESSED BY SYMBOL (`id:` inside `!each`), so a verdict OVERWRITES the claim it
# is about. That is what lets `gather` read `status: open` and get what is still standing rather
# than everything ever filed — and it is why a reviewer going quiet leaves a finding alone
# instead of deleting it.
waitfor 'has "$(range ffff6666)" "symbol: pkg.Thing"' 20 \
	&& ok "ffff6666 was handed the finding the last review left open" \
	|| bad "outstanding on ffff6666: $(tr '\n' ' ' <<<"$(range ffff6666)")"
led=$(v data verse-dev.drift.finding --paths --depth 4 2>/dev/null | sed 's/.*finding\.//' | tr -d '"' | sort | tr '\n' ' ')
[ "$led" = "pkg.Thing " ] && ok "one entity per symbol in the ledger, not one per review: $led" \
	|| bad "the ledger holds: $led"
resolve ffff6666 pkg.Thing
# THE ADDRESS IS QUOTED because the id is: a symbol carries dots (`entity.(*LogdStore).Get`),
# and a kinded path quotes a segment whose content would otherwise split it.
row(){ v data 'verse-dev.drift.finding."pkg.Thing"' 2>/dev/null; }
waitfor 'has "$(row)" "status: resolved"' 20 \
	&& ok "…and a verdict OVERWROTE the open row rather than landing beside it" \
	|| bad "the ledger row is $(tr '\n' ' ' <<<"$(row)")"
push 7777aaaa
waitfor 'has "$(v task 2>/dev/null)" "id: $(task_for 7777aaaa)"' 20 || bad "no task for 7777aaaa"
has "$(range 7777aaaa)" "outstanding: []" \
	&& ok "the next review is handed nothing: closed means closed" \
	|| bad "outstanding on 7777aaaa: $(sed -n '/^outstanding:/,/^[a-z]/p' <<<"$(range 7777aaaa)" | tr '\n' ' ')"

step "5. a review that FAILS stops the chain where it broke"
v task do fail "$(task_for 7777aaaa)" "the runner had nowhere to put it" --as human:drive >/dev/null 2>&1
waitfor 'has "$(range 7777aaaa)" "status: failed"' 20 && ok "the range is failed" || bad "the range did not fail"
sleep 2
[ "$(at)" != 7777aaaa ] && ok "the cursor did not step past it: still at $(at)" \
	|| bad "the cursor stepped over a failed review"
push 8888bbbb
sleep 3
[ "$(at)" != 8888bbbb ] && ok "…and a later push does not walk past it either" \
	|| bad "the chain ran past the failure to $(at)"

step "6. one write resumes it, and the range has GROWN"
v data verse-dev.drift.at --put '{stage: idle, covering: !delete null}' >/dev/null
waitfor 'has "$(range 8888bbbb)" "from: eeee5555" || has "$(range 8888bbbb)" "from: ffff6666"' 20 \
	&& ok "the retry covers everything since: $(sed -n 's/^from: //p' <<<"$(range 8888bbbb)")..8888bbbb" \
	|| bad "after the reset the ranges are: $(ranges)"

step "result"
if [ "$fails" -gt 0 ]; then
	printf '%s%s ASSERTION(S) FAILED%s\n' "$red" "$fails" "$rst"; exit 1
fi
printf '%sthe harness works%s\n' "$grn" "$rst"
