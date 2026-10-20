#!/usr/bin/env bash
# Carry what people wrote across a wipe: the places a hand-written list names, each with its owner,
# its grants and its data, and nothing else (git-issue dzxn44gw, step 0 of the staging plan).
#
#   deploy/carry.sh capture <verse-addr> <places-file> <dir>   BEFORE the wipe; reads, writes only <dir>
#   deploy/carry.sh restore <verse-addr> <places-file> <dir>   AFTER the repopulate; as each place's owner
#
# THE PLACES FILE is the manual step, and on purpose: somebody names the places that hold what
# people wrote and no charter manages. One place per line, as the CLI spells an address, in steps
# (`notes team`; a step holding a dot is one segment, as `verse data` takes it); `#` starts a
# comment. A deployment's own list is deploy/carry/<deployment>.places.
#
# WHAT CROSSES, per place:
#
#   the record   its owner and what it holds out (`verse perms`): taken again by the same owner,
#                with the same grants and child-grants, before anything is written in it.
#   the data     everything beneath it, as it stood, written as the owner in one write. Its
#                history does not cross: what stands after the restore is one commit.
#
# WHAT DOES NOT, and is refused at capture rather than half-carried:
#
#   a charter package's address, or anything beneath one — a charter is written against a runtime
#                that may change incompatibly, so it is installed again from its file by the new
#                vintage and its state derived again, never carried. A place with a package
#                beneath it is refused for the same reason.
#   a place with another principal's record beneath it: the data is written as the place's owner in
#                one write, which cannot write into somebody else's place. Name the places so each
#                is one owner's.
#   verse's own half, `verse.*`: bindings, groups, questions, tasks, flows. Only verse writes there,
#                and linking an account again after a wipe is the cost.
#
# An iod's reflection is not listed either: the iod fills it again on its own.
#
# CAPTURE READS AS WHOEVER RUNS IT, so run it as an admin: each place's record and data are read,
# and a place somebody may not read is refused, not skipped.
#
# RESTORE WRITES AS EACH OWNER (VERSE_USER, per place): the place is taken by whoever writes the
# record, and a restore run as somebody else would hand every place to them. That is an assertion
# the deployment's mode must admit, which `optional` does; under `required`, run the restore of
# each place as its owner yourself.
#
# <dir> holds people's data. Keep it out of the repository.
#
# THE ADDRESS IS AN ARGUMENT, never the default, as in repopulate.sh and for the same reason.
set -euo pipefail

verb="${1:-}" ADDR="${2:-}" LIST="${3:-}" DIR="${4:-}"
if [ -z "$verb" ] || [ -z "$ADDR" ] || [ -z "$LIST" ] || [ -z "$DIR" ]; then
  echo "usage: $0 capture|restore <verse-addr> <places-file> <dir>   — say which verse, always" >&2
  exit 2
fi
[ -r "$LIST" ] || { echo "$LIST: no such places file" >&2; exit 2; }
VERSE="${VERSE:-verse}"
O="${O:-o}"
v() { "$VERSE" -addr "$ADDR" "$@"; }
v status >/dev/null || { echo "cannot reach a verse at $ADDR" >&2; exit 1; }

