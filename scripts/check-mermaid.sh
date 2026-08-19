#!/usr/bin/env bash
#
# check-mermaid.sh — fail if a ```mermaid block in the tree does not parse.
#
# Diagrams are the one thing here that ships unverified. Everything else is Go the compiler
# reads, tony the codegen reads, or prose. A mermaid block is a string that mkdocs copies
# through untouched and a browser parses months later — so a syntax error is invisible at
# build time and shows up as mermaid's error message printed where the picture should be.
# That is how the broker sequence in docs/seams.md shipped broken: reviewed by reading, which
# found one of its two faults and not the one the parser reports first.
#
# The VERSION is derived, not written down here. mkdocs-mermaid2-plugin decides which mermaid
# the site loads, and checking against a different one is checking a different grammar — the
# failure mode being a diagram that passes here and breaks on the page. So the plugin is asked.
#
set -euo pipefail
cd "$(cd "$(dirname "$0")/.." && pwd)"

# Node is not a build dependency of this repo and must not become one. Absent, say so and
# pass: `make check` has to stay runnable on a machine with nothing but Go, and a check that
# fails for want of a toolchain is one people route around. Loud, so a green run that skipped
# is never mistaken for a green run that checked.
if ! command -v node >/dev/null 2>&1; then
  echo "mermaid: SKIPPED — no node on PATH (diagrams unverified)"
  exit 0
fi

# Ask the plugin which mermaid the site will load. If the docs toolchain is not installed
# there is nothing to be consistent with, so fall back and say which version was assumed.
version=$(python3 -c 'import mermaid2.plugin as p; print(p.JAVASCRIPT_VERSION)' 2>/dev/null || true)
if [ -z "$version" ]; then
  version=10.4.0
  echo "mermaid: mkdocs-mermaid2-plugin not installed; assuming mermaid $version" >&2
fi

here=scripts/mermaid
have=$(node -p "require('./$here/node_modules/mermaid/package.json').version" 2>/dev/null || true)
if [ "$have" != "$version" ]; then
  echo "mermaid: fetching mermaid@$version + jsdom (one time, into $here/node_modules)"
  # jsdom is pinned: 25+ pulls an ESM-only encoding dependency that Node 20 cannot require,
  # and the failure reads as a broken checker rather than a broken diagram.
  ( cd "$here" && npm install --silent --no-audit --no-fund --no-package-lock \
      "mermaid@$version" "jsdom@24" >/dev/null )
fi

# git ls-files for the same reason check-json.sh uses it: the tree carries untracked worktree
# copies and a scratch dir, and a guard that trips on those is one people learn to ignore.
#
# Read into the array rather than `mapfile`, which is bash 4: /bin/bash on macOS is 3.2, and
# this script is one `#!/bin/bash` away from being unrunnable on the machine it was written on.
files=()
while IFS= read -r f; do files+=("$f"); done < <(git ls-files '*.md' '*.mdx')
[ ${#files[@]} -gt 0 ] || { echo "mermaid: no markdown tracked"; exit 0; }

node "$here/check.mjs" "${files[@]}"

# …and then what the parse cannot see. A `;` in a label TERMINATES the statement, so the words
# after it become states of their own: the diagram is valid, renders, and renders loose boxes
# reading "the", "branch", "is", "deleted". check.mjs has nothing to complain about, so this
# reader looks at the state table mermaid built rather than at the syntax it was built from.
node "$here/nodes.mjs" "${files[@]}"
