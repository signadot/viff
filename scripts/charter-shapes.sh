#!/usr/bin/env bash
#
# charter-shapes.sh — the coarse-grained shapes a charter rule comes in, one command each,
# so you can install one, make it fire, and read what it did.
#
# Every rule verse runs has the same four parts (condition / gates / action, under a name).
# What varies is which parts are present, and the variations below are the ones that change
# how a rule BEHAVES rather than what it is about. Each acts on its own kind — demo.latch.*,
# demo.seq.* — so several can stand at once without arguing over an entity.
#
#   ./scripts/charter-shapes.sh                   list the shapes
#   ./scripts/charter-shapes.sh show    <shape>   print the rule, without installing it
#   ./scripts/charter-shapes.sh install <shape>   install it (idempotent: it re-installs)
#   ./scripts/charter-shapes.sh drive   <shape>   write the entity that makes it fire
#   ./scripts/charter-shapes.sh run     <shape>   install, then drive
#   ./scripts/charter-shapes.sh remove  [shape]   uninstall one, or all of them
#   ./scripts/charter-shapes.sh clean             uninstall all, and delete the entities they acted on
#
# Env: VERSE (the binary, default: ./verse then `verse` on PATH), VERSE_ADDR (the verse to
# talk to). --scope <name> works on every subcommand.
set -euo pipefail

# The scripts live in scripts/ and act on the checkout above them: the `./verse` a
# build produces is at the root, and so is the reader's cwd.
cd "$(cd "$(dirname "$0")/.." && pwd)"

SHAPES=(patch latch ask seq when on vote derive)

# The strata the `derive` shape declares. They are named here because removing one is REFUSED
# while a rule still names it — so the teardown has to go rule-first, which is that refusal
# doing its job rather than getting in the way.
DERIVE_STRATA=(shapes-source shapes-derived)

describe() {
	case "$1" in
	patch) echo "condition + patch. No task at all: a rule can be pure state." ;;
	latch) echo "spawn + onDone. The shape that settles — the action advances the subject out of its own selection." ;;
	ask) echo "one ask gate. The firing waits for a person (verse gate)." ;;
	seq) echo "seq of two steps, with an onFailed off-ramp. The depth axis." ;;
	when) echo "a condition on ANOTHER entity: act on these, while that holds." ;;
	on) echo "a cause: fire on the rising edge, not while it stays true (and no backlog)." ;;
	vote) echo "a gate formula the verse resolves itself: a quorum, on the docket." ;;
	derive) echo "an action that writes an entity which is NOT its subject, and a declared ordering saying where that may land." ;;
	esac
}

# The rule, as tony. `show` prints it and `install` pipes it in — one source, so what you
# read is what runs.
shape_tony() {
	case "$1" in
	patch)
		cat <<-'TONY'
			- name: shape-patch
			  condition:
			    subject: {path: [demo, patch, *], value: { stage: open }}
			  action:
			    patch: { stage: triaged }
		TONY
		;;
	latch)
		cat <<-'TONY'
			- name: shape-latch
			  condition:
			    subject: {path: [demo, latch, *], value: { stage: open }}
			  action:
			    patch:  { stage: reviewing }
			    spawn:  { driver: scripted, vector: auto, prompt: "echo reviewing $subject; sleep 2" }
			    onDone: { patch: { stage: reviewed } }
		TONY
		;;
	ask)
		cat <<-'TONY'
			- name: shape-ask
			  condition:
			    subject: {path: [demo, ask, *], value: { stage: open }}
			  gates:
			  - { name: approval, mode: ask, onDenied: { patch: { stage: rejected } } }
			  action:
			    patch:  { stage: reviewing }
			    spawn:  { driver: scripted, vector: auto, prompt: "echo reviewing $subject; sleep 2" }
			    onDone: { patch: { stage: reviewed } }
		TONY
		;;
	seq)
		cat <<-'TONY'
			- name: shape-seq
			  condition:
			    subject: {path: [demo, seq, *], value: { stage: ready }}
			  action:
			    seq:
			    - spawn: { driver: scripted, vector: auto, prompt: "echo lint $subject" }
			    - spawn:    { driver: scripted, vector: auto, prompt: "echo test $subject" }
			      onFailed: { patch: { stage: broken } }
			    onDone:
			      patch: { stage: built }
		TONY
		;;
	when)
		cat <<-'TONY'
			- name: shape-when
			  condition:
			    subject: {path: [demo, when, *], value: { stage: ready }}
			    when:    {path: [ci, status, shapes], value: { state: green }}
			  action:
			    patch: { stage: shipped }
		TONY
		;;
	on)
		cat <<-'TONY'
			- name: shape-on
			  condition:
			    subject: {path: [demo, on, *], value: { stage: waiting }}
			    on:      {path: [ci, status, release], value: { state: green }}
			  action:
			    patch: { stage: released }
		TONY
		;;
	derive)
		cat <<-'TONY'
			# A charter file may hold strata as well as rules; `verse c -a` sorts them
			# and installs the ordering first. A rule's subject must be inside its own
			# stratum, and a derive must land in a strictly higher one — both checked at
			# install, neither reading the store.
			- name: shapes-source
			  level: 0
			  match: demo
			- name: shapes-derived
			  level: 10
			  match: derived
			---
			- name: shape-derive
			  stratum: shapes-source
			  condition:
			    subject: {path: [demo, derive, *], value: { stage: open }}
			  action:
			    patch: { stage: raised }
			    derive:
			      derived:
			        feature:
			          priority: urgent
			          # A derive may reference its own subject and nothing else — which is
			          # what lets its content be checked at install and what keeps a re-fire
			          # a no-op. `.value` reads at the commit that caused the firing.
			          about: $subject
			          from: $subject.value.title
		TONY
		;;
	vote)
		cat <<-'TONY'
			- name: shape-vote
			  condition:
			    subject: {path: [demo, vote, *], value: { stage: shipped }}
			  gates:
			  - { name: approvals, formula: !quorum 2 }
			  action:
			    patch: { stage: merged }
		TONY
		;;
	esac
}

