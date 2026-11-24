#!/usr/bin/env bash
# relayout.sh: the move to the golang-standards/project-layout tree, as a script.
#
# Committed with the move so that a branch owner, or the viff instance -> viff-oss sync, can apply the
# same move to a tree that still has the old paths: rebase, then run this. Delete it once nothing
# needs it.
#
# Idempotent: a move whose source is gone is skipped; a move whose destination already exists as
# a directory merges the source into it (what a rebased branch leaves behind: a file it added at
# the old path, beside the moved directory); the import rewrite is a no-op on a rewritten path.
#
# What it does:
#   1. git mv the Go packages into pkg/ (what a program outside the viff instance imports) and internal/
#      (viffd's own), and the loose root files into build/, docs/ and test/.
#   2. rewrite the import paths in every *.go file (tracked or not), then gofmt the files it
#      touched, since a rewritten path can change the sort order inside an import block.
#
# It does NOT touch path mentions in comments, docs, the Makefile or the scripts; those are a
# separate, by-hand pass (see the plan the move was made from).
# Only the names listed below move: a package a branch added at the root is left where it is,
# for its owner to place.
set -euo pipefail
cd "$(dirname "$0")/.."

# Go packages that a program outside the viff instance imports: the dependency closure of the two
# documented examples (docs/reference/examples/{runner,io}) plus the CLI's client, and verbs: the
# stock runner (pkg/runner/runnerd/stock, reached from cmd/runnerd) imports it, and it imports
# nothing from internal/.
PKG="api client codec entity exprs systems task launch runner io answer mirror schedule charterfile verbs"
# viffd's own packages.
INTERNAL="viffd trigger resolver schema config adminface"

# mv SRC DST: git mv, tolerant of a rerun.
mv_() {
	local src=$1 dst=$2 f
	[ -e "$src" ] || return 0
	if [ -d "$src" ] && [ -d "$dst" ]; then
		for f in "$src"/* "$src"/.[!.]*; do
			[ -e "$f" ] || continue
			mv_ "$f" "$dst/$(basename "$f")"
		done
		rmdir "$src" 2>/dev/null || true
		return 0
	fi
	mkdir -p "$(dirname "$dst")"
	if git ls-files --error-unmatch -- "$src" >/dev/null 2>&1; then
		git mv -- "$src" "$dst"
	else
		mv -- "$src" "$dst"
	fi
	echo "moved $src -> $dst"
}

for d in $PKG; do mv_ "$d" "pkg/$d"; done
for d in $INTERNAL; do mv_ "$d" "internal/$d"; done
mv_ system/repocharters internal/repocharters   # ends the system vs systems collision
[ -d system ] && rmdir system 2>/dev/null || true

mv_ Dockerfile build/Dockerfile
mv_ overrides docs/overrides
mv_ requirements.txt docs/requirements.txt
mv_ repository.md docs/repository.md
mv_ smoke.sh test/smoke.sh
mv_ scripts/level-ping-pong.sh test/level-ping-pong.sh

# Import paths. Anchored on the module path and on what follows the package name, so that io
# does not match iod, runner does not match runnerd, and system does not match systems.
alt() { echo "$*" | tr ' ' '|'; }
sedexpr="s#(github\.com/signadot/viff/)($(alt $PKG))([/\"])#\1pkg/\2\3#g"
sedexpr="$sedexpr; s#(github\.com/signadot/viff/)($(alt $INTERNAL))([/\"])#\1internal/\2\3#g"
sedexpr="$sedexpr; s#(github\.com/signadot/viff/)system/repocharters([/\"])#\1internal/repocharters\2#g"

touched=()
while IFS= read -r -d '' f; do
	if grep -qE 'github\.com/signadot/viff/' "$f"; then
		before=$(cksum <"$f")
		sed -E -i "$sedexpr" "$f"
		[ "$before" = "$(cksum <"$f")" ] || touched+=("$f")
	fi
done < <(git ls-files -z --cached --others --exclude-standard -- '*.go')

if [ "${#touched[@]}" -gt 0 ]; then
	gofmt -w "${touched[@]}"
	echo "rewrote imports in ${#touched[@]} files"
fi
