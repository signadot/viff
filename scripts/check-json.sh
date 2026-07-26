#!/usr/bin/env bash
#
# check-json.sh — fail if non-test code imports encoding/json outside the allowlist.
#
# verse's representation is tony, rendered through package codec. A stray encoding/json is
# how that quietly stops being true: it does not fail, it just produces a document in the
# wrong dialect, or silently fails to parse one — which is exactly how `verse connect` hung
# and how the broker's claim epoch came back 0 during the migration. Both were a client
# hard-coding a decoder for a body it did not own.
#
set -euo pipefail
cd "$(cd "$(dirname "$0")/.." && pwd)"

# PERMANENT exceptions.
#   process/harness/llm.go — the OpenAI chat-completions schema. Not verse's format; not
#     ours to re-render.
#   codec/codec.go         — json.Number, in AsInt64. codec is where formats live.
#
# PENDING removal. These are the charter grammar's pre-tony JSON codecs. They are dead in
# production — every path goes through codec, which uses the tony codecs and Detag — but
# four test files still exercise the grammar through them. Delete them with those tests.
allow='process/harness/llm.go|codec/codec.go|trigger/formula.go|trigger/match.go'

# git ls-files, not a filesystem walk: the tree carries untracked worktree copies and
# scratch dirs, and a guard that trips on those is a guard people learn to ignore.
offenders=$(git ls-files '*.go' \
  | grep -v '_test.go' \
  | grep -vE "^($allow)$" \
  | xargs grep -l '^\t"encoding/json"$' 2>/dev/null || true)

if [ -n "$offenders" ]; then
  echo "encoding/json imported outside the allowlist:" >&2
  echo "$offenders" | sed 's/^/  /' >&2
  echo >&2
  echo "verse renders through package codec. If this file genuinely owns a foreign wire" >&2
  echo "format, add it to the allowlist in scripts/check-json.sh with the reason." >&2
  exit 1
fi
echo "encoding/json is confined to the allowlist"
