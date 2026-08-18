#!/usr/bin/env bash
#
# seed-demo.sh — put a small demo world into a RUNNING verse: a charter, the entities it
# reacts to, and a provider registry, so every listing has something to show and there is
# a live gate waiting for you the moment it finishes.
#
# It is the missing first five minutes. `verse up` gives you an empty verse, and an empty
# verse looks the same as a broken one: `verse entity`, `verse charter` and `verse gate`
# all answer with nothing. This seeds enough that they don't, and prints what to type next.
#
# Unlike smoke.sh — which builds its own binaries and runs its own isolated server — this
# talks to YOUR verse, and leaves what it writes behind for you to play with.
#
#   ./verse up                       # in another terminal (or: make seed-demo)
#   ./scripts/seed-demo.sh
#   ./scripts/seed-demo.sh --scope me        # …into a copy-on-write scope instead of baseline
#   ./scripts/seed-demo.sh --clean           # remove what a previous run seeded
#
# Env: VERSE (the binary, default: ./verse then `verse` on PATH), VERSE_ADDR (the verse to
# seed, default: verse's own default). Both are the CLI's own knobs; nothing here is a
# second configuration.
set -euo pipefail

# The scripts live in scripts/ and act on the checkout above them: the `./verse` a
# build produces is at the root, and so is the reader's cwd.
cd "$(cd "$(dirname "$0")/.." && pwd)"

clean=false
scope=()
while [ $# -gt 0 ]; do
	case "$1" in
	--clean) clean=true ;;
	--scope) scope=(--scope "$2"); shift ;;
	--scope=*) scope=(--scope "${1#*=}") ;;
	-h | --help)
		sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		printf 'seed-demo: unknown argument %q (try --help)\n' "$1" >&2
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

# The binary, in the order you are likely to have one: an explicit VERSE, the build a
# checkout produces, then PATH.
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
if ! printf '%s\n' "$_verse_cmds" | grep -qE '^ *state '; then
	die "$BIN does not have \`verse state\` — it predates the one-document read.
     Rebuild it (go build -o verse ./cmd/verse), remove a stale ./verse so \$PATH wins,
     or point VERSE=<path> at a current one."
fi
v() { "$BIN" "${scope[@]}" "$@"; }

# Talk to it before writing to it: every failure below would otherwise be one "connection
# refused" per command, which reads as a broken script rather than a verse that isn't up.
v status >/dev/null 2>&1 ||
	die "no verse answering at ${VERSE_ADDR:-its default address} — start one with \`$BIN up\` (or pass VERSE_ADDR=host:port)"

TRIGGERS=(demo-review demo-escalate)
ENTITIES=(
	demo:pr:1 demo:pr:2
	demo:task:hotfix-1 demo:task:cleanup-1
	provider:review:claude provider:review:cheapo
)

if $clean; then
	step "clean"
	for t in "${TRIGGERS[@]}"; do v charter rm rules "$t" >/dev/null 2>&1 || true; done
	ok "removed the seeded triggers"
	for e in "${ENTITIES[@]}"; do v state rm "$e" >/dev/null 2>&1 || true; done
	ok "removed the seeded entities"
	printf '\nseeded state is gone. Live tasks and answered gates are not touched.\n'
	exit 0
fi

# ── the charter ──────────────────────────────────────────────────────────────
# Two rules, because one rule cannot show both halves of the dial: `demo-escalate` is
# `auto` and has already fired by the time this script exits, and `demo-review` is `ask`
# and is still waiting for you. Removing first makes a re-run idempotent — `charter add`
# refuses a duplicate name, which is right, and is not what a seed script should trip over.
step "charter"
for t in "${TRIGGERS[@]}"; do v charter rm rules "$t" >/dev/null 2>&1 || true; done
v charter add -f - >/dev/null <<'TONY' || die "charter add failed"
- name: demo-review
  condition:
    subject: { system: demo, kind: pr, value: { stage: open } }
  gates:
  - { name: approval, mode: ask, onDenied: { patch: { stage: rejected } } }
  action:
    patch: { stage: reviewing }
    spawn: { driver: scripted, vector: auto, prompt: "echo reviewing $subject; sleep 45" }
    onDone:
      patch: { stage: reviewed }

- name: demo-escalate
  condition:
    subject: { system: demo, kind: task, value: { priority: high } }
  action:
    patch: { stage: escalated }
TONY
ok "demo-review (ask: a human approves) + demo-escalate (auto)"

# ── the world the charter is about ───────────────────────────────────────────
step "entities"
v state put demo:pr:1 '{stage: open, title: "add the token TTL", owner: ana}' >/dev/null
v state put demo:pr:2 '{stage: draft, title: "rename the docket", owner: bo}' >/dev/null
ok "demo:pr:1 (open — demo-review fires on it), demo:pr:2 (draft — it does not)"
v state put demo:task:hotfix-1 '{priority: high, title: "expiring cert"}' >/dev/null
v state put demo:task:cleanup-1 '{priority: low, title: "tidy the fixtures"}' >/dev/null
ok "demo:task:hotfix-1 (high — demo-escalate fires), demo:task:cleanup-1 (low — it does not)"

# Providers are ordinary entities, which is the point: `verse provider` is a view of the
# store, and a spawn that SELECTS its doer picks from these by capability and attributes.
v state put provider:review:claude '{driver: llm, tier: premium}' >/dev/null
v state put provider:review:cheapo '{driver: scripted, tier: basic}' >/dev/null
ok "two review providers (premium + basic) in the registry"

# ── what the seed produced ───────────────────────────────────────────────────
# The engine reacts, so both of these are races against it rather than reads of a settled
# world: poll briefly instead of sleeping a fixed amount and hoping.
step "the engine reacts"
escalated=
for _ in $(seq 1 50); do
	if v state get demo:task:hotfix-1 2>/dev/null | grep -q escalated; then
		escalated=1
		break
	fi
	sleep 0.2
done
[ -n "$escalated" ] && ok "demo-escalate advanced demo:task:hotfix-1 → escalated (auto: nobody was asked)" ||
	printf '  \033[33m~\033[0m demo:task:hotfix-1 has not advanced yet — `verse state get demo:task:hotfix-1`\n'

GID=
for _ in $(seq 1 50); do
	GID=$(v gate -o tony | sed -n 's/^[[:space:]]*id:[[:space:]]*//p' | head -1)
	[ -n "$GID" ] && break
	sleep 0.2
done
[ -n "$GID" ] && ok "demo-review is holding demo:pr:1 at a gate: $GID" ||
	printf '  \033[33m~\033[0m no gate yet — `verse gate` in a moment\n'

scope_note=""
[ ${#scope[@]} -gt 0 ] && scope_note=" ${scope[*]}"
cat <<EOF

seeded. What to type next:

  $BIN$scope_note state                        what is there (also: state demo)
  $BIN$scope_note charter                      the charter you just installed
  $BIN$scope_note gates                        the decision demo-review is holding
  $BIN$scope_note gate approve ${GID:-<id>} --as you
                                        …and it spawns a 45s review task:
  $BIN$scope_note flows                        the subjects with live work
  $BIN$scope_note flow show demo:pr:1          what is happening to this one, and what comes next
  $BIN$scope_note tasks                    the review task itself

Watch it move (another terminal, before approving):

  $BIN$scope_note watch demo

Then drive it yourself — a put is how you speak to the verse:

  $BIN$scope_note state put demo pr 2 '{stage: open}'     open the draft; demo-review fires again

Undo it all with: $0 --clean
EOF
