#!/usr/bin/env bash
#
# lineage.sh — how one piece of work keeps its identity across three systems: a commit
# becomes a PR becomes a release. It answers a question from docs/working/questions.md —
# "how do all these IDs live in a flow?" — by running it rather than arguing it.
#
# IT RUNS AGAINST REAL SOURCES. It used to seed three invented systems (lingit, lingh, lintag)
# so it could demonstrate the shape without depending on anything; two of the three are now
# reflected by connectors that exist, so it reads what they wrote instead. What that costs is a
# setup step and what it buys is the only kind of evidence that counts — the ids are GitHub's
# and git's, chosen by neither verse nor this script, and they still line up.
#
#   verse source add --dir <this repo> git:verse         # git:commit:*, git:ref:*
#   sourced github -token $GITHUB_TOKEN -repo <org>/<name>
#   verse source add github:<name> http://localhost:9801  # github:pr:*
#
# It needs a MERGED pull request to correlate, and says so and stops if there is none: what a
# real repository contains is not a script's to arrange.
#
# The release is still this script's own entity, and that is not laziness: nothing reflects
# GitHub releases yet, and a release is the thing a person cuts — so it is the one input the
# demonstration supplies, exactly as somebody tagging would.
#
# The short answer verse gives: no fourth id is ever minted. A correlation is a DERIVED
# entity whose address is `<system>:<kind>:<rule>@<subject>` — identity is (rule, subject),
# so the link is named by what it is about. The subject is sanitised into that address (`#`
# and `/` become `_`) and a short hash keeps the sanitising from colliding two subjects, so
# the address is not constructible by string surgery: the engine writes the linkage itself at
# `verse.meta.derivedFrom`, and that is what anything looking for a trace asks. Reassembling
# the chain is then a read over the set, because a derive is about exactly one subject and
# rollups are deliberately not available.
#
# Three sharp edges it shows rather than hides, all at the end:
#   1. membership is not askable — a predicate narrows by what a trace LOOKS like, not by
#      which release it is in
#   2. the cross-system join is a LIST read, not an identity one — which is the edge the real
#      ids forced and the invented ones hid: a git source ids a commit `<repo>/<sha>` (a sha
#      is not a sound cross-repo key) while a pull request carries a bare `headSha`, and
#      `!get-path` refuses a predicate (docs/trigger-model.md)
#   3. the manifest COPIES, it does not track (docs/working/derived-views.md)
#
# And one thing running against real data forced the script itself to do: A RULE DOES NOT
# BACKFILL. The pull requests were reflected before the charter existed, and a trigger is
# level-triggered on writes, so nothing fires until GitHub changes one. There is no replay
# verb — replaying a charter over history would fire rules about states the world has left —
# so the script drops the github source's entities and re-adds it, and the source rewrites
# them. Those are real writes, and the rule sees them. It checks the source answers first.
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
		sed -n '2,50p' "$0" | sed 's/^# \{0,1\}//'
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
# Nothing to seed any more but the release, which the run cuts. The names stay `lin-` prefixed
# so a --clean removes this script's installs and nobody else's.
ENTITIES=()

if $clean; then
	step "clean"
	for t in "${TRIGGERS[@]}"; do v trigger remove "$t" >/dev/null 2>&1 || true; done
	ok "removed the rules"
	# The derived entities are the engine's, named f(rule, subject) — so they are removed by
	# the address the engine chose, not by one this script invented.
	for sys in lineage lintag; do
		for e in $(v entity --system "$sys" -o tony 2>/dev/null | sed -n 's|^[[:space:]]*ref:[[:space:]]*||p'); do
			v entity rm "$e" >/dev/null 2>&1 || true
		done
	done
	for e in "${ENTITIES[@]:-}"; do v entity rm "$e" >/dev/null 2>&1 || true; done
	ok "removed the entities — derived ones, and every release a run cut"
	note "what the sources reflected is theirs and is left alone"
	for s in "${STRATA[@]}"; do v stratum remove "$s" >/dev/null 2>&1 || true; done
	ok "removed the ordering"
	printf '\nwhat this script wrote is gone. The tasks it started are left where they are —\nthey are the record that they ran, and the next run counts them to pick its release.\n'
	exit 0
