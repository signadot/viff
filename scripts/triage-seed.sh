#!/usr/bin/env bash
# Seed the triage drive past the backlog: one row per open verse issue saying it was judged as
# it now stands, so only an issue that moves from here on is judged
# (deploy/charters/verse/repo/verse/issue-triage.tony, "Seeding"). Run BEFORE installing the charter.
#
# Idempotent: a row that already carries a judgement is left alone, so re-running this after
# the drive has been judging does not wipe what it said.
#
# IT REFUSES rather than seed short. A seed that lists fewer open issues than the source counts
# is worse than no seed, because the install that follows judges every issue it missed — and a
# listing that silently came back empty is exactly how a whole backlog was judged at once. So
# the listing is held against the git-issue source's own count (`git-issue.verse.repo`, written
# in the same pass as the issues) before anything is written, and exits 1 on a mismatch.
#
#   VERSE_ADDR=verse:9797 scripts/triage-seed.sh        # bare `verse` goes where the config says
set -euo pipefail

v() { verse ${VERSE_ADDR:+-addr "$VERSE_ADDR"} "$@"; }

# The listing, trimmed to the two facts the mark carries. The `*` is what makes each issue the
# item `--if` and `--trim` ask of: a path names the node at it, so `git-issue.verse.issue` is the
# map of issues as one node and `git-issue.verse.issue.*` is the issues. The steps spelling is
# `git-issue verse issue '*'`. Output is tony:
#   <xidr>:
#     id: <xidr>
#     updatedAt: "<instant>"
listing="$(v data --at 'git-issue.verse.issue.*' --if '{status: open}' --trim '{id: null, updatedAt: null}' \
| awk '
  /^[^ ]/            { id=""; at="" }
  /^ *id: /          { id=$2 }
  /^ *updatedAt: /   { at=$2; gsub(/"/, "", at); if (id != "" && at != "") print id, at }
')"
listed=0
[ -n "$listing" ] && listed="$(wc -l <<<"$listing" | tr -d ' ')"

open="$(v data git-issue verse repo --trim '{open: null}' | awk '/^ *open: /{print $2}')"
if [ -z "$open" ]; then
  echo "triage-seed: git-issue.verse.repo says no open count — the source has not reflected the repository; nothing seeded" >&2
  exit 1
fi
if [ "$listed" != "$open" ]; then
  echo "triage-seed: listed $listed open issues with an id and updatedAt, and git-issue.verse.repo counts $open; nothing seeded." >&2
  echo "Installing the triage drive now would judge every issue the seed missed. If an issue moved between the two reads, re-run; otherwise the listing is wrong." >&2
  exit 1
fi

while read -r id at; do
  [ -n "$id" ] || continue
  if v data verse-dev triage next "$id" judged 2>/dev/null | grep -q 'updatedAt'; then
    echo "-- $id already judged; left alone"
    continue
  fi
  # Both the mark, which keeps `mark` quiet, and a stage, which keeps `judge` quiet: a row
  # with the mark alone is one the judge takes, holding nothing for it to read.
  v data verse-dev triage next "$id" --put "{judged: {id: $id, updatedAt: \"$at\"}, stage: seeded}" >/dev/null
  echo "-- $id seeded at $at"
done <<<"$listing"
echo "-- $listed open issues seeded or already judged"