step() { printf '\n\033[1m# %s\033[0m\n' "$*"; }
ok() { printf '  \033[32m✓\033[0m %s\n' "$*"; }
die() {
	printf '  \033[31m✗ %s\033[0m\n' "$*" >&2
	exit 1
}

scope=()
args=()
while [ $# -gt 0 ]; do
	case "$1" in
	--scope)
		scope=(--scope "$2")
		shift
		;;
	--scope=*) scope=(--scope "${1#*=}") ;;
	-h | --help)
		sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*) args+=("$1") ;;
	esac
	shift
done
set -- "${args[@]+"${args[@]}"}"

BIN=${VERSE:-}
if [ -z "$BIN" ]; then
	if [ -x ./verse ]; then
		BIN=./verse
	elif command -v verse >/dev/null 2>&1; then
		BIN=verse
	else
		die "no verse binary — build one (go build -o verse ./cmd/verse) or set VERSE=<path>"
	fi
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
v() { "$BIN" "${scope[@]}" "$@"; }

known() {
	for s in "${SHAPES[@]}"; do [ "$s" = "$1" ] && return 0; done
	return 1
}
need_shape() {
	[ -n "${1:-}" ] || die "which shape? (${SHAPES[*]})"
	known "$1" || die "no shape %q — try: ${SHAPES[*]}"
}
up_or_die() {
	v status >/dev/null 2>&1 ||
		die "no verse answering at ${VERSE_ADDR:-its default address} — start one with \`$BIN up\`"
}

# remove_shape takes a shape out, whatever it declared. A shape is usually one rule; `derive`
# also brings an ordering, and the ordering goes LAST because a stratum still holding rules
# refuses to be removed.
remove_shape() {
	v charter remove "shape-$1" >/dev/null 2>&1 || true
	if [ "$1" = derive ]; then
		for st in "${DERIVE_STRATA[@]}"; do v charter remove "$st" >/dev/null 2>&1 || true; done
	fi
}

install_shape() {
	remove_shape "$1" # idempotent: add refuses a duplicate, of a rule or of a stratum
	shape_tony "$1" | v charter -a -f - >/dev/null || die "installing shape-$1 failed"
	ok "installed shape-$1 — $(describe "$1")"
}