fi

# ── what the systems say, each in its own words ──────────────────────────────
# Nothing here knows about verse, and nothing shares an id scheme: a sha, a PR number, a
# semver tag. That is the premise of the question, not a simplification of it.
# THIS RUNS BEFORE THE CHARTER, deliberately: a stratum over `github` is a verse-wide ordering
# statement, and a run that is going to exit for want of data should not leave one behind.
step "two real sources, and what they have reflected"
# Nothing is seeded here. `git:commit:*` comes from a git source and `github:pr:*` from a
# GitHub one, and both were written before this script ran — which is the whole difference
# between demonstrating a shape and demonstrating that it holds.
commits=$(v entity --system git --kind commit -o tony 2>/dev/null | grep -c '^- ' || true)
prs=$(v entity --system github --kind pr -o tony 2>/dev/null | grep -c '^- ' || true)
[ "${commits:-0}" -gt 0 ] ||
	die "no git:commit:* — install a git source first: $BIN source add --dir <repo> git:<name>"
[ "${prs:-0}" -gt 0 ] ||
	die "no github:pr:* — run \`sourced github -repo <org>/<name>\` and install it: $BIN source add github:<name> http://localhost:9801"
ok "$commits commits and $prs pull request(s), reflected by sources this script did not write"

# The subject this run is about: a merged pull request. Found rather than assumed, because
# what a real repository contains is not this script's to arrange.
PR=$(v entity --system github --kind pr --if '{merged: true}' -o tony 2>/dev/null |
	sed -n 's|^[[:space:]]*ref:[[:space:]]*||p' | tr -d '"' | head -1)
if [ -z "$PR" ]; then
	printf '\n'
	note "no MERGED pull request in this verse, so there is nothing for the rule to correlate."
	note "merge one and run this again — or, to watch the shape without merging anything,"
	note "edit lin-pr-lineage's subject to { state: closed } and re-run."
	exit 0
fi
SHA=$(v entity get "$PR" -o tony | sed -n 's|^[[:space:]]*headSha:[[:space:]]*||p' | tr -d '"')
BASE=$(v entity get "$PR" -o tony | sed -n 's|^[[:space:]]*base:[[:space:]]*||p' | tr -d '"')
ok "$PR — merged into $BASE at $SHA"

# And the commit it proposed, under git's own id, which is `<repo>/<sha>` and not the sha the
# pull request carries. That mismatch is the reason the rule joins by predicate.
COMMIT=$(v entity --system git --kind commit --if "{sha: \"$SHA\"}" -o tony 2>/dev/null |
	sed -n 's|^[[:space:]]*ref:[[:space:]]*||p' | tr -d '"' | head -1)
if [ -n "$COMMIT" ]; then
	ok "the same commit, under git's id: $COMMIT"
	note "two systems, two spellings of one sha, and no third id minted to bridge them"
else
	note "the git source has not reflected $SHA (its window is a bounded tip), so the trace"
	note "will carry the pull request and an empty message — which is the join failing to"
	note "find, rather than the join being wrong"
fi

# ── the ordering, and the two rules ──────────────────────────────────────────
# Three levels, because the chain crosses them in one direction: a commit is observed, a PR
# is a change proposed about commits, and a trace is a fact derived about a change. A derive
# must land ABOVE the rule's own level and may READ only at or below it, which is exactly the
# shape of "correlate upward from what you observed".
#
# These now name REAL systems, which is a verse-wide statement rather than this script's own
# business: a stratum over `github` orders every rule anybody writes about it. That is the
# price of demonstrating on real ids instead of invented ones, and --clean takes it back.
step "the ordering, and the rules that cross it"
for t in "${TRIGGERS[@]}"; do v trigger remove "$t" >/dev/null 2>&1 || true; done
for s in "${STRATA[@]}"; do v stratum remove "$s" >/dev/null 2>&1 || true; done
v trigger add -f - >/dev/null <<'TONY' || die "the charter did not install"
- name: lin-source
  level: 0
  match: "git"
