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

# ONE RECORD AND ONE TASK PER COMMIT, which is what `id: $subject.value.sha` buys: the record
# is addressed by the sha alone, so every commit keeps its own state and its own task id instead
# of sharing one slot with every other commit — and the address is one anything can spell, which
# `key:` would not have been (it composes rule and subject in front of the sha).
record()  { v data "verse-dev.drift.head.$1" 2>/dev/null; }
field()   { record "$1" | grep -E "^$2:" | awk '{print $2}'; }
task_for(){ echo "comment-drift@verse-dev.drift.head.$1"; }
move_head(){ v data git:head:verse --put "{repo: verse, sha: $1}" >/dev/null; }
# reflect_commit <sha> [<parent>…] — the record the git source writes for a commit, `parents`
# included: a LIST of shas, empty for a root commit (source/git.go). The harness names its own
# chain, so it is the one that knows what landed on what.
reflect_commit() {
	local sha=$1; shift
	local parents="[]"
	[ $# -gt 0 ] && parents="[$(printf '%s, ' "$@" | sed 's/, $//')]"
	v data "git:commit:verse/$sha" --put \
		"{repo: verse, sha: $sha, msg: \"commit $sha\", parents: $parents}" >/dev/null
}
# land_commit <sha> — a push of one, in the order the source reflects it: the commit record
# first, then the tip onto it. $LAST is the chain, so a landing names its own parent and the
# first one lands as a root.
land_commit() {
	reflect_commit "$1" ${LAST:+"$LAST"}
	move_head "$1"
	LAST=$1
}
LAST=

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
		"{at: $sha, report: \"reviewed $sha: one comment disagrees with its code\", findings: [{symbol: $sym, status: open, says: \"a comment disagrees with its code\", at: $sha}]}" \
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
land_commit aaaa1111
wait_task aaaa1111 20 && ok "a task of aaaa1111's own, in $SECONDS s" || bad "no task for aaaa1111"
review aaaa1111 || bad "could not complete aaaa1111's review"
wait_field aaaa1111 reviewedSha aaaa1111 20 && ok "aaaa1111 is reviewed, and says so" \
	|| bad "aaaa1111 reviewedSha=$(field aaaa1111 reviewedSha)"
wait_finding pkg.Thing 15 && ok "the finding is in the ledger" || bad "no finding was derived"
# The read the real charter needs: the report of ONE commit, at an address the reader spells
# out of a sha it already has. `id:` on rule 1 is what makes that address exist.
v data verse-dev.drift.train.t-aaaa1111 --put "{sha: aaaa1111}" >/dev/null
t0=$SECONDS; got=
for _ in $(seq 1 40); do
	if v data verse-dev.drift.train.t-aaaa1111 2>/dev/null | grep -q 'says: "reviewed aaaa1111'; then got=1; break; fi
	sleep 0.5
done
[ -n "$got" ] \
	&& ok "the train carries aaaa1111's own report, read at an address it spelled, after $((SECONDS-t0))s" \
	|| bad "the train has no report after $((SECONDS-t0))s:\n$(v data verse-dev.drift.train 2>/dev/null)"
# …and a train cut before the review lands loads NOTHING, rather than a plausible empty: the
# record has no `result.report` yet, a get of a field that is not there is a failure, and a
# refused firing writes nothing at all.
v data verse-dev.drift.train.t-early --put "{sha: 9999early}" >/dev/null
sleep 2
v data verse-dev.drift.train.t-early 2>/dev/null | grep -q 'says' \
	&& bad "a train for an unreviewed sha loaded something:\n$(v data verse-dev.drift.train.t-early)" \
	|| ok "a train cut before the review holds no report, and no empty one either"

step "2. a commit that lands WHILE a review is running"
land_commit bbbb2222
wait_task bbbb2222 20 || bad "no task for bbbb2222"
land_commit cccc3333                     # arrives before bbbb2222's review answers
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

step "3. two commits landing in ONE push"
# NOT two head moves — that is two pushes, which scenario 2 already covers. A push of two
# commits moves the tip ONCE: verse sees eeee5555 and never sees dddd4444 as a tip at all.
#
# The git source does reflect both, as `git:commit:<repo>/<sha>` — main's last N commits, from
# `git log -n <depth>` on the mirror — so the commit exists as a fact even though the tip never
# named it. Whether the flow reviews it is the open question this scenario holds.
reflect_commit dddd4444 cccc3333       # the middle commit: reflected, and never a tip
reflect_commit eeee5555 dddd4444       # …and the tip, the only one the head ever names
move_head eeee5555
LAST=eeee5555
both=1
for sha in eeee5555 dddd4444; do
	if wait_task "$sha" 15 && review "$sha" && wait_field "$sha" reviewedSha "$sha" 15; then
		continue
	fi
	both=
	[ "$sha" = eeee5555 ] && bad "the tip of the push was not reviewed"
done
[ -n "$both" ] && ok "both commits of the push were reviewed" \
	|| known "only the tip was reviewed: the chain fires on git:head:verse, which names one commit per push, so dddd4444 never becomes a subject — the source reflects it as git:commit:verse/dddd4444 and nothing reads that"

step "4. a commit chosen by its PARENTS rather than by being the tip"
# The shape the git source writes: `parents` is a LIST of shas, so rule 5 can ask `!key()
# [aaaa1111]` — "is aaaa1111 one of this commit's parents" — which is a question no predicate
# could put to the space-joined string the field used to be. Five commits are in the verse by
# now, in a chain, and exactly one of them landed on aaaa1111.
#
# Asserted over the WHOLE listing rather than one record at a time: "these and no others" is the
# claim, and a per-record read that fails transiently would report a commit as unselected — the
# same shape as reading an unreadable store as an empty one (tyr7f6dhh). `--depth 4` because
# these records are addressed at the place plus a sha.
kids() { v data verse-dev.drift.child --paths --depth 4 2>/dev/null; }
t0=$SECONDS
for _ in $(seq 1 30); do [ -n "$(kids)" ] && break; sleep 0.5; done
sleep 2   # …and settle, so a WRONG extra selection has the same window to show up
got=$(kids)
if [ "$got" = "verse-dev.drift.child.bbbb2222" ]; then
	ok "exactly one of the five was selected by !key() over its parents, and it is bbbb2222, after $((SECONDS-t0))s"
else
	bad "the commits selected as children of aaaa1111 are:\n$got\nwant exactly verse-dev.drift.child.bbbb2222 — the one that landed on it"
fi

step "result"
if [ "$fails" -gt 0 ]; then
	printf '%s%s ASSERTION(S) FAILED%s\n' "$red" "$fails" "$rst"; exit 1
fi
printf '%sthe harness works%s — the ~ lines above are the defects it exists to hold still\n' "$grn" "$rst"
