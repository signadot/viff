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
# THE SYSTEMS ARE SEEDED, AND THE VOCABULARY IS NOT. It writes its own `git:commit:*` and
# `github:pr:*` fixtures, so it runs against a bare verse with nothing installed — but the ids
# and the field names are the ones the shipped connectors actually write (source/git.go,
# source/github). A demo that invents both sides of a join can make any join look easy; using
# the real spellings is what keeps it honest without making it need a token and a repository.
#
# Three sharp edges it shows rather than hides, all at the end:
#   1. membership is not askable — a predicate narrows by what a trace LOOKS like, not by
#      which release it is in
#   2. the cross-system join is a LIST read, not an identity one — git ids a commit
#      `<repo>/<sha>` and a pull request carries a bare `headSha`, and `!get-path` refuses a
#      predicate, so there is no identity read that reaches it
#   3. the manifest COPIES, it does not track (docs/working/derived-views.md)
#
# A fourth is real and NOT demonstrable here, because seeding hides it: a rule does not
# backfill. Entities written before a charter was installed never fire it, and there is no
# replay verb. This script always installs its rules first, so it never meets that; running it
# against a repository already reflected does, and docs/working/gh-source-residual.md records it.
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
		sed -n '2,38p' "$0" | sed 's/^# \{0,1\}//'
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

# Real-shaped shas, because the join is by sha and a three-character stand-in would hide that
# the id carrying it has a repo glued on the front.
A=a1b2c3d4e5f60718293a4b5c6d7e8f9012345678
B=d4e5f60718293a4b5c6d7e8f901234567890abcd
C=90abcdef1234567890abcdef1234567890abcdef

# The subject this run is about, spelled once. It is a merged pull request in the seed below,
# and everything that asks about the correlation asks about this.
PR="github:pr:acme/widget#42"

TRIGGERS=(lin-pr-lineage lin-release-manifest)
STRATA=(lin-source lin-change lin-trace)
ENTITIES=(
	"git:commit:widget/$A" "git:commit:widget/$B" "git:commit:widget/$C"
	"github:pr:acme/widget#42" "github:pr:acme/widget#43"
	"github:pr:acme/widget#44" "github:pr:acme/widget#45"
)

if $clean; then
	step "clean"
	for t in "${TRIGGERS[@]}"; do v charter -r rules "$t" >/dev/null 2>&1 || true; done
	ok "removed the rules"
	# The derived entities are the engine's, named f(rule, subject) — so they are removed by
	# the address the engine chose, not by one this script invented.
	# `--paths` answers in steps, one address per LINE — so it is read a line at a time and
	# handed over whole. Splitting on whitespace would tear each address into three words.
	for sys in lineage lintag; do
		while IFS= read -r e; do
			[ -n "$e" ] && v data --ref "$e" --rm >/dev/null 2>&1 || true
		done < <(v data --paths "$sys" 2>/dev/null)
	done
	# --ref: these are refs, and a positional is a step.
	for e in "${ENTITIES[@]}"; do v data --ref "$e" --rm >/dev/null 2>&1 || true; done
	ok "removed the entities — derived ones, and every release a run cut"
	for s in "${STRATA[@]}"; do v charter remove "$s" >/dev/null 2>&1 || true; done
	ok "removed the ordering"
	printf '\nwhat this script wrote is gone. The tasks it started are left where they are —\nthey are the record that they ran, and removing one would not make the engine forget\nit anyway. The next run keys its release on the store revision, so it does not care.\n'
	exit 0
fi

# ── nobody else's territory ──────────────────────────────────────────────────
# The fixtures go into `git:*` and `github:*` because those are the shipped connectors'
# systems and using anything else would teach a vocabulary that does not exist. The cost is
# that a real source of either kind is the SINGLE WRITER of that slice, and its converge
# tombstones what it covers and does not find — so these fixtures would quietly disappear
# mid-run, and the failure would look like the engine rather than like this.
#
# Ownership is proved when a source is INSTALLED, against the other sources, and an entity
# put is not checked against it at all. So nothing stops this; the check has to be here.
step "checking nobody owns what this is about to write"
# grep -E rather than a sed alternation: `\|` inside `\(...\)` is a GNU extension that BSD sed
# accepts and ignores, so on macOS the guard silently matched nothing and let the run proceed.
owned=$(v sources -o tony 2>/dev/null |
	grep -E '^[[:space:]]*- ref:[[:space:]]*"?(git|github):' |
	sed -E 's|.*ref:[[:space:]]*"?([a-z]+):.*|\1|' | sort -u | tr '\n' ' ' || true)