- name: lin-change
  level: 10
  match: "github"
- name: lin-trace
  level: 20
  match: "lineage"
---
# One correlation per merged PR. The derived id is NOT chosen here — the engine writes
# `lineage:trace:lin-pr-lineage@github:pr:<org>/<repo>#42`, f(rule, subject) — so re-firing
# about the same PR rewrites one address rather than accumulating duplicates. What the address
# contains is GitHub's own id, carried through — sanitised where an id may not go (`#` and `/`
# become `_`) and disambiguated with a short hash, so the sanitising cannot collide two
# subjects into one trace.
- name: lin-pr-lineage
  stratum: lin-change
  condition:
    # GitHub's own vocabulary: a pull request is `merged`, and the branch it proposed against
    # is its `base`. Matching a display string instead is what breaks when somebody renames a
    # column; there is nothing to rename here.
    subject: { system: github, kind: pr, value: { merged: true } }
  action:
    derive:
      lineage:
        trace:
          pr:      $subject
          title:   $subject.value.title
          commit:  $subject.value.headSha
          target:  $subject.value.base
          # The commit is a different system's entity, and reaching it is where the real ids
          # bite. A git source ids a commit `<repo>/<sha>` — a sha is not a sound cross-repo
          # key, since two repositories have independent histories — while a pull request
          # carries a bare `headSha`. So there is no id to name, and `!get-path` refuses a
          # predicate: the join has to be a LIST read narrowed by the sha the payload carries.
          #
          # It answers a one-element list rather than a value, which is the shape of "find the
          # thing that says this about itself" and the price of two systems that never agreed
          # on an id — which is the whole point being demonstrated.
          message: !list-paths { system: git, kind: commit, id: "*", value: {sha: $subject.value.headSha}, path: msg }
          author:  !list-paths { system: git, kind: commit, id: "*", value: {sha: $subject.value.headSha}, path: author }

# The release does not correlate anything; it READS THE SET. A derive is about exactly one
# subject, so fifty traces cannot feed one release — the level above reads them instead.
# `patch` rather than `derive` on purpose: reads inside a patch are unconstrained.
#
# The predicate is what keeps that read from being a walk of every trace in the verse, and it
# is expanded like the rest of the address — `$subject.value.target` is the release's OWN
# branch, so this is one rule rather than one rule per branch.
- name: lin-release-manifest
  condition:
    subject: { system: lintag, kind: release, value: { stage: cut } }
  action:
    patch:
      stage:   shipped
      prs:     !list-paths { system: lineage, kind: trace, id: "*", value: { target: $subject.value.target }, path: pr }
      commits: !list-paths { system: lineage, kind: trace, id: "*", value: { target: $subject.value.target }, path: commit }
      authors: !list-paths { system: lineage, kind: trace, id: "*", value: { target: $subject.value.target }, path: author }
      titles:  !list-paths { system: lineage, kind: trace, id: "*", value: { target: $subject.value.target }, path: title }
    spawn: { driver: scripted, prompt: "echo shipping $subject; sleep 30" }
TONY
ok "3 strata + 2 rules (lin-pr-lineage derives; lin-release-manifest reads the set)"

