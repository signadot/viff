#!/usr/bin/env bash
#
# lineage.sh — how one piece of work keeps its identity across three systems: a commit
# becomes a PR becomes a release. It answers a question from docs/working/questions.md —
# "how do all these IDs live in a flow?" — by running it rather than arguing it.
#
# The short answer verse gives: no fourth id is ever minted. A correlation is a DERIVED
# entity whose address is `<system>:<kind>:<rule>@<subject>` — identity is (rule, subject),
# so the link is named by what it is about. The engine writes the subject linkage itself at
# `verse.meta.derivedFrom`. Reassembling the chain is then a read over the set, because a
# derive is about exactly one subject and rollups are deliberately not available.
#
# Two sharp edges it shows rather than hides, both at the end:
#   1. a path read selects by IDENTITY only — it may not narrow by payload
#   2. the manifest COPIES, it does not track (docs/working/derived-views.md)
#
# Like seed-demo.sh — and unlike smoke.sh — this talks to YOUR verse and leaves what it
# writes behind for you to poke at.
#
#   ./verse up                        # in another terminal
#   ./scripts/lineage.sh
#   ./scripts/lineage.sh --scope me   # …into a copy-on-write scope instead of baseline
#   ./scripts/lineage.sh --clean      # remove what a previous run wrote
#
# Env: VERSE (the binary, default: ./verse then `verse` on PATH), VERSE_ADDR (the verse to
# talk to). Both are the CLI's own knobs; nothing here is a second configuration.
set -euo pipefail

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
		sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		printf 'lineage: unknown argument %q (try --help)\n' "$1" >&2
		exit 2
		;;
	esac
	shift
done

step() { printf '\n\033[1m# %s\033[0m\n' "$*"; }
ok() { printf '  \033[32m✓\033[0m %s\n' "$*"; }
note() { printf '  \033[33m~\033[0m %s\n' "$*"; }
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
	die "no verse answering at ${VERSE_ADDR:-its default address} — start one with \`$BIN up\` (or pass VERSE_ADDR=host:port)"

