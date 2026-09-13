#!/usr/bin/env bash
# Seed the triage drive past the backlog: one row per open verse issue saying it was judged as
# it now stands, so only an issue that moves from here on is judged
# (docs/working/issue-triage.tony, "Seeding"). Run BEFORE installing the charter.
#
# Idempotent: a row that already carries a judgement is left alone, so re-running this after
# the drive has been judging does not wipe what it said.
#
#   VERSE_ADDR=verse:9797 scripts/triage-seed.sh        # bare `verse` goes where the config says
set -euo pipefail

v() { verse ${VERSE_ADDR:+-addr "$VERSE_ADDR"} "$@"; }

# The listing, trimmed to the two facts the mark carries. Output is tony:
#   <xidr>:
#     id: <xidr>
#     updatedAt: "<instant>"
v data git-issue verse issue --if '{status: open}' --trim '{id: null, updatedAt: null}' \
| awk '
  /^[^ ]/            { id=""; at="" }
  /^ *id: /          { id=$2 }
  /^ *updatedAt: /   { at=$2; gsub(/"/, "", at); if (id != "" && at != "") print id, at }
' \
| while read -r id at; do
  if v data verse-dev triage next "$id" judged 2>/dev/null | grep -q 'updatedAt'; then
    echo "-- $id already judged; left alone"
    continue
  fi
  # Both the mark, which keeps `mark` quiet, and a stage, which keeps `judge` quiet: a row
  # with the mark alone is one the judge takes, holding nothing for it to read.
  v data verse-dev triage next "$id" --put "{judged: {id: $id, updatedAt: \"$at\"}, stage: seeded}" >/dev/null
  echo "-- $id seeded at $at"
done
