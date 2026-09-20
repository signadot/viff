#!/usr/bin/env bash
# Who holds which work, what has lapsed, and what is free — derived from the three places that
# already say it, so nothing is kept by hand and nothing can disagree with itself:
#
#   docs/working/owners.tony   the claims: work, owner, item, until
#   docs/working/work/*.md     the notes; one is claimed by an entry whose `work` is its basename
#   git issue --label residual the gaps a landing left
#
# LAPSED IS ITS OWN LIST rather than folded into free. The charter says an expired claim is
# void, so a lapsed claim and a claim never made leave the work equally free — but they do not
# tell a reader the same thing, and the difference (somebody meant to, and life moved) is worth
# knowing before picking it up.
#
# `--check` is the half `make check` runs: a note may carry a `Claimed:` line, and if it does it
# must agree with owners.tony. A note that says nothing is free to be free; a note that says
# something wrong is the only way to be wrong, which is the point of checking it.
set -euo pipefail

cd "$(dirname "$0")/.."
OWNERS=docs/working/owners.tony
NOTES=docs/working/work
TODAY=$(date +%Y-%m-%d)

# The claims, as `work<TAB>owner<TAB>until<TAB>item`. `o` reads the block; owner and until are
# absent on free work, and an absent field reads as the empty string rather than the word null.
claims() {
  o list '[*]' "$OWNERS" -trim '{work, owner, until, item}' 2>/dev/null |
    awk '
      {
        # `- ` opens an entry, and carries that entry`s first field on the same line.
        if ($0 ~ /^- /) { if (w != "") print w "\t" o "\t" u "\t" i; w=o=u=i=""; sub(/^- /, "  ") }
        v = $0
        if      (sub(/^  work: */,  "", v)) { gsub(/"/, "", v);      w = v }
        else if (sub(/^  owner: */, "", v)) { gsub(/"/, "", v);      o = v }
        else if (sub(/^  until: */, "", v)) { gsub(/"/, "", v);      u = v }
        else if (sub(/^  item: */,  "", v)) { gsub(/^"|"$/, "", v);  i = v }
      }
      END { if (w != "") print w "\t" o "\t" u "\t" i }
    '
}

held=$(claims | awk -F'\t' -v today="$TODAY" '$2 != "" && $3 >= today')
lapsed=$(claims | awk -F'\t' -v today="$TODAY" '$2 != "" && $3 != "" && $3 < today')
free=$(claims | awk -F'\t' '$2 == ""')

if [ "${1:-}" = "--check" ]; then
  bad=0
  for f in "$NOTES"/*.md; do
    line=$(grep -m1 '^Claimed: ' "$f" || true)
    [ -z "$line" ] && continue
    name=$(basename "$f" .md)
    said=${line#Claimed: }
    row=$(printf '%s\n' "$held" | awk -F'\t' -v w="$name" '$1 == w {print $2 " until " $3}')
    if [ -z "$row" ]; then
      echo "$f: says '$said', but $OWNERS holds no live claim on '$name'" >&2
      bad=1
    elif [ "$said" != "$row" ]; then
      echo "$f: says '$said'; $OWNERS says '$row'" >&2
      bad=1
    fi
  done
  [ "$bad" = 0 ] || { echo "a note's Claimed: line disagrees with $OWNERS" >&2; exit 1; }
  echo "every Claimed: line agrees with $OWNERS"
  exit 0
fi

echo "claimed"
if [ -n "$held" ]; then
  printf '%s\n' "$held" | awk -F'\t' '{ it=$4; if (length(it) > 38) it = substr(it,1,37) "…"; printf "  %-20s %-24s until %s   %s\n", $1, $2, $3, it }'
else
  echo "  (none)"
fi

echo
echo "lapsed — the claim is void, so the work is free again"
if [ -n "$lapsed" ]; then
  printf '%s\n' "$lapsed" | awk -F'\t' '{printf "  %-20s %-26s expired %s\n", $1, $2, $3}'
else
  echo "  (none)"
fi

echo
echo "free"
claimednames=$(printf '%s\n' "$held" | cut -f1)
notes=$(for f in "$NOTES"/*.md; do
  n=$(basename "$f" .md)
  printf '%s\n' "$claimednames" | grep -qx "$n" || echo "$n"
done | sort)
printf '  notes      %s\n' "$(echo $notes | fmt -w 68 | sed '2,$s/^/             /')"
# An unclaimed entry whose name IS a note is already in the line above; listing it twice would
# say there are two free things where there is one.
freework=$(printf '%s\n' "$free" | cut -f1 | while read -r n; do
  if [ -n "$n" ] && [ ! -f "$NOTES/$n.md" ]; then echo "$n"; fi
done)
if [ -n "$freework" ]; then
  printf '  work       %s\n' "$(printf '%s\n' "$freework" | tr '\n' ' ' | sed 's/ *$//')"
fi
if command -v git-issue >/dev/null 2>&1 || git issue version >/dev/null 2>&1; then
  n=$(git issue list --label residual 2>/dev/null | grep -c . || true)
  printf '  residuals  %s issues, tracked rather than held — git issue list --label residual\n' "$n"
fi
