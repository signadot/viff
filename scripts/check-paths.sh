#!/usr/bin/env bash
#
# check-paths.sh — fail if the fixed-position address vocabulary grows, or shrinks without the
# baseline following it down.
#
# An address in verse is a kinded path. system:kind:id — a store three segments deep, positions
# that mean something, a shorthand that names them — is not the model, and it has been decided so
# at least five times and come back each time: through a helper that counts to three, a shorthand
# kept for old charters, a sentence in a doc. On 2026-09-15 it was a permission leak, a reap watch
# on the wrong subtree and a loop throttle that could not see verse's own half.
# The model is docs/reference/entities.md; what is left to remove is tracked as a residual.
#
# A RATCHET rather than a ban, because the removal takes several steps and the tree has to stay
# green between them. scripts/check-paths.baseline holds, per file, how many lines still use the
# vocabulary. More than that fails. Fewer also fails, until the baseline is lowered with
# `scripts/check-paths.sh -update` — which refuses to raise any count — so a step that removes a
# use has to say so, and nothing can quietly put one back.
#
# What counts, and only what is unambiguous: a source's System() is the system it reflects, a
# different thing, so calls to System()/Kind()/ID() are not matched — their definitions on Ref and
# Match are, and every call goes with them.
#
#   Go:     Ref's and Match's System/Kind/ID (and Ref.Len, the second length); the mKey* and
#           subjPart* dimension keys; entity.Depth and its definition; halfDepth, atDepth,
#           AtNamesEntity, RefOf.
#   Charter grammar, in any file: a `system:` key opening or continuing a mapping, and
#           $subject.system / .kind / .id.
#   Prose, in any file: system:kind:id, "system, kind and id", "three segments", `{system: {kind:`.
#
# CLAUDE.md states the rule this enforces, and is not counted.
set -euo pipefail
cd "$(cd "$(dirname "$0")/.." && pwd)"

baseline=scripts/check-paths.baseline

# ERE for git grep, which has no \b: boundaries are spelled as classes.
nb='[^A-Za-z0-9_]'
patterns=(
  "func \((r Ref|m Match)\) (System|Kind|ID)\("
  "func \(r Ref\) Len\("
  "(^|$nb)(mKey|subjPart)(System|Kind|ID)($nb|$)"
  "entity\.Depth($nb|$)"
  "^const Depth = "
  "(^|$nb)(halfDepth|atDepth|AtNamesEntity)($nb|$)"
  "(^|[^A-Za-z0-9_])RefOf\("
  "(^|[{,][[:space:]]*)system:[[:space:]]"
  "\\\$subject\.(system|kind|id)($nb|$)"
  "system:kind:id"
  "system, kind,? and id"
  "[Tt]hree segments"
  "\{system: \{kind:"
)
args=()
for p in "${patterns[@]}"; do
  args+=(-e "$p")
done

current() {
  git grep -c -E "${args[@]}" -- '*.go' '*.md' '*.tony' '*.sh' '*.yaml' '*.yml' \
    ':!scripts/check-paths.sh' ':!scripts/check-paths.baseline' \
    ':!CLAUDE.md' \
    | awk -F: '{n=$NF; sub(/:[0-9]+$/, ""); print n, $0}' \
    | sort -k2 || true
}

now=$(current)
base=$( [ -f "$baseline" ] && cat "$baseline" || true )

# grew: files over their baseline (or new); shrank: files under it (or gone).
diffs=$(awk '
  NR == FNR { if ($2 != "") base[$2] = $1; next }
  { cur[$2] = $1 }
  END {
    for (f in cur) if (!(f in base) || cur[f] > base[f]) printf "grew %s %d %d\n", f, (f in base ? base[f] : 0), cur[f]
    for (f in base) if (!(f in cur) || cur[f] < base[f]) printf "shrank %s %d %d\n", f, base[f], (f in cur ? cur[f] : 0)
  }' <(printf '%s\n' "$base") <(printf '%s\n' "$now") | sort -k2)

grew=$(printf '%s\n' "$diffs" | awk '$1 == "grew" {printf "  %s: %d → %d\n", $2, $3, $4}')
shrank=$(printf '%s\n' "$diffs" | awk '$1 == "shrank" {printf "  %s: %d → %d\n", $2, $3, $4}')

if [ "${1:-}" = "-update" ]; then
  if [ -n "$grew" ] && [ -f "$baseline" ]; then
    echo "check-paths: refusing to raise the baseline:" >&2
    echo "$grew" >&2
    exit 1
  fi
  printf '%s\n' "$now" | sed '/^$/d' > "$baseline"
  echo "check-paths: baseline lowered to $(wc -l < "$baseline" | tr -d ' ') file(s)"
  exit 0
fi

if [ -n "$grew" ]; then
  echo "system:kind:id vocabulary grew:" >&2
  echo "$grew" >&2
  echo >&2
  echo "An address is a kinded path (docs/reference/entities.md). Remove the use — ask the" >&2
  echo "question's own kpath instead of a position or a depth. The baseline is never raised." >&2
  exit 1
fi
if [ -n "$shrank" ]; then
  echo "system:kind:id vocabulary shrank — lower the baseline with scripts/check-paths.sh -update:" >&2
  echo "$shrank" >&2
  exit 1
fi
total=$(printf '%s\n' "$now" | awk '{s += $1} END {print s + 0}')
echo "system:kind:id vocabulary is at its baseline ($total lines, falling)"