# ── the charter arrived second, and that matters ─────────────────────────────
# The edge the invented systems hid completely, because seeding wrote them after the rules
# were installed: A TRIGGER IS LEVEL-TRIGGERED ON WRITES, AND NOTHING REPLAYS. The pull
# requests above were reflected before this rule existed, so as far as the engine is
# concerned nothing has happened about them and nothing will until GitHub changes one.
#
# There is no backfill verb, and that is a position rather than a gap: replaying a charter
# over history would fire rules about states the world has already left. What there is
# instead is the source's own idempotence — drop what it wrote and it writes it again, and
# those writes are real writes, so the rule sees them. That is the same mechanism a source
# uses after any outage, used here on purpose.
step "making the reflection happen again, because rules do not backfill"
GHSRC=$(v sources -o tony 2>/dev/null | sed -n 's|^[[:space:]]*ref:[[:space:]]*\(sources:github:.*\)|\1|p' | tr -d '"' | head -1)
[ -n "$GHSRC" ] || die "no github source installed — see the setup at the top of this script"
GHURL=$(v source show "$GHSRC" -o tony | sed -n 's|^[[:space:]]*url:[[:space:]]*||p' | tr -d '"')
# Asked before anything is dropped: --forget deletes what the source wrote, and doing that to
# a source that cannot answer is how a demonstration eats somebody's data.
v source check "$GHSRC" >/dev/null 2>&1 ||
	die "$GHSRC is not answering, so nothing will be dropped — start \`sourced github\` and run again"
note "dropping and re-adding $GHSRC so it re-reflects; the entities are GitHub's and it will rewrite them"
v source remove "$GHSRC" --forget >/dev/null || die "could not remove $GHSRC"
v source add "${GHSRC#sources:}" "$GHURL" >/dev/null || die "could not re-add $GHSRC at $GHURL"
ok "re-reflected — and now the writes are in front of the rule instead of behind it"

# ── the correlation, which is a derived entity ───────────────────────────────
step "the link verse writes, and the id it gives it"
until_ok 100 "the trace never landed — is the engine running, and is the rule installed?" \
	bash -c "$BIN ${scope[*]} entities --system lineage -o tony 2>/dev/null | grep -q '$PR'"
# Found by asking each trace what it is ABOUT, not by finding the subject's id inside the
# address — because it is not in there to find. The engine sanitises what may not appear in an
# id (`#` and `/` become `_`) and appends a short hash so the sanitising cannot collide two
# subjects, which means the address is not constructible by string surgery on the subject.
# Anything that wants the trace for a subject asks for it; it does not spell it.
tref=
for t in $(v entity --system lineage -o tony | sed -n 's|^[[:space:]]*ref:[[:space:]]*||p' | tr -d '"'); do
	if v entity get "$t" -o tony | grep -qF "subject: \"$PR\""; then
		tref=$t
		break
	fi
done
[ -n "$tref" ] || die "a trace landed but none is about $PR"
ok "$tref"
v entity get "$tref"
printf '\n'
ok "one entity names a GitHub number and a git sha — and its address says which PR it is about"
note "an empty message/author list is the join finding nothing: a git source reflects a bounded"
note "tip, so a pull request older than that window has no commit here to join to"
note "the id is f(rule, subject): re-merging that PR rewrites THIS address, never a second one"

# The control: a pull request that is NOT merged. A rule keyed on the commit would have
# correlated it — several PRs can share a head sha — and the subject predicate is what says
# only merged ones have lineage. Skipped rather than faked when the repository has none.
UNMERGED=$(v entity --system github --kind pr --if '{merged: false}' -o tony 2>/dev/null |
	sed -n 's|^[[:space:]]*ref:[[:space:]]*||p' | tr -d '"' | head -1)
if [ -n "$UNMERGED" ]; then
	v entity --system lineage -o tony | grep -qF "$UNMERGED" &&
		die "an unmerged PR was correlated — the subject predicate is not selecting"
	ok "$UNMERGED is not merged, so nothing was derived about it"
else
	note "no unmerged pull request to use as the control; the selection is untested this run"
fi

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
n=$(v entity --system task -o tony 2>/dev/null | grep -c 'ref: task:instance:lin-release-manifest@' || true)
REL="lintag:release:v1.4.$n"
# The one input this script still supplies, and the honest reason: nothing reflects GitHub
# releases yet, and a release is the thing a PERSON cuts. Its `target` is the branch the
# manifest asks for, which is the base the pull requests above were merged into.
v entity put "$REL" "{stage: cut, target: \"$BASE\"}" >/dev/null
until_ok 100 "the manifest never landed" \
	bash -c "$BIN ${scope[*]} entity get '$REL' 2>/dev/null | grep -q shipped"