# What makes each shape fire. Most are one put; the two that read a second entity need the
# world put in the right order, which is itself the point of those shapes.
drive_shape() {
	case "$1" in
	patch) v data demo patch 1 --put '{stage: open, title: "a triage"}' >/dev/null ;;
	latch) v data demo latch 1 --put '{stage: open, title: "a review"}' >/dev/null ;;
	ask) v data demo ask 1 --put '{stage: open, title: "a gated review"}' >/dev/null ;;
	seq) v data demo seq 1 --put '{stage: ready, title: "a build"}' >/dev/null ;;
	when)
		v data demo when 1 --put '{stage: ready, title: "a release"}' >/dev/null
		v data ci status shapes --put '{state: green}' >/dev/null
		;;
	on)
		# An edge has to RISE, so drop it first: a `ci:status:release` left green by an
		# earlier run is a predicate that never becomes true again, and the rule that reads
		# it would look broken rather than edge-triggered.
		v data ci status release --put '{state: red}' >/dev/null
		v data demo on 1 --put '{stage: waiting, title: "an edge"}' >/dev/null
		sleep 1
		v data ci status release --put '{state: green}' >/dev/null
		;;
	vote) v data demo vote 1 --put '{stage: shipped, title: "a merge"}' >/dev/null ;;
	derive) v data demo derive 1 --put '{stage: open, title: "a feature request"}' >/dev/null ;;
	esac
	ok "drove shape-$1"
}

# What to look at once it has fired — different per shape, because what a shape DOES is
# visible in a different place: a gate, the docket, a flow, or just the entity.
next_steps() {
	# The subject twice: as the STEPS `verse data` takes, and as the one token `flow show`
	# takes — a flow is keyed by its subject as the store renders it.
	local sub_at="demo $1 1" sub="demo.$1.\"1\"" c="$BIN"
	[ ${#scope[@]} -gt 0 ] && c="$BIN ${scope[*]}"
	printf '\nwhat it did:\n\n  %s data %s\n' "$c" "$sub_at"
	case "$1" in
	ask) printf '  %s gates                  ← it is waiting for you\n  %s gate approve <id> --as you\n' "$c" "$c" ;;
	vote) printf '  %s docket                 ← it is waiting for a tally\n  %s vote <motion> approve --as alice\n  %s vote <motion> approve --as bob\n' "$c" "$c" "$c" ;;
	latch | seq) printf '  %s flow show %s  ← the shape of the work, and what comes next\n' "$c" "$sub" ;;
	on) printf '  (now re-drive it: nothing fires, because the edge already rose)\n' ;;
	derive)
		printf '  %s data derived feature shape-derive demo derive 1\n' "$c"
		printf '  %s strata                 ← the ordering it was checked against\n' "$c"
		printf '\n  the derived ADDRESS is the ENGINE'"'"'s — the place, then the rule, then the\n'
		printf '  subject'"'"'s own segments — so a derivation is addressed by what it is about,\n'
		printf '  and no two rules can ever address the same entity.\n'
		;;
	esac
	printf '\n'
}

cmd=${1:-list}
[ $# -gt 0 ] && shift || true

case "$cmd" in
list)
	printf '\nthe shapes a rule comes in:\n\n'
	for s in "${SHAPES[@]}"; do printf '  \033[1m%-7s\033[0m %s\n' "$s" "$(describe "$s")"; done
	printf '\n  %s show <shape> · install <shape> · drive <shape> · run <shape> · remove [shape]\n\n' "$0"
	;;
show)
	need_shape "${1:-}"
	shape_tony "$1"
	;;
install)
	need_shape "${1:-}"
	up_or_die
	step "install"
	install_shape "$1"
	;;
drive)
	need_shape "${1:-}"
	up_or_die
	step "drive"
	drive_shape "$1"
	next_steps "$1"
	;;
run)
	need_shape "${1:-}"
	up_or_die
	step "install"
	install_shape "$1"
	step "drive"
	drive_shape "$1"
	next_steps "$1"
	;;
remove)
	up_or_die
	step "remove"
	if [ -n "${1:-}" ]; then
		need_shape "$1"
		remove_shape "$1" && ok "removed shape-$1" || ok "shape-$1 was not installed"
	else
		for s in "${SHAPES[@]}"; do remove_shape "$s"; done
		ok "removed every shape trigger"
	fi
	;;
clean)
	up_or_die
	step "clean"
	for s in "${SHAPES[@]}"; do
		remove_shape "$s"
		v data demo "$s" 1 --rm >/dev/null 2>&1 || true
	done
	v data ci status shapes --rm >/dev/null 2>&1 || true
	v data ci status release --rm >/dev/null 2>&1 || true
	# The derived entity is addressed by the ENGINE — id = f(rule, subject) — so it is named
	# here rather than guessed at: nothing else in the verse knows to clean it up.
	v data 'derived:feature:shape-derive@demo:derive:1' --rm >/dev/null 2>&1 || true
	ok "every shape trigger and the entities they acted on are gone"
	;;
*)
	die "unknown command %q (try --help)" "$cmd"
	;;
esac
