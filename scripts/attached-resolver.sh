#!/usr/bin/env bash
#
# attached-resolver.sh — a service that answers a run's gates, one act each, so you can watch
# supervision happen instead of reading about it.
#
# A resolver is a BINDING (`verse:resolvers:<kind>:<name>`) plus whatever is currently reading for
# it (`verse:resolvers:instance:<name>@<origin>`). It is not a callback verse invokes: the service
# dials IN, reads the run's own stream, and posts a verdict — which is why the two commands
# that ACT as the resolver here use curl, and everything else is `verse`.
#
#   ./scripts/attached-resolver.sh                    list the acts
#   ./scripts/attached-resolver.sh show    <act>      print what it installs, without installing
#   ./scripts/attached-resolver.sh run     <act>      set it up and pose a question
#   ./scripts/attached-resolver.sh watch              read the RESOLVER's stream (ctrl-c to stop)
#   ./scripts/attached-resolver.sh attach             read the RUN's stream — the operator's view
#   ./scripts/attached-resolver.sh answer  [verdict]  answer AS the resolver: proceed|deny|escalate
#   ./scripts/attached-resolver.sh state              the binding, the instance, and the question
#   ./scripts/attached-resolver.sh clean              remove the bindings and the entities
#
# The acts, in the order they build on each other:
#
#   named       a spawn names its answerer. One layer, and ⊤ — the human docket — above it.
#   standing    nobody names anybody; a binding COVERS the subject and owns its questions.
#   layered     both. The named layer owns it; `escalate` hands it up, and again, to ⊤.
#   deadline    the same handover, more slowly: silence past `within` moves it on the next read.
#   exclusive   whose answer COUNTS. A person is refused, and told how to override.
#
# Env: VERSE (the binary, default: ./verse then `verse` on PATH), VERSE_ADDR (the verse to
# drive, default localhost:9797 — it is passed to the CLI as well as to curl, so both halves of
# this script talk to the SAME verse whatever the config layers say).
set -euo pipefail

cd "$(cd "$(dirname "$0")/.." && pwd)"

ACTS=(named standing layered deadline exclusive)
SUBJECT=change:pr:1
RUN_NAME=review

describe() {
	case "$1" in
	named) echo "a spawn names its answerer: one layer, and the human docket above it." ;;
	standing) echo "a binding that COVERS the subject: nobody named it, and it owns the question anyway." ;;
	layered) echo "both at once: named owns it, escalate hands it up, and the second hop is ⊤." ;;
	deadline) echo "silence does what escalate does: past \`within\`, the next reader finds it moved." ;;
	exclusive) echo "whose answer counts: a person is refused and told how to override." ;;
	esac
}

# ── the bindings ─────────────────────────────────────────────────────────────
# Every binding needs a door: `byRef` is the one a spawn or a rule comes through, `condition`
# is the one nobody has to open. Neither is refused at install — a binding nothing can reach
# answers nothing.

binding_named() {
	cat <<-'TONY'
		name:   reviewer
		kind:   security
		byRef:  true
		method: { driver: url, url: "https://reviewer.internal/verse" }
	TONY
}

binding_standing() {
	cat <<-'TONY'
		name: payments-review
		kind: security
		condition:
		  subject: { system: change, kind: pr, value: { area: payments } }
		method: { driver: url, url: "https://payments.internal/verse" }
	TONY
}

binding_deadline() {
	cat <<-'TONY'
		name:     reviewer
		kind:     security
		byRef:    true
		deadline: { within: "20s" }
		method:   { driver: url, url: "https://reviewer.internal/verse" }
	TONY
}

binding_exclusive() {
	cat <<-'TONY'
		name:      payments-review
		kind:      security
		exclusive: true
		deadline:  { within: "1h" }
		condition:
		  subject: { system: change, kind: pr, value: { area: payments } }
		method: { driver: url, url: "https://payments.internal/verse" }
	TONY
}

show() {
	case "$1" in
	named) binding_named ;;
	standing) binding_standing ;;
	layered | deadline)
		[ "$1" = deadline ] && binding_deadline || binding_named
		printf -- '---\n'
		binding_standing
		;;
	exclusive) binding_exclusive ;;
	esac
}

# ── setup ────────────────────────────────────────────────────────────────────

