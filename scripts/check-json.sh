#!/usr/bin/env bash
#
# check-json.sh — fail if non-test code imports encoding/json outside the allowlist.
#
# verse's representation is tony, rendered through package codec. A stray encoding/json is
# how that quietly stops being true: it does not fail, it just produces a document in the
# wrong dialect, or silently fails to parse one — which is exactly how a client hung
# and how a claim epoch came back 0 during the migration. Both were a client
# hard-coding a decoder for a body it did not own.
#
set -euo pipefail
cd "$(cd "$(dirname "$0")/.." && pwd)"

# PERMANENT exceptions, and every one of them is the SAME exception: a foreign wire format
# that is not verse's to re-render, at the edge of the process that speaks it.
#   pkg/runner/runnerd/verse/llm.go
#                            — the OpenAI chat-completions schema.
#   pkg/codec/codec.go       — json.Number, in AsInt64. codec is where formats live.
#   pkg/runner/runnerd/verse/modeltest/model.go
#                            — the same OpenAI schema, served: the scripted model tests run
#                              tasks against. The JSON stops at that file too.
#   pkg/io/slack/*.go        — Slack's Web API and its signed event envelopes.
#   pkg/io/linear/*.go       — Linear's GraphQL responses and its signed webhook bodies.
#   pkg/io/github/*.go       — GitHub's REST responses.
#   pkg/runner/runnerd/codex/rpc.go
#                            — the codex app-server protocol: JSON-RPC over the process's stdio.
#                              The JSON stops at that file, and what leaves it is typed Go
#                              values the agent (codex.go) reads.
#   pkg/runner/runnerd/claude/wire.go
#                            — Claude Code's stream protocol: one JSON message per line over the
#                              process's stdio, control requests both ways and the MCP server
#                              this side serves inside it. The JSON stops at that file too.
#   pkg/runner/runnerd/delivery/github.go
#                            — GitHub's pull-request endpoints, the one forge write a runner
#                              makes that is not a ref. Same exception as source/github: the
#                              JSON stops at that file, and what leaves it is a tool result.
#   internal/versed/idptest/idp.go
#                            — OpenID Connect's wire format, served: the identity provider the
#                              tests point a verse and a CLI at. Discovery documents, key sets
#                              and tokens are JSON by specification, and the JSON stops at that
#                              file: a test holds a token as a string and claims as a map.
#   internal/versed/issuer.go        — the same wire format, issued: verse's own discovery document, key
#                              set and the tokens it signs, which a relying party outside verse
#                              reads as the specification writes them.
#
# The source connectors are the shape to copy: JSON stops at the
# package boundary. What they hand back is an entity.Payload, and what they serve is rendered
# by codec like everything else — so no verse surface acquires a second dialect from this.
allow='pkg/runner/runnerd/verse/llm\.go|pkg/runner/runnerd/verse/modeltest/model\.go|pkg/codec/codec.go|pkg/io/slack/[a-z]+\.go|pkg/io/linear/[a-z]+\.go|pkg/io/github/[a-z]+\.go|pkg/runner/runnerd/delivery/github\.go|pkg/runner/runnerd/codex/rpc\.go|pkg/runner/runnerd/claude/wire\.go|internal/versed/idptest/idp\.go|internal/versed/issuer\.go'

# git ls-files, not a filesystem walk: the tree carries untracked worktree copies and
# scratch dirs, and a guard that trips on those is a guard people learn to ignore.
offenders=$(git ls-files '*.go' \
  | grep -v '_test.go' \
  | grep -vE "^($allow)$" \
  | xargs grep -l $'^\t"encoding/json"$' 2>/dev/null || true)

if [ -n "$offenders" ]; then
  echo "encoding/json imported outside the allowlist:" >&2
  echo "$offenders" | sed 's/^/  /' >&2
  echo >&2
  echo "verse renders through package codec. If this file genuinely owns a foreign wire" >&2
  echo "format, add it to the allowlist in scripts/check-json.sh with the reason." >&2
  exit 1
fi
echo "encoding/json is confined to the allowlist"
