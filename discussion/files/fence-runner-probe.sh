#!/usr/bin/env bash
# fence-runner-probe.sh — who may spawn onto a runner is who may read its entity.
#
# Runs against a THROWAWAY local verse (VERSE_CONFIG must name it; the runner URL is a closed
# port, so a permitted spawn fails as "unreachable" and a refused one never starts). Acts as
# three principals via VERSE_USER; OWNER is the config's own user. Cleans up what it creates.
#
#   VERSE_CONFIG=~/.config/verse-local.tony ./fence-runner-probe.sh
set -u
: "${VERSE_CONFIG:?name the throwaway verse explicitly}"
export VERSE_CONFIG
OWNER=${OWNER:-owner@example.com}
MEMBER=bo@example.com
OUTSIDER=carol@example.com
R=pool:fence-probe
RADDR="verse.runners.$R"
N=$(date +%s)   # task ids derive from the subject, so each run fires on subjects no earlier run used
fail=0

as() { local u=$1; shift; VERSE_USER=$u verse "$@" 2>&1; }
refused() { grep -q 'no runner'; }
check() { # check <label> <want: spawned|refused> <output>
	local got="neither: $3"   # a spawn prints its id (ad hoc) or its record names the runner (rule)
	printf '%s' "$3" | grep -qE '^run-|runnerName: verse.runners' && got=spawned
	printf '%s' "$3" | refused && got=refused
	printf '%s' "$3" | grep -q NOFIRE && got="no firing"
	if [ "$got" = "$2" ]; then echo "ok   $1: $got"; else echo "FAIL $1: want $2, got $got"; fail=1; fi
}
adhoc() { as "$1" task run "fence probe" --runner "$R" --vector auto; }
rule_fire() { # rule_fire <writer> <subject step> → the task record, or NOFIRE if the rule never spawned
	local s="$2$N"
	as "$1" data fenceprobe "$s" -put '{stage: open}' >/dev/null
	sleep 3
	local rec; rec=$(verse task get "fence-probe@fenceprobe.$s" 2>&1)
	case $rec in *"status:"*) printf '%s\n' "$rec" ;; *) echo NOFIRE ;; esac
}
cleanup() {
	as "$MEMBER" charter -r rules fence-probe >/dev/null
	# whoever created the place owns it: the member on a normal run
	as "$MEMBER" data fenceprobe -rm >/dev/null || verse data fenceprobe -rm >/dev/null 2>&1
	verse runner remove "$R" >/dev/null 2>&1
	verse group remove fence-probe >/dev/null 2>&1
}
trap cleanup EXIT
cleanup

verse runner add "$R" http://127.0.0.1:1 >/dev/null 2>&1

echo "# ad hoc: verse task run --runner"
check "open runner, member" spawned "$(adhoc $MEMBER)"
verse perms verse runners "$R" --grants '[]' >/dev/null
check "owner-only, member" refused "$(adhoc $MEMBER)"
check "owner-only, owner" spawned "$(adhoc $OWNER)"
verse group add fence-probe "$OWNER" "$MEMBER" >/dev/null
verse perms verse runners "$R" --grants '[{read: [group:fence-probe]}]' >/dev/null
check "group read, member" spawned "$(adhoc $MEMBER)"
check "group read, outsider" refused "$(adhoc $OUTSIDER)"
verse group add fence-probe "$OWNER" >/dev/null
check "member dropped" refused "$(adhoc $MEMBER)"

echo "# read-write lets a member repoint the runner (why to grant read)"
verse group add fence-probe "$OWNER" "$MEMBER" >/dev/null
verse perms verse runners "$R" --grants '[{read-write: [group:fence-probe]}]' >/dev/null
as $MEMBER runner add "$R" http://127.0.0.1:2 >/dev/null
url=$(verse runner show "$R" 2>&1 | sed -n 's/^url: //p')
[ "$url" = "http://127.0.0.1:2" ] && echo "ok   read-write member repointed url (hazard confirmed)" \
	|| { echo "FAIL expected the member's repoint to land, url=$url"; fail=1; }
as $MEMBER perms verse runners "$R" --grants '[{read: ["*"]}]' | grep -q 'needs owner' \
	&& echo "ok   read-write member cannot change perms" || { echo "FAIL member changed perms"; fail=1; }
as $MEMBER runner remove "$R" | grep -q 'needs owner' \
	&& echo "ok   read-write member cannot remove" || { echo "FAIL member removed runner"; fail=1; }

echo "# charter: the spawn reads the runner as the RULE'S OWNER"
verse runner add "$R" http://127.0.0.1:1 >/dev/null 2>&1
verse perms verse runners "$R" --grants '[]' >/dev/null
cat <<EOF | as $MEMBER charter -a -f - >/dev/null
- name: fence-probe
  condition:
    subject: {path: "fenceprobe.*", value: { stage: open }}
  action:
    patch:  { stage: fired }
    spawn:  { driver: scripted, vector: auto, runner: "$RADDR", prompt: "echo probe \$subject" }
EOF
[ "$(verse charter 2>&1 | grep -c 'name: fence-probe')" = 1 ] || { echo "FAIL the member's charter did not install"; exit 1; }
check "owner-only, member's rule" refused "$(rule_fire $MEMBER a)"
verse group add fence-probe "$OWNER" "$MEMBER" >/dev/null
verse perms verse runners "$R" --grants '[{read: [group:fence-probe]}]' >/dev/null
check "group read, member's rule" spawned "$(rule_fire $MEMBER b)"
verse group add fence-probe "$OWNER" >/dev/null
check "member dropped, member's rule" refused "$(rule_fire $MEMBER c)"
as $MEMBER perms fenceprobe --grant "{read-write: [$OWNER]}" --below >/dev/null
check "owner writes the subject, member's rule" refused "$(rule_fire $OWNER d)"

exit $fail