# Waits for a condition rather than sleeping a guessed interval: the engine is reacting, and
# how long a reaction takes is not this script's to assert.
until_ok() {
	local tries=$1 label=$2
	shift 2
	local i
	for i in $(seq 1 "$tries"); do
		if "$@" >/dev/null 2>&1; then return 0; fi
		sleep 0.2
	done
	die "$label"
}
has() { case "$1" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

TRIGGERS=(lin-pr-lineage lin-release-manifest)
STRATA=(lin-source lin-change lin-trace)
ENTITIES=(
	lingit:commit:a1b2c3 lingit:commit:d4e5f6
	lingh:pr:42 lingh:pr:43 lingh:pr:44
)

if $clean; then
	step "clean"
	for t in "${TRIGGERS[@]}"; do v trigger remove "$t" >/dev/null 2>&1 || true; done
	ok "removed the rules"
	# The derived entities are the engine's, named f(rule, subject) — so they are removed by
	# the address the engine chose, not by one this script invented.
	for sys in lineage lintag; do
		for e in $(v entities --system "$sys" -o tony 2>/dev/null | sed -n 's|^[[:space:]]*ref:[[:space:]]*||p'); do
			v entity rm "$e" >/dev/null 2>&1 || true
		done
	done
	for e in "${ENTITIES[@]}"; do v entity rm "$e" >/dev/null 2>&1 || true; done
	ok "removed the entities — derived ones, and every release a run cut"
	for s in "${STRATA[@]}"; do v stratum remove "$s" >/dev/null 2>&1 || true; done
	ok "removed the ordering"
	printf '\nwhat this script wrote is gone. The tasks it started are left where they are —\nthey are the record that they ran, and the next run counts them to pick its release.\n'
	exit 0
fi

# ── the ordering, and the two rules ──────────────────────────────────────────
# Three levels, because the chain crosses them in one direction: a commit is observed, a PR
# is a change proposed about commits, and a trace is a fact derived about a change. A derive
# must land ABOVE the rule's own level and may READ only at or below it, which is exactly the
# shape of "correlate upward from what you observed".
step "the ordering, and the rules that cross it"
for t in "${TRIGGERS[@]}"; do v trigger remove "$t" >/dev/null 2>&1 || true; done
for s in "${STRATA[@]}"; do v stratum remove "$s" >/dev/null 2>&1 || true; done
v trigger add -f - >/dev/null <<'TONY' || die "the charter did not install"
- name: lin-source
  level: 0
  match: "lingit"
- name: lin-change
  level: 10
  match: "lingh"
- name: lin-trace
  level: 20
  match: "lineage"
---
# One correlation per merged PR. The derived id is NOT chosen here — the engine writes
# `lineage:trace:lin-pr-lineage@lingh:pr:42`, f(rule, subject) — so re-firing about the same
# PR rewrites one address rather than accumulating duplicates.
- name: lin-pr-lineage
  stratum: lin-change
  condition:
    subject: { system: lingh, kind: pr, value: { stage: merged } }
  action:
    derive:
      lineage:
        trace:
          pr:      $subject
          commit:  $subject.value.head
          # The commit is a different system's entity, reached by identity. This is the copy
          # that makes the trace answer on its own, without a join at read time.
          message: !get-path { system: lingit, kind: commit, id: $subject.value.head, path: message }
          author:  !get-path { system: lingit, kind: commit, id: $subject.value.head, path: author }

# The release does not correlate anything; it READS THE SET. A derive is about exactly one
# subject, so fifty traces cannot feed one release — the level above reads them instead.
# `patch` rather than `derive` on purpose: reads inside a patch are unconstrained.
- name: lin-release-manifest
  condition:
    subject: { system: lintag, kind: release, value: { stage: cut } }
  action:
    patch:
      stage:   shipped
      prs:     !list-paths { system: lineage, kind: trace, id: "*", path: pr }
      commits: !list-paths { system: lineage, kind: trace, id: "*", path: commit }
      authors: !list-paths { system: lineage, kind: trace, id: "*", path: author }
    spawn: { driver: scripted, prompt: "echo shipping $subject; sleep 30" }
TONY
ok "3 strata + 2 rules (lin-pr-lineage derives; lin-release-manifest reads the set)"

# ── what the systems say, each in its own words ──────────────────────────────
# Nothing here knows about verse, and nothing shares an id scheme: a sha, a PR number, a
# semver tag. That is the premise of the question, not a simplification of it.
step "three systems, three id schemes, no coordination"
v entity put lingit:commit:a1b2c3 '{message: "add the token TTL", author: ana}' >/dev/null
v entity put lingit:commit:d4e5f6 '{message: "fix the docket label", author: bo}' >/dev/null
ok "lingit:commit:a1b2c3, lingit:commit:d4e5f6 — a sha is the id"

v entity put lingh:pr:42 '{stage: merged, head: a1b2c3, title: "token TTL"}' >/dev/null
v entity put lingh:pr:43 '{stage: merged, head: d4e5f6, title: "docket label"}' >/dev/null
v entity put lingh:pr:44 '{stage: open,   head: a1b2c3, title: "still in review"}' >/dev/null
ok "lingh:pr:42, :43 merged and :44 open — a number is the id"

# ── the correlation, which is a derived entity ───────────────────────────────
step "the link verse writes, and the id it gives it"
until_ok 100 "the trace never landed — is the engine running?" \
	bash -c "$BIN ${scope[*]} entities --system lineage -o tony 2>/dev/null | grep -q 'lingh:pr:42'"
tref=$(v entities --system lineage -o tony | sed -n 's|^[[:space:]]*ref:[[:space:]]*||p' | grep 'pr:42' | head -1)
ok "$tref"
v entity get "$tref"
printf '\n'
ok "one entity names both ids — and the address says which PR it is about"
note "the id is f(rule, subject): re-merging pr:42 rewrites THIS address, never a second one"

# :44 is the control. It shares a commit with :42, so a rule keyed on the commit would have
# correlated it; the selection is what says only merged PRs have lineage.
v entities --system lineage -o tony | grep -q 'pr:44' &&
	die "an open PR was correlated — the subject predicate is not selecting"
ok "lingh:pr:44 is open, so nothing was derived about it (it shares a1b2c3 with :42)"

# ── the release, which reassembles the chain by reading the set ──────────────
step "the release: three ids reassembled without a fourth"
# EACH RUN CUTS A NEW RELEASE, and that is semantics rather than scripting convenience: a
# task's id is f(rule, subject) exactly like the derived entity above, so re-cutting one
# release addresses the task that already ran and the engine returns it instead of starting a
# second (docs/working/spawn-repeat-policy.md). A new release is a new subject, so it is a new
# task and a new flow — which is also what releases do in life.
# Counted from the TASKS that have run, not the releases in the store: a --clean removes
# the releases but the engine still holds the task each one started, and a subject whose
# task it already has is the case above. Counting what actually blocks keeps this correct
# however many times the script is run or cleaned.
n=$(v entities --system task -o tony 2>/dev/null | grep -c 'ref: task:instance:lin-release-manifest@' || true)
REL="lintag:release:v1.4.$n"
v entity put "$REL" '{stage: cut}' >/dev/null
until_ok 100 "the manifest never landed" \
	bash -c "$BIN ${scope[*]} entity get '$REL' 2>/dev/null | grep -q shipped"
v entity get "$REL"
printf '\n'
ok "the release carries the PRs and the commits, each still in its own system's id scheme"

# ── the flow ─────────────────────────────────────────────────────────────────
# The literal question was how these ids live in a FLOW, and the answer is that a flow is
# keyed by ONE of them. Correlation is what lets that one reach the rest.
step "the flow, which is keyed by exactly one of them"
until_ok 150 "no flow appeared for $REL" \
	bash -c "$BIN ${scope[*]} flows 2>/dev/null | grep -q '$REL'"
v flows
ok "the flow's subject is $REL — one id, not a merged one"
note "the task can reach the others because the manifest is on its subject: \`verse task get\`"

# ── what this does not do ────────────────────────────────────────────────────
step "two edges, stated rather than hidden"
cat <<'EDGES'
  1. A PATH READ SELECTS BY IDENTITY, NEVER BY PAYLOAD. The manifest above globs
     `id: "*"`, so it collects every trace in the verse — not the ones in this release.
     Narrowing it is refused at install, on purpose:

       !list-paths { system: lineage, kind: trace, id: "*", value: "..." }
       → "a path read addresses entities by identity. Selecting by what they LOOK
          like is a different question, and one whose answer changes as rules fire"

     So scoping a manifest to a release wants the scope to be in the ADDRESS — and the
     derived address is the engine's, f(rule, subject). That is the open corner: this
     correlates pairwise and rolls up globally, with nothing in between.

  2. THE MANIFEST COPIES; IT DOES NOT TRACK. Merge another PR now and v1.4.0 does not
     change — the read happened at the commit that fired the rule. That is by design
     (trigger/path.go), and the proposed `!view.*` spelling for the other half is in
     docs/working/derived-views.md.
EDGES

step "poke at it"
cat <<EOF
  $BIN entities --system lineage           every correlation verse has written
  $BIN entity get $tref
  $BIN entity get $REL    the reassembled chain
  $BIN flows                               the flow, keyed by one id
  ./scripts/lineage.sh --clean             put it all back
EOF