setup() {
	local bound=
	case "$1" in
	named)
		binding_named | v resolver add - >/dev/null
		bound=verse:resolvers:security:reviewer
		;;
	standing) binding_standing | v resolver add - >/dev/null ;;
	layered)
		binding_named | v resolver add - >/dev/null
		binding_standing | v resolver add - >/dev/null
		bound=verse:resolvers:security:reviewer
		;;
	deadline)
		binding_deadline | v resolver add - >/dev/null
		binding_standing | v resolver add - >/dev/null
		bound=verse:resolvers:security:reviewer
		;;
	exclusive) binding_exclusive | v resolver add - >/dev/null ;;
	esac
	ok "installed: $(v resolver -o tony | sed -n 's/^[[:space:]]*ref:[[:space:]]*//p' | tr '\n' ' ')"

	retire_previous

	# What the work is ABOUT. A standing binding matches on this, which is the whole difference
	# between the two doors: one is named by the work, the other selects it.
	v data --ref "$SUBJECT" --put '{area: payments, title: "raise the retry budget"}' >/dev/null
	ok "subject $SUBJECT"

	# A run that stops at its first action, so there is a real question to answer.
	#
	# --name is a LABEL: the id is derived and printed, because an id is the prefix of every
	# gate question the run raises and answering one cuts at the first `~`. --subject joins the
	# flow; --resolver is the named door, and a binding that is not installed is refused here
	# rather than silently shadowing whatever covers the subject.
	# --vector ask is what makes it stop and ASK: without it the run is `auto` and a resolver
	# bound to it would have nothing to answer. --driver scripted so the question is the same
	# one every time — the default driver is an llm, which may answer in prose and call no
	# tool at all, and a demo that sometimes poses no question teaches nothing.
	local run
	run=$(v task run 'echo reviewing' --name "$RUN_NAME" --subject "$SUBJECT" \
		--driver scripted --vector ask ${bound:+--resolver "$bound"})
	ok "spawned $run${bound:+, bound to $bound}"
}

# ── looking at it ────────────────────────────────────────────────────────────

# The run, asked for rather than remembered: the task about this subject that is STILL
# HOLDING A QUESTION. `pending` is the ref of the question it is stopped at, so asking for the
# runs that have one asks exactly the question this script is about — and an act that has been
# answered leaves nothing behind to be mistaken for the next one.
#
# Not "the last one listed": store order is not spawn order, so that picked an arbitrary run
# about the subject — sometimes one already finished, whose question never came and never would.
# The mirror is in verse's own half (`verse:task:instance:*`) and `--paths` answers in steps,
# so the id is the last step of each line.
run_id() {
	v data --paths --if "{flow: \"$SUBJECT\", pending: null}" verse.task.instance 2>/dev/null |
		awk 'NF {print $NF}' | head -1
}

# Whatever this subject was left holding from an earlier act. One act at a time is the whole
# contract here: two runs stopped at two questions about one subject is a fine thing for a
# verse to hold and a confusing thing to demonstrate.
retire_previous() {
	local old
	while old=$(run_id) && [ -n "$old" ]; do
		v task cancel "$old" >/dev/null 2>&1 || break
		sleep 0.2
	done
}

# The question this run raised, waited for. Filtered by the run rather than taking the first
# on the docket, so an act does not pick up the question another act left standing — the id of
# a run's question is `<run>~d<n>`, which is why the prefix is enough.
question_id() {
	local tries=0 run q
	while [ $tries -lt 40 ]; do
		# INSIDE the loop: a run that has just been spawned has not reached its gate yet, so
		# it holds no question and run_id does not see it. Resolving the run once and giving
		# up is how this came to report "no question was posed" about a run that was still
		# walking to the gate.
		run=$(run_id)
		if [ -n "$run" ]; then
			q=$(v gate -o tony | sed -n 's/^[[:space:]]*id:[[:space:]]*//p' | grep "^$run~" | head -1)
			if [ -n "$q" ]; then
				echo "$q"
				return 0
			fi
		fi
		tries=$((tries + 1))
		sleep 0.25
	done
}

field() { v data verse gate question "$1" -o tony | sed -n "s/^[[:space:]]*$2:[[:space:]]*//p"; }

state() {
	step "the bindings — who may answer, and for what"
	v resolver
	step "the instances — who is READING, one per (binding, origin)"
	v data verse resolvers instance
	printf '\n  the address is `<name>@<origin>`, so one `verse data verse resolvers` shows both what is\n'
	printf '  installed and who is watching what, with no singular/plural pair to mistype.\n'
	local q
	q=$(question_id)
	if [ -z "$q" ]; then
		printf '\n  no question is open — `run` an act first, or the last one was answered.\n\n'
		return
	fi
	step "the question — an entity, and it knows who owns it"
	v data "verse:gate:question:$q"
	printf '\n  resolver:      the owner. absent means ⊤, the docket a person reads.\n'
	printf '  escalatesAt:   its clock, if the owner has a deadline. absolute, so a restart cannot slide it.\n'
	printf '  reownedFrom/Why: the handover, once it has changed hands.\n'
	printf '  overrode:      who was answered OVER, if anyone.\n\n'
}

