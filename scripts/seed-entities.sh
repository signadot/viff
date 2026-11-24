#!/usr/bin/env bash
#
# seed-entities.sh — put a WORLD into a running viff instance: a dozen entities, all under viff-tutorial,
# with enough variety in their payloads that selecting over them says something.
#
# No charter, deliberately. This is the half of viff you can hold before any rule exists:
# entities are just addressed documents, and `--if` / `--if-at` select over what they say.
# The tutorial's data page walks these; `charter-shapes.sh` is the half that reacts to them.
#
#   ./viff up                         # in another terminal
#   ./scripts/seed-entities.sh
#   ./scripts/seed-entities.sh --scope me      # …into a copy-on-write scope instead of baseline
#   ./scripts/seed-entities.sh --clean         # remove what a previous run seeded
#
# Env: VIFF (the binary, default: ./viff then `viff` on PATH), VIFF_ADDR (the viff instance to
# seed, default: viff's own default).
set -euo pipefail

# The scripts live in scripts/ and act on the checkout above them: the `./viff` a
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

BIN=${VIFF:-}
if [ -z "$BIN" ]; then
	if [ -x ./viff ]; then
		BIN=./viff
	elif command -v viff >/dev/null 2>&1; then
		BIN=viff
	else
		die "no viff binary — build one (go build -o viff ./cmd/viff) or set VIFF=<path>"
	fi
fi

# THE BINARY HAS TO SPEAK `state`. These scripts read the viff instance as one document at a kinded
# path, and a CLI from before that has `entity` instead — whose failure is a usage error and
# the whole command tree, which says nothing about why. The trap is a STALE `./viff` in the
# working directory: it is preferred over $PATH above, so a checkout that built one months ago
# keeps using it long after `go install` refreshed the real one.
# `|| true` into a variable, NOT a pipe from "$BIN": bare `viff` exits 1 (it just printed
# usage), and these scripts run under `set -o pipefail`, which hands that 1 to the whole
# pipeline however well the grep matched — so the pipe spelling fired this guard on every
# binary, current ones included.
_viff_cmds=$("$BIN" 2>&1 || true)
if ! printf '%s\n' "$_viff_cmds" | grep -qE '^ *data '; then
	die "$BIN does not have \`viff data\` — it predates the one-document read.
     Rebuild it (go build -o viff ./cmd/viff), remove a stale ./viff so \$PATH wins,
     or point VIFF=<path> at a current one."
fi
v() { "$BIN" ${scope[@]+"${scope[@]}"} "$@"; }

v status >/dev/null 2>&1 ||
	die "no viff answering at ${VIFF_ADDR:-its default address} — start one with \`$BIN up\`"

# ── the world ────────────────────────────────────────────────────────────────
# Written as address + payload pairs so --clean is the same list read for its addresses, and so
# the variety is visible in one place: four kinds, and payloads that differ in
# stage, in owner, and in a nested `labels` object (which is what --if-at is for).
#
# The address is STEPS, one word per segment, which is what `viff data` takes and what its
# listings answer in — so it is handed over unquoted and the shell splits it back.
WORLD=(
	'viff-tutorial pr 1|{stage: open,   owner: ana, title: "add the token TTL",   labels: {urgent: true,  area: auth}}'
	'viff-tutorial pr 2|{stage: draft,  owner: bo,  title: "rename the docket",   labels: {urgent: false, area: docs}}'
	'viff-tutorial pr 3|{stage: open,   owner: ana, title: "cache the schema",    labels: {urgent: true,  area: store}}'
	'viff-tutorial pr 4|{stage: merged, owner: cy,  title: "bump go-tony",        labels: {urgent: false, area: build}}'
	'viff-tutorial pr 5|{stage: open,   owner: cy,  title: "drop the flow field", labels: {area: engine}}'
	'viff-tutorial task hotfix-1|{priority: high, owner: ana, title: "expiring cert"}'
	'viff-tutorial task hotfix-2|{priority: high, owner: bo,  title: "docd skew"}'
	'viff-tutorial task cleanup-1|{priority: low, owner: cy,  title: "tidy the fixtures"}'
	'viff-tutorial ci build|{state: green, commit: a1b2c3d}'
	'viff-tutorial ci e2e|{state: red, commit: a1b2c3d, failed: [auth, docket]}'
	'viff-tutorial person ana|{team: platform, reviews: [auth, store]}'
	'viff-tutorial person bo|{team: docs, reviews: [docs]}'
)

if $clean; then
	step "clean"
	# shellcheck disable=SC2086
	for w in "${WORLD[@]}"; do v data ${w%%|*} --rm >/dev/null 2>&1 || true; done
	ok "removed ${#WORLD[@]} seeded entities"
	exit 0
fi

# A world is only inert if nothing is watching it. The tutorial's charters page installs a
# charter at viff-tutorial, over exactly what this seeds, so say so rather than let the reader
# wonder why an entity they just wrote is already at a different stage.
if [ -n "$(v charter viff-tutorial -o tony 2>/dev/null | grep -v '^\[\]$')" ]; then
	printf '\n  \033[33m~\033[0m the tutorial\x27s charter is installed, so some of this will be reacted to at once.\n'
	printf '    For a world nothing is watching: %s charter -r viff-tutorial\n' "$BIN"
fi

step "seeding ${#WORLD[@]} entities"
for w in "${WORLD[@]}"; do
	# Unquoted: the WORLD table names each entity as its steps, and the shell splitting them
	# back into words is exactly how `viff data` takes an address.
	# shellcheck disable=SC2086
	v data ${w%%|*} --put "${w#*|}" >/dev/null || die "put ${w%%|*} failed"
done
ok "viff-tutorial.pr.1–5       three open, one draft, one merged; nested labels"
ok "viff-tutorial.task.*       two high-priority, one low"
ok "viff-tutorial.ci.*         build green, e2e red"
ok "viff-tutorial.person.*     two reviewers"

scope_note=""
[ ${#scope[@]} -gt 0 ] && scope_note=" ${scope[*]}"
cat <<EOF

seeded. Now select over it:

  $BIN$scope_note data                                                              the whole viff instance, one document
  $BIN$scope_note data viff-tutorial pr                                            by where it lives
  $BIN$scope_note data viff-tutorial pr 1                                          one of them
  $BIN$scope_note data --at 'viff-tutorial.pr.*.stage'                             one field of each, keyed by id
  $BIN$scope_note data viff-tutorial pr '*' --if '{stage: open}'                   by what it says
  $BIN$scope_note data viff-tutorial pr '*' --if '{stage: !or [open, merged]}'     …with an operator
  $BIN$scope_note data viff-tutorial pr '*' --if '{urgent: true}' --if-at labels   …rooted inside the payload
  $BIN$scope_note data --paths viff-tutorial pr                                    just the addresses

And watch it move (another terminal), then write to it:

  $BIN$scope_note watch --if '{stage: open}' viff-tutorial pr '*'
  $BIN$scope_note data viff-tutorial pr 2 --put '{stage: open}'

Undo it all with: $0 --clean
EOF