v entity get "$REL"
printf '\n'
ok "the release carries the PRs and the commits, each still in its own system's id scheme"

# The predicate did the narrowing, and here it is asked of real data: a trace whose target is
# some other branch must not be in a release cut from this one. That is a different exclusion
# from the control above — one is the read's predicate, the other is the rule's subject.
# Found by asking each trace what it targets, rather than by parsing a listing: `--if` has no
# not-equal, and the negative is the whole point of the check.
OTHER=
for t in $(v entity --system lineage -o tony | sed -n 's|^[[:space:]]*ref:[[:space:]]*||p' | tr -d '"'); do
	tgt=$(v entity get "$t" -o tony | sed -n 's|^[[:space:]]*target:[[:space:]]*||p' | tr -d '"')
	if [ -n "$tgt" ] && [ "$tgt" != "$BASE" ]; then
		OTHER=$t
		break
	fi
done
if [ -n "$OTHER" ]; then
	v entity get "$REL" | grep -qF "$OTHER" &&
		die "a trace targeting another branch is in a release cut from $BASE — the predicate did not narrow"
	ok "a trace targeting another branch is absent: this release asked for $BASE"
else
	note "every trace targets $BASE, so the read's predicate is untested this run"
fi

# ── the flow ─────────────────────────────────────────────────────────────────
# The literal question was how these ids live in a FLOW, and the answer is that a flow is
# keyed by ONE of them. Correlation is what lets that one reach the rest.
step "the flow, which is keyed by exactly one of them"
until_ok 150 "no flow appeared for $REL" \
	bash -c "$BIN ${scope[*]} flows 2>/dev/null | grep -q '$REL'"
v flow
ok "the flow's subject is $REL — one id, not a merged one"
note "and $tref is about $PR without spelling it: the address is sanitised and hashed"
note "the task can reach the others because the manifest is on its subject: \`verse task get\`"

# ── what this does not do ────────────────────────────────────────────────────
step "three edges, stated rather than hidden"
cat <<'EDGES'
  1. A PREDICATE NARROWS BY WHAT A TRACE LOOKS LIKE — NOT BY WHICH RELEASE IT IS IN.
     The manifest asks for traces whose `target` is this release's own, which is why a
     trace onto another branch is absent. What it cannot ask is "the ones in THIS
     release": nothing on a trace says so, and the trace's address is the engine's,
     f(rule, subject), so there is nowhere to put it. Membership would have to be a
     fact somebody writes — the release patching each trace, or a second correlation
     keyed on the pair — and a derive is about exactly one subject, so it is not the
     derive that would do it. Pairwise correlation, and a predicate over the set;
     still nothing in between.

  2. THE COMMIT JOIN IS A LIST READ, not an identity one, because the two systems never
     agreed on an id: git spells a commit `<repo>/<sha>` and a pull request carries a
     bare `headSha`. `!get-path` refuses a predicate, so the rule narrows `git:commit:*`
     by the `sha` its payload carries and takes the one element back. That is what a
     cross-system join costs when nobody minted a shared key — and paying it here, in
     the open, is the alternative to minting a fourth id nobody asked for.

  3. THE MANIFEST COPIES; IT DOES NOT TRACK. Merge another PR onto main now and this
     release does not change — the read happened at the commit that fired the rule.
     That is by design (trigger/path.go), and it is also what makes the predicate safe
     to have: it is evaluated against the one snapshot the whole firing reads, not
     against whatever the store reaches later. The proposed `!view.*` spelling for the
     tracking half is in docs/working/derived-views.md.
EDGES

step "poke at it"
cat <<EOF
  $BIN entities --system lineage           every correlation verse has written
  $BIN entity get $tref
  $BIN entity get $REL    the reassembled chain
  $BIN flows                               the flow, keyed by one id
  ./scripts/lineage.sh --clean             put it all back
EOF