# places prints the list's places, one per line, comments and blank lines dropped.
places() { sed -e 's/#.*//' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' "$LIST" | grep -v '^$' || true; }

# dotted joins steps the way the verse spells an address back: `notes.team`.
dotted() { local IFS=.; echo "$*"; }

# managed answers why a place is a charter's to rebuild, and nothing when it is not: a package at
# it or under it, or a package at one of the places above it.
managed() {
  local steps=("$@") i prefix out
  if out="$(v charter "${steps[@]}" 2>/dev/null)"; then
    echo "a charter is installed at or under it: $(head -1 <<<"$out")"
    return
  fi
  for ((i = 1; i < ${#steps[@]}; i++)); do
    prefix=("${steps[@]:0:i}")
    if out="$(v charter "${prefix[@]}" 2>/dev/null)" && [ "$(head -1 <<<"$out")" = "at: $(dotted "${prefix[@]}")" ]; then
      echo "it is beneath the charter package at $(dotted "${prefix[@]}")"
      return
    fi
  done
}

# strangers prints the records beneath a place whose owner is not the place's owner.
strangers() {
  local at="$1" owner="$2"
  v perms -o tony | awk -v pre="$at." -v owner="$owner" '
    /^- at: /{ at = substr($0, 7); gsub(/"/, "", at); next }
    /^  owner: /{ o = substr($0, 10); if (index(at, pre) == 1 && o != owner) print at " (" o ")" }'
}

case "$verb" in
capture)
  mkdir -p "$DIR"
  cp "$LIST" "$DIR/places"
  n=0
  while read -r line; do
    read -ra steps <<<"$line"
    at="$(dotted "${steps[@]}")"
    if why="$(managed "${steps[@]}")" && [ -n "$why" ]; then
      echo "$at: not carried, $why; it is installed again from its file after the wipe" >&2
      exit 1
    fi
    d="$DIR/$at"
    mkdir -p "$d"
    v perms "${steps[@]}" -o tony >"$d/perms.tony"
    owner="$("$O" get owner "$d/perms.tony" 2>/dev/null || true)"
    if [ -z "$owner" ]; then
      echo "$at: no record names its owner, so there is no place to carry (verse perms $line)" >&2
      exit 1
    fi
    if others="$(strangers "$at" "$owner")" && [ -n "$others" ]; then
      echo "$at: somebody else's place stands beneath it, which a write as $owner cannot restore:" >&2
      echo "$others" | sed 's/^/  /' >&2
      echo "name the places so each is one owner's" >&2
      exit 1
    fi
    v data "${steps[@]}" -o tony >"$d/data.tony"
    echo "-- $at: $owner, $(grep -c '' "$d/data.tony") line(s) of data"
    n=$((n + 1))
  done < <(places)
  echo "== captured $n place(s) from $ADDR into $DIR =="
  ;;
restore)
  [ -r "$DIR/places" ] || { echo "$DIR holds no capture (no $DIR/places)" >&2; exit 1; }
  if ! diff -q <(places) <(LIST="$DIR/places" places) >/dev/null; then
    echo "$LIST names other places than the capture in $DIR did; restore from the list it was taken with" >&2
    exit 1
  fi
  n=0
  while read -r line; do
    read -ra steps <<<"$line"
    at="$(dotted "${steps[@]}")"
    d="$DIR/$at"
    [ -s "$d/perms.tony" ] || { echo "$at: $d/perms.tony is missing: capture first" >&2; exit 1; }
    owner="$("$O" get owner "$d/perms.tony")"
    grants="$("$O" get -wire grants "$d/perms.tony" 2>/dev/null || echo '[]')"
    args=(--grants "$grants")
    if child="$("$O" get -wire child-grants "$d/perms.tony" 2>/dev/null)" && [ -n "$child" ]; then
      args+=(--child-grants "$child")
    fi
    echo "-- $at, as $owner"
    # THE RECORD BEFORE THE DATA, so the data lands in a place that is already the owner's.
    VERSE_USER="$owner" v perms "${steps[@]}" "${args[@]}" >/dev/null
    if [ -s "$d/data.tony" ] && [ "$(tr -d '[:space:]' <"$d/data.tony")" != "null" ]; then
      VERSE_USER="$owner" v data "${steps[@]}" --put "$(cat "$d/data.tony")" >/dev/null
    fi
    n=$((n + 1))
  done < <(places)
  echo "== restored $n place(s) on $ADDR from $DIR =="
  ;;
*)
  echo "usage: $0 capture|restore <verse-addr> <places-file> <dir>" >&2
  exit 2
  ;;
esac
