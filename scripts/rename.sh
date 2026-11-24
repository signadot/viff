#!/usr/bin/env bash
# rename.sh: verse -> viff across the tree, as a script.
#
# Committed with the rename so that a branch owner can apply the same rename to a tree that
# still has the old name: rebase onto the commit before the rename, then run this. Delete it
# once nothing needs it.
#
# Idempotent: a second run finds nothing to change.
#
# Usage: scripts/rename.sh [<lower> <Capital> <UPPER>]      (default: viff Viff VIFF)
#
# What it does, in this order, to every tracked text file:
#   1. the Go module path: github.com/signadot/verse -> $MODULE, where it names the module or a
#      package in it. A repository URL (https://github.com/signadot/verse...) or a repository
#      named as host/owner/name stays on GitHub and is renamed by step 4 like any other text.
#   2. the reserved payload key, written as a path: verse.meta, verse.charter -> @sys.meta,
#      @sys.charter. The key written as a document key (`verse: {meta: ...}`) is NOT handled
#      here; it cannot be told from other `verse:` keys by pattern, and is a by-hand pass.
#   3. the reserved root, written as a path, as CLI steps or as a list of segments:
#      verse.task, `data verse`, `perms verse runners`, "verse", "task" -> mirror.task,
#      `data mirror`, `perms mirror runners`, "mirror", "task".
#   4. the name itself, in its three forms, in file paths and in contents. converse, reverse,
#      inverse and traverse (and their capitalised forms) and UNIVERSE-like REVERSE are kept.
#
# After it: gofmt -w $(git ls-files '*.go'); make generate; go mod tidy.
set -euo pipefail
cd "$(dirname "$0")/.."

export LO=${1:-viff} CAP=${2:-Viff} UP=${3:-VIFF}
export MODULE=${MODULE:-go.viff.dev/viff}
export ROOT=${ROOT:-mirror}
export PAYLOAD=${PAYLOAD:-@sys}

old='(?<![Cc]on)(?<![Rr]e)(?<![Ii]n)(?<![Tt]ra)verse'
any="$old|Verse|(?<!RE)VERSE"

# The program applied to each line of each file. Kept in a file so that neither the shell nor
# perl's own interpolation gets a say in the patterns.
prog=$(mktemp)
trap 'rm -f "$prog"' EXIT
cat >"$prog" <<'PERL'
BEGIN {
    ($lo, $cap, $up, $module, $root, $payload) = @ENV{qw(LO CAP UP MODULE ROOT PAYLOAD)};
    $children = 'task|trigger|identity|resolvers|motion|gate|runners|iods|schedule|ask|principal|systems|perms|covers|sources|charters';
}
# 1. the module path
s{^module github\.com/signadot/verse$}{module $module};
s{(?<!://)github\.com/signadot/verse(?=/)}{$module}g;
s{(verseModule\s*=\s*")github\.com/signadot/verse"}{$1$module"};
# 2. the reserved payload key, as a path
s{(?<![\w-])verse\.(meta|charter)(?![\w-])}{$payload.$1}g;
# 3. the reserved root, as a path (in logd it stands under the store's own prefix) and as steps
s{(?<![\w.-])verse\.verse\.($children)(?![\w-])}{verse.$root.$1}g;
s{(?<![\w.-])verse\.($children)(?![\w-])}{$root.$1}g;
s{\b(data|d) verse(?![\w.-])}{$1 $root}g;
s{\b(perms|watch) verse (?=(?:$children)\b)}{$1 $root }g;
s{(?<![\w.-])verse (?=(?:task instance|trigger rule)\b)}{$root }g;
s{"verse",(\s*)"($children)"}{"$root",$1"$2"}g;
# 4. the name
s{(?<![Cc]on)(?<![Rr]e)(?<![Ii]n)(?<![Tt]ra)verse}{$lo}g;
s{Verse}{$cap}g;
s{(?<!RE)VERSE}{$up}g;
PERL

# Contents first, while the paths are still the ones `git grep` reports.
# (git grep exits 1 when nothing matches, which is what a second run finds.)
{ git grep -z -IlP "$any" -- . ':!scripts/rename.sh' || true; } | xargs -0 perl -pi "$prog"

# Then the paths.
git ls-files -z | perl -0 -ne "print if /$any/" | while IFS= read -r -d '' f; do
	[ "$f" = scripts/rename.sh ] && continue
	n=$(printf '%s' "$f" | LC_ALL=C perl -pe "s{$old}{$LO}g; s{Verse}{$CAP}g; s{(?<!RE)VERSE}{$UP}g")
	[ "$f" = "$n" ] && continue
	mkdir -p "$(dirname "$n")"
	git mv "$f" "$n"
done