if [ -n "${owned// /}" ] && [ ${#scope[@]} -eq 0 ]; then
	printf '\n'
	note "a source already claims: $owned"
	note "its converge tombstones what it covers and does not find, so these fixtures would"
	note "vanish under it. Remove it for the demo, or run this in a scope of its own:"
	die "$0 --scope demo   (then $0 --scope demo --clean)"
elif [ -n "${owned// /}" ]; then
	# A scope is a copy-on-write overlay, and a source reflects into BASELINE — so the
	# fixtures are somewhere its converge does not reach. Checked rather than assumed: a
	# scoped fixture in the git slice survives a converge of a real git source, and does not
	# appear in baseline at all.
	ok "a source claims $owned, but this run is scoped — its converge does not reach here"
else
	ok "no source claims git:* or github:*, so the fixtures are this script's to write"
fi

# ── the ordering, and the two rules ──────────────────────────────────────────
# Three levels, because the chain crosses them in one direction: a commit is observed, a PR
# is a change proposed about commits, and a trace is a fact derived about a change. A derive
# must land ABOVE the rule's own level and may READ only at or below it, which is exactly the
# shape of "correlate upward from what you observed".
#
# These name REAL systems, which is a verse-wide statement rather than this script's own
# business: a stratum over `github` orders every rule anybody writes about it. That is the
# price of the shipped vocabulary, it is the same price a real deployment pays, and --clean
# takes it back.
step "the ordering, and the rules that cross it"
for t in "${TRIGGERS[@]}"; do v charter -r rules "$t" >/dev/null 2>&1 || true; done
for s in "${STRATA[@]}"; do v charter remove "$s" >/dev/null 2>&1 || true; done
v charter -a -f - >/dev/null <<'TONY' || die "the charter did not install"
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
# `lineage:trace:lin-pr-lineage@github:pr:acme_widget_42+<hash>`, f(rule, subject) — so
# re-firing about the same PR rewrites one address rather than accumulating duplicates.
# The subject is CARRIED into that address, not concatenated into it: `#` and `/` may not
# appear in an id and become `_`, and a short hash keeps the sanitising from collapsing two
# subjects into one trace. Nothing can spell a trace's address from its subject, which is why
# the engine writes the linkage at `verse.meta.derivedFrom` and this script reads it back.
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
          # The commit is a different system's entity, and reaching it is where two real id
          # schemes bite. git ids a commit `<repo>/<sha>` — a sha is not a sound cross-repo
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
    spawn: { driver: scripted, vector: auto, prompt: "echo shipping $subject; sleep 30" }
TONY
ok "3 strata + 2 rules (lin-pr-lineage derives; lin-release-manifest reads the set)"

# ── what the systems say, each in its own words ──────────────────────────────
# Nothing here knows about verse, and nothing shares an id scheme: a sha, a PR number, a
# semver tag. That is the premise of the question, not a simplification of it.
step "two systems, two id schemes, no coordination"
# The ids and fields are the shipped connectors', not this script's inventions. A git source
# writes `git:commit:<repo>/<sha>` with `sha`/`msg`/`author`/`repo`; a github source writes
# `github:pr:<org>/<repo>#<n>` with `merged`/`headSha`/`base`/`title`. Both spellings are
# copied from what those sources produce, so a charter written against this demo is a charter
# that works — which is the whole reason not to invent a vocabulary here.
v data --ref "git:commit:widget/$A" --put "{sha: $A, msg: \"add the token TTL\", author: ana, repo: widget}" >/dev/null
v data --ref "git:commit:widget/$B" --put "{sha: $B, msg: \"fix the docket label\", author: bo, repo: widget}" >/dev/null
v data --ref "git:commit:widget/$C" --put "{sha: $C, msg: \"widen the gate vector\", author: cy, repo: widget}" >/dev/null
ok "three commits — git ids them <repo>/<sha>, because a sha alone is not a cross-repo key"

v data --ref "$PR" --put "{merged: true,  headSha: $A, base: main, title: \"token TTL\", number: 42, repo: acme/widget, state: closed}" >/dev/null
v data --ref "github:pr:acme/widget#45" --put "{merged: true,  headSha: $C, base: main, title: \"gate vector\", number: 45, repo: acme/widget, state: closed}" >/dev/null
v data --ref "github:pr:acme/widget#43" --put "{merged: true,  headSha: $B, base: next, title: \"docket label\", number: 43, repo: acme/widget, state: closed}" >/dev/null
v data --ref "github:pr:acme/widget#44" --put "{merged: false, headSha: $A, base: main, title: \"still in review\", number: 44, repo: acme/widget, state: open}" >/dev/null
ok "four PRs — GitHub ids them <org>/<repo>#<n>; :42 and :45 onto main, :43 onto next, :44 unmerged"
note "a pull request carries a BARE headSha, and the commit's id has a repo in front of it"

# ── the correlation, which is a derived entity ───────────────────────────────
step "the link verse writes, and the id it gives it"
# ASKED AS ONE QUESTION, retried — not "wait for the id to appear somewhere" and then a
# separate lookup, which is two questions with a gap in the middle and was flaky exactly
# there: the wait passed on a trace from an earlier run while this run's rewrite was in
# flight, and the lookup that followed found nothing.
#
# And it asks what a trace is ABOUT rather than looking for the subject inside the address,
# because it is not in there to find: `#` and `/` are sanitised to `_` and a short hash is
# appended so the sanitising cannot collapse two subjects. Nothing can spell a trace's
# address from its subject; the engine writes the linkage at `verse.meta.derivedFrom`.
tref=
for _ in $(seq 1 100); do
	while IFS= read -r t; do
		[ -n "$t" ] || continue
		# UNQUOTED: `--paths` answers in steps, so the shell splits the line into arguments,
		# which is what the address grammar takes. (A segment holding whitespace would not
		# survive this; none of these ids do. `xargs -L1` is the form that does.)
		# shellcheck disable=SC2086
		if v data $t -o tony 2>/dev/null | grep -qF "subject: \"$PR\""; then
			tref=$t
			break 2
		fi
	done < <(v data --paths lineage 2>/dev/null)
	sleep 0.2
done
[ -n "$tref" ] ||
	die "no trace about $PR — is the engine running, and did lin-pr-lineage install?"
ok "$tref"
# shellcheck disable=SC2086
v data $tref
printf '\n'
ok "one entity names a GitHub number and a git sha, each still in its own system's spelling"
note "the message and author came from git by a PREDICATE on sha, not by naming an id: the"
note "commit's id is widget/$A and the pull request only ever knew the bare sha"
note "the trace's id is f(rule, subject) — re-merging #42 rewrites THIS address, never a second"

# #44 is the control. It shares a headSha with #42, so a rule keyed on the commit would have
# correlated it; the subject predicate is what says only merged PRs have lineage.
v -o tony state lineage | grep -qF 'github:pr:acme/widget#44' &&
	die "an unmerged PR was correlated — the subject predicate is not selecting"
ok "#44 is unmerged, so nothing was derived about it (it shares its headSha with #42)"

# ── the release, which reassembles the chain by reading the set ──────────────
step "the release: three ids reassembled without a fourth"
# EACH RUN CUTS A NEW RELEASE, and that is semantics rather than scripting convenience: a
# task's id is f(rule, subject) exactly like the derived entity above, so re-cutting one
# release addresses the task that already ran and the engine returns it instead of starting a
# second (docs/working/spawn-repeat-policy.md). A new release is a new subject, so it is a new
# task and a new flow — which is also what releases do in life.
# THE VERSION IS THE STORE REVISION, and that is the whole fix for a race this script had.
#
# A release must be a subject THE ENGINE HAS NEVER HAD A TASK FOR. Re-cutting one it has
# addresses the task that already ran, and the engine returns that task rather than starting a
# second (docs/working/spawn-repeat-policy.md) — so if it has since finished there is no
# in-flight work, and the flow this script waits for at the end never appears. Everything
# before that step is correct, which is what makes it a confusing failure rather than an
# obvious one.
#
# Counting the task entities was the first attempt and is a PROXY, wrong in three ways. A
# --scope run's task lands in baseline while its release does not. A task entity appears some
# time after the task starts, so two runs close together count the same number. And deleting a
# task entity does not make the engine forget the task — the count can be smaller than what
# the engine holds, which is exactly the case that collides.
#
# The store revision has none of those problems: it is verse's own monotonic counter, it moved
# when this run wrote its seven fixtures a moment ago, and it needs nothing to have caught up.
rev=$(v status -o tony 2>/dev/null | sed -n 's|^storeRev:[[:space:]]*||p' | tr -d '"')
[ -n "$rev" ] || die "could not read storeRev — is this verse answering \`$BIN status\`?"
REL="lintag:release:v1.4.$rev"
v data --ref "$REL" --put '{stage: cut, target: main}' >/dev/null
until_ok 100 "the manifest never landed" \
	bash -c "$BIN ${scope[*]} data '$REL' 2>/dev/null | grep -q shipped"
# shellcheck disable=SC2086
v data --ref "$REL"
printf '\n'
ok "the release carries the PRs and the commits, each still in its own system's id scheme"
# Look at `authors` against `commits`: one nests and the other does not, and the difference is
# exactly the join. `commit` on a trace is a scalar copied straight off the pull request, so
# collecting it over the set gives a flat list. `author` on a trace is already a ONE-ELEMENT
# LIST, because that is what a predicate read answers with — so collecting it gives a list of
# lists. Nothing flattens it, and nothing should: a sha that matched two commits would answer
# with two, and silently flattening would lose which trace they belonged to.
note "authors nests and commits does not: the join answers with a list, and the set read keeps it"
# The predicate did the narrowing: #43 is merged and traced, and is not in this release
# because its base was `next`. That is a different exclusion from #44's, which is not traced
# at all — one is the read's predicate, the other is the rule's subject.
v data --ref "$REL" | grep -qF 'github:pr:acme/widget#43' &&
	die "a trace onto next is in a release cut from main — the read's predicate did not narrow"
ok "#43 is traced but absent: it went onto next, and this release asked for its own branch"

# ── the flow ─────────────────────────────────────────────────────────────────
# The literal question was how these ids live in a FLOW, and the answer is that a flow is
# keyed by ONE of them. Correlation is what lets that one reach the rest.
step "the flow, which is keyed by exactly one of them"
# If this is what fails, the release is a subject the engine already had a task for — see the
# loop above, which exists to make that impossible.
until_ok 150 "no flow appeared for $REL — is a task already recorded for it? (verse data verse task instance lin-release-manifest@$REL)" \
	bash -c "$BIN ${scope[*]} flows 2>/dev/null | grep -q '$REL'"
v flow
ok "the flow's subject is $REL — one id, not a merged one"
note "the task can reach the others because the manifest is on its subject: \`verse task get\`"

# ── what this does not do ────────────────────────────────────────────────────
step "three edges, stated rather than hidden"
cat <<'EDGES'
  1. A PREDICATE NARROWS BY WHAT A TRACE LOOKS LIKE — NOT BY WHICH RELEASE IT IS IN.
     The manifest asks for traces whose `target` is this release's own, which is why
     #43 is absent. What it still cannot ask is "the ones in THIS release": nothing on
     a trace says so, and the trace's address is the engine's, f(rule, subject), so
     there is nowhere to put it. Membership would have to be a fact somebody writes —
     the release patching each trace, or a second correlation keyed on the pair — and
     a derive is about exactly one subject, so it is not the derive that would do it.
     Pairwise correlation and a predicate over the set; still nothing in between.

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
  $BIN state lineage                       every correlation verse has written
  $BIN data $tref
  $BIN data $REL    the reassembled chain
  $BIN flows                               the flow, keyed by one id
  ./scripts/lineage.sh --clean             put it all back
EOF
