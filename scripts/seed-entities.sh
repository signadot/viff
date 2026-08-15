#!/usr/bin/env bash
#
# seed-entities.sh — put a WORLD into a running verse: a dozen entities across two systems,
# with enough variety in their payloads that selecting over them says something.
#
# No charter, deliberately. This is the half of verse you can hold before any rule exists:
# entities are just addressed documents, and `--if` / `--if-at` select over what they say.
# Tutorial §2 walks these; `charter-shapes.sh` is the half that reacts to them.
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
v() { "$BIN" "${scope[@]}" "$@"; }

v status >/dev/null 2>&1 ||
	die "no verse answering at ${VERSE_ADDR:-its default address} — start one with \`$BIN up\`"

# ── the world ────────────────────────────────────────────────────────────────
# Written as ref + payload pairs so --clean is the same list read for its refs, and so the
# variety is visible in one place: two systems, three kinds, and payloads that differ in
# stage, in owner, and in a nested `labels` object (which is what --if-at is for).
WORLD=(
	'demo:pr:1|{stage: open,   owner: ana, title: "add the token TTL",   labels: {urgent: true,  area: auth}}'
	'demo:pr:2|{stage: draft,  owner: bo,  title: "rename the docket",   labels: {urgent: false, area: docs}}'
	'demo:pr:3|{stage: open,   owner: ana, title: "cache the schema",    labels: {urgent: true,  area: store}}'
	'demo:pr:4|{stage: merged, owner: cy,  title: "bump go-tony",        labels: {urgent: false, area: build}}'
	'demo:pr:5|{stage: open,   owner: cy,  title: "drop the flow field", labels: {area: engine}}'
	'demo:task:hotfix-1|{priority: high, owner: ana, title: "expiring cert"}'
	'demo:task:hotfix-2|{priority: high, owner: bo,  title: "docd skew"}'
	'demo:task:cleanup-1|{priority: low, owner: cy,  title: "tidy the fixtures"}'
	'ci:status:build|{state: green, commit: a1b2c3d}'
	'ci:status:e2e|{state: red, commit: a1b2c3d, failed: [auth, docket]}'
	'human:person:ana|{team: platform, reviews: [auth, store]}'
	'human:person:bo|{team: docs, reviews: [docs]}'
)

if $clean; then
	step "clean"
	for w in "${WORLD[@]}"; do v state rm "${w%%|*}" >/dev/null 2>&1 || true; done
	ok "removed ${#WORLD[@]} seeded entities"
	exit 0
fi

# A world is only inert if nothing is watching it. `seed-demo.sh` installs rules over
# demo:pr and demo:task — exactly what this seeds — so say so rather than let the reader
# wonder why an entity they just wrote is already at a different stage.
if [ -n "$(v trigger -o tony 2>/dev/null | grep -v '^\[\]$')" ]; then
	printf '\n  \033[33m~\033[0m a charter is installed, so some of this will be reacted to at once.\n'
	printf '    For a world nothing is watching: %s trigger remove <name> (or ./scripts/seed-demo.sh --clean)\n' "$BIN"
fi

step "seeding ${#WORLD[@]} entities"
for w in "${WORLD[@]}"; do
	v state put "${w%%|*}" "${w#*|}" >/dev/null || die "put ${w%%|*} failed"
done
ok "demo:pr:1–5        three open, one draft, one merged; nested labels"
ok "demo:task:*        two high-priority, one low"
ok "ci:status:*        build green, e2e red"
ok "human:person:*     two reviewers"

scope_note=""
[ ${#scope[@]} -gt 0 ] && scope_note=" ${scope[*]}"
cat <<EOF

seeded. Now select over it:

  $BIN$scope_note state                                     the whole verse, one document
  $BIN$scope_note state demo.pr                             by where it lives
  $BIN$scope_note state demo.pr.1                           one of them
  $BIN$scope_note state 'demo.pr.*.stage'                   one field of each, keyed by id
  $BIN$scope_note state --if '{stage: open}'                by what it says
  $BIN$scope_note state --if '{stage: !or [open, merged]}'      …with an operator
  $BIN$scope_note state --if '{urgent: true}' --if-at labels     …rooted inside the payload
  $BIN$scope_note state --paths demo.pr                     just the addresses

And watch it move (another terminal), then write to it:

  $BIN$scope_note watch --if '{stage: open}' demo
  $BIN$scope_note state put demo:pr:2 '{stage: open}'

Undo it all with: $0 --clean
EOF
