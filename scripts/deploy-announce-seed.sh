#!/usr/bin/env bash
# Seed the deploy announcement past the tags that already exist: one silent record per
# `deploy/staging.*` tag on the remote, in the package's outbox (`verse-dev.announce.out.slack`),
# so only a tag pushed from here on is announced
# (deploy/charters/verse/workspace/Signadot/deploy-announce.tony, "The message is the latch").
# Run BEFORE installing the charter.
#
# The tags come from the REMOTE rather than from what the git io has reflected, so a seed run
# before the io's first pass still covers them all. A seeded record carries no `to`, which is
# what keeps the Slack io from delivering it.
#
# Idempotent: a record that already stands — seeded, or a message already announced — is left
# alone, so re-running this never turns a posted announcement back into a silent one.
#
#   VERSE_ADDR=verse:9797 scripts/deploy-announce-seed.sh   # bare `verse` goes where the config says
set -euo pipefail

v() { "${VERSE:-verse}" ${VERSE_ADDR:+-addr "$VERSE_ADDR"} "$@"; }

cd "$(dirname "$0")/.."

tags="$(git ls-remote --tags --refs origin 'refs/tags/deploy/staging.*' | awk '{sub("^refs/tags/", "", $2); print $2}')"
if [ -z "$tags" ]; then
  echo "deploy-announce-seed: the remote lists no deploy/staging.* tags; nothing seeded" >&2
  exit 1
fi

n=0
while read -r tag; do
  if v data verse-dev announce out slack "$tag" >/dev/null 2>&1; then
    echo "-- $tag already stands; left alone"
  else
    v data verse-dev announce out slack "$tag" --put "{seen: {name: \"$tag\"}}" >/dev/null
    echo "-- $tag seeded"
  fi
  n=$((n + 1))
done <<<"$tags"
echo "-- $n deploy tags seeded or already standing"