# ── being the resolver ───────────────────────────────────────────────────────
# The two streams a resolver lives on, addressed by INSTANCE — the binding watching one run —
# so neither route needs a second parameter to say which run it is about.

# The instance to act AS: the one whose binding currently owns the question, not merely the
# first one watching this run. After an escalation there are two — the layer that handed it on
# still has its instance, and reading a stream is not the same as owning a docket.
instance_of() {
	local run owner q
	run=$(run_id)
	[ -n "$run" ] || return 0
	q=$(v gate -o tony | sed -n 's/^[[:space:]]*id:[[:space:]]*//p' | grep "^$run~" | head -1)
	[ -n "$q" ] || return 0
	owner=$(field "$q" resolver)
	if [ -z "$owner" ]; then
		return 0 # ⊤ owns it: there is no instance to answer as, which is the point
	fi
	echo "${owner##*:}@$run"
}

# The same run, from the outside. The point of this one is what it does NOT hide: the ask, the
# decision with the binding in `by`, and the action that followed, in order, on the run's own
# read plane. There is no supervised run that reads as unsupervised, and no way to answer
# invisibly — which is a claim worth seeing rather than being told.
#
#   {kind: pending,  id: …~d1, mode: ASK, tool: run_bash}
#   {kind: decision, by: verse:resolvers:security:reviewer, proceed: true}
#   {kind: exec,     tool: run_bash, text: reviewing}
#   {kind: status,   text: done}
attach() {
	local run
	run=$(run_id)
	[ -n "$run" ] || die "no run is holding a question — run an act first"
	step "attaching to $run — what an OBSERVER sees"
	printf '  answer it from another terminal (%s answer) and watch the decision land here.\n\n' "$0"
	v task attach "$run"
}

watch() {
	local inst
	inst=$(instance_of)
	[ -n "$inst" ] || die "nothing is watching $(run_id) — run an act first"
	step "reading $inst from the beginning"
	printf '  this is the RUN'"'"'s own stream, not a projection of it: a resolver sees what an\n'
	printf '  attached observer sees, or there would be two orders to reconcile.\n\n'
	curl -sN "$(addr)/resolvers/instance/$inst/events?since=0"
}

answer() {
	local verdict="${1:-proceed}" inst q code
	inst=$(instance_of)
	if [ -z "$inst" ]; then
		die "no resolver owns this question — it is ⊤'s, so a person answers it:
      $BIN gates
      $BIN gate y <id> --as human:you"
	fi
	q=$(question_id)
	[ -n "$q" ] || die "no open question"
	step "answering as $inst"
	code=$(curl -s -o /tmp/attached-resolver.out -w '%{http_code}' -X POST \
		-H 'Content-Type: application/tony' \
		-d "{id: \"$q\", verdict: $verdict}" \
		"$(addr)/resolvers/instance/$inst/verdicts")
	case "$code" in
	204)
		ok "$verdict"
		printf '  `by` on the record is the BINDING, stamped by verse. A service says what it\n'
		printf '  decided, never who it is.\n'
		[ "$verdict" = escalate ] && printf '  now: %s state — `resolver:` has changed, and `reownedFrom:` says what moved it\n' "$0"
		;;
	*) printf '  \033[31m✗\033[0m %s: %s\n' "$code" "$(cat /tmp/attached-resolver.out)" ;;
	esac
	printf '\n'
}

# ── acts ─────────────────────────────────────────────────────────────────────

run_act() {
	step "$1 — $(describe "$1")"
	setup "$1"
	local q owner
	q=$(question_id)
	[ -n "$q" ] || die "no question was posed"
	owner=$(field "$q" resolver)
	ok "question $q"
	printf '  owner: %s\n' "${owner:-⊤ (nobody named, nothing covers it — the human docket)}"

	case "$1" in
	named | standing)
		next "$0 watch" "the RESOLVER's stream — what it decides on" \
			"$0 attach" "the RUN's stream — and answer from another terminal to watch it land" \
			"$0 answer" "answer as the bound resolver" \
			"$0 state" "the binding, the instance, and the question"
		;;
	layered)
		next "$0 answer escalate" "hand it to the layer above" \
			"$0 answer escalate" "again — and the second hop is ⊤, with no counter anywhere" \
			"$0 state" "watch resolver: change, and reownedFrom: appear"
		;;
	deadline)
		printf '  clock: %s\n' "$(field "$q" escalatesAt)"
		printf '\n  say nothing. The expiry is evaluated when a READER arrives, because the layer\n'
		printf '  above is the only party it is for — a sweeper would compute the same transfer\n'
		printf '  slightly earlier, for nobody.\n'
		next "sleep 21; $BIN gates" "read it late: it has already moved" \
			"$0 state" "reownedWhy: overdue, and the clock cleared for its new owner"
		;;
	exclusive)
		next "$BIN gate y $q --as human:you" "refused, and told who owns it" \
			"$BIN gate y $q --as human:you --override" "accepted, and the record names you both" \
			"$0 state" "overrode: beside by:"
		;;
	esac
}

