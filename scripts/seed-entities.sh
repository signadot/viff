#!/usr/bin/env bash
#
# seed-entities.sh — put a WORLD into a running verse: a dozen entities, all under verse-tutorial,
# with enough variety in their payloads that selecting over them says something.
#
# No charter, deliberately. This is the half of verse you can hold before any rule exists:
# entities are just addressed documents, and `--if` / `--if-at` select over what they say.
# The tutorial's data page walks these; `charter-shapes.sh` is the half that reacts to them.
#
#   ./verse up                         # in another terminal
#   ./scripts/seed-entities.sh
#   ./scripts/seed-entities.sh --scope me      # …into a copy-on-write scope instead of baseline
#   ./scripts/seed-entities.sh --clean         # remove what a previous run seeded
#
# Env: VERSE (the binary, default: ./verse then `verse` on PATH), VERSE_ADDR (the verse to
# seed, default: verse's own default).
set -euo pipefail

# The scripts live in scripts/ and act on the checkout above them: the `./verse` a
# build produces is at the root, and so is the reader's cwd.
cd "$(cd "$(dirname "$0")/.." && pwd)"

clean=false
scope=()
while [ $# -gt 0 ]; do
	case "$1" in
	--clean) clean=true ;;
	--scope)
		scope=(--scope "$2")
		shift
		;;
	--scope=*) scope=(--scope "${1#*=}") ;;
	-h | --help)
		sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		printf 'seed-entities: unknown argument %q (try --help)\n' "$1" >&2
		exit 2
		;;
	esac
	shift
done

step() { printf '\n\033[1m# %s\033[0m\n' "$*"; }
ok() { printf '  \033[32m✓\033[0m %s\n' "$*"; }
die() {
	printf '  \033[31m✗ %s\033[0m\n' "$*" >&2
	exit 1
}

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

v status >/dev/null 2>&1 ||
	die "no verse answering at ${VERSE_ADDR:-its default address} — start one with \`$BIN up\`"

# ── the world ────────────────────────────────────────────────────────────────
# Written as address + payload pairs so --clean is the same list read for its addresses, and so
# the variety is visible in one place: four kinds, and payloads that differ in
# stage, in owner, and in a nested `labels` object (which is what --if-at is for).
#
# The address is STEPS, one word per segment, which is what `verse data` takes and what its
# listings answer in — so it is handed over unquoted and the shell splits it back.
WORLD=(
	'verse-tutorial pr 1|{stage: open,   owner: ana, title: "add the token TTL",   labels: {urgent: true,  area: auth}}'
	'verse-tutorial pr 2|{stage: draft,  owner: bo,  title: "rename the docket",   labels: {urgent: false, area: docs}}'
	'verse-tutorial pr 3|{stage: open,   owner: ana, title: "cache the schema",    labels: {urgent: true,  area: store}}'
	'verse-tutorial pr 4|{stage: merged, owner: cy,  title: "bump go-tony",        labels: {urgent: false, area: build}}'
	'verse-tutorial pr 5|{stage: open,   owner: cy,  title: "drop the flow field", labels: {area: engine}}'
	'verse-tutorial task hotfix-1|{priority: high, owner: ana, title: "expiring cert"}'
	'verse-tutorial task hotfix-2|{priority: high, owner: bo,  title: "docd skew"}'
	'verse-tutorial task cleanup-1|{priority: low, owner: cy,  title: "tidy the fixtures"}'
	'verse-tutorial ci build|{state: green, commit: a1b2c3d}'
	'verse-tutorial ci e2e|{state: red, commit: a1b2c3d, failed: [auth, docket]}'
	'verse-tutorial person ana|{team: platform, reviews: [auth, store]}'
	'verse-tutorial person bo|{team: docs, reviews: [docs]}'
)

if $clean; then
	step "clean"
	# shellcheck disable=SC2086
	for w in "${WORLD[@]}"; do v data ${w%%|*} --rm >/dev/null 2>&1 || true; done
	ok "removed ${#WORLD[@]} seeded entities"
	exit 0
fi

# A world is only inert if nothing is watching it. The tutorial's charters page installs a
# charter at verse-tutorial, over exactly what this seeds, so say so rather than let the reader
# wonder why an entity they just wrote is already at a different stage.
if [ -n "$(v charter verse-tutorial -o tony 2>/dev/null | grep -v '^\[\]$')" ]; then
	printf '\n  \033[33m~\033[0m the tutorial\x27s charter is installed, so some of this will be reacted to at once.\n'
	printf '    For a world nothing is watching: %s charter -r verse-tutorial\n' "$BIN"
fi

step "seeding ${#WORLD[@]} entities"
for w in "${WORLD[@]}"; do
	# Unquoted: the WORLD table names each entity as its steps, and the shell splitting them
	# back into words is exactly how `verse data` takes an address.
	# shellcheck disable=SC2086
	v data ${w%%|*} --put "${w#*|}" >/dev/null || die "put ${w%%|*} failed"
done
ok "verse-tutorial.pr.1–5       three open, one draft, one merged; nested labels"
ok "verse-tutorial.task.*       two high-priority, one low"
ok "verse-tutorial.ci.*         build green, e2e red"
ok "verse-tutorial.person.*     two reviewers"

scope_note=""
[ ${#scope[@]} -gt 0 ] && scope_note=" ${scope[*]}"
cat <<EOF

seeded. Now select over it:

  $BIN$scope_note data                                                              the whole verse, one document
  $BIN$scope_note data verse-tutorial pr                                            by where it lives
  $BIN$scope_note data verse-tutorial pr 1                                          one of them
  $BIN$scope_note data --at 'verse-tutorial.pr.*.stage'                             one field of each, keyed by id
  $BIN$scope_note data verse-tutorial pr '*' --if '{stage: open}'                   by what it says
  $BIN$scope_note data verse-tutorial pr '*' --if '{stage: !or [open, merged]}'     …with an operator
  $BIN$scope_note data verse-tutorial pr '*' --if '{urgent: true}' --if-at labels   …rooted inside the payload
  $BIN$scope_note data --paths verse-tutorial pr                                    just the addresses

And watch it move (another terminal), then write to it:

  $BIN$scope_note watch --if '{stage: open}' verse-tutorial pr '*'
  $BIN$scope_note data verse-tutorial pr 2 --put '{stage: open}'

Undo it all with: $0 --clean
EOF