next() {
	printf '\nnext:\n\n'
	while [ $# -ge 2 ]; do
		printf '  %s\n      %s\n' "$1" "$2"
		shift 2
	done
	printf '\n'
}

clean() {
	local run
	run=$(run_id)
	[ -n "$run" ] && v task cancel "$run" >/dev/null 2>&1 || true
	for kn in security:reviewer security:payments-review; do
		v resolver remove "$kn" >/dev/null 2>&1 || true
	done
	v data --ref "$SUBJECT" --rm >/dev/null 2>&1 || true
	ok "removed the bindings, the run and $SUBJECT"
	printf '  answered questions are kept: a docket that forgets is one nobody can audit.\n\n'
}

# ── plumbing ─────────────────────────────────────────────────────────────────

step() { printf '\n\033[1m# %s\033[0m\n' "$*"; }
ok() { printf '  \033[32m✓\033[0m %s\n' "$*"; }
die() {
	printf '  \033[31m✗\033[0m %s\n' "$*" >&2
	exit 1
}

BIN="${VERSE:-}"
if [ -z "$BIN" ]; then
	if [ -x ./verse ]; then BIN=./verse; else BIN=verse; fi
fi

# THE BINARY HAS TO SPEAK `state`. These scripts read the verse as one document at a kinded
# path, and a CLI from before that has `entity` instead — whose failure is a usage error and
# the whole command tree, which says nothing about why. The trap is a STALE `./verse` in the
# working directory: it is preferred over $PATH above, so a checkout that built one months ago
# keeps using it long after `go install` refreshed the real one.
# `|| true` into a variable, NOT a pipe from "$BIN": bare `verse` exits 1 (it just printed
# usage), and these scripts run under `set -o pipefail`, which hands that 1 to the whole
# pipeline however well the grep matched — so the pipe spelling fired this guard on every
# binary, current ones included.
_verse_cmds=$("$BIN" 2>&1 || true)
if ! printf '%s\n' "$_verse_cmds" | grep -qE '^ *data '; then
	die "$BIN does not have \`verse data\` — it predates the one-document read.
     Rebuild it (go build -o verse ./cmd/verse), remove a stale ./verse so \$PATH wins,
     or point VERSE=<path> at a current one."
fi
scope=()
args=()
while [ $# -gt 0 ]; do
	case "$1" in
	--scope)
		scope=(--scope "$2")
		shift 2
		;;
	*)
		args+=("$1")
		shift
		;;
	esac
done
set -- ${args[@]+"${args[@]}"}

# ONE TARGET, NAMED ONCE. Both halves of this script talk to a verse — the CLI for everything
# and curl for the two commands that ACT as the resolver — and they used to name it two
# different ways: the CLI resolved its address from the config layers, while curl built
# `http://${VERSE_ADDR:-localhost:9797}`. With a config pointing at staging, the run and its
# question were created there and the verdict was posted to localhost, which answers 404. Every
# act failed at `answer`, and nothing said why, because both halves were behaving.
ADDR=${VERSE_ADDR:-localhost:9797}
v() { "$BIN" --addr "$ADDR" "${scope[@]}" "$@"; }
addr() { echo "http://$ADDR"; }

command -v curl >/dev/null || die "this one needs curl: a resolver dials in over HTTP"
v data >/dev/null 2>&1 || die "no verse at $ADDR — start one with \`./verse up\`, or set VERSE_ADDR to name another"

case "${1:-}" in
"" | list)
	printf '\nacts:\n\n'
	for a in "${ACTS[@]}"; do printf '  %-10s %s\n' "$a" "$(describe "$a")"; done
	printf '\n  %s run <act>\n\n' "$0"
	;;
show) show "${2:?which act}" ;;
run) run_act "${2:?which act}" ;;
watch) watch ;;
attach) attach ;;
answer) answer "${2:-proceed}" ;;
state) state ;;
clean) clean ;;
*) die "unknown command: $1" ;;
esac
