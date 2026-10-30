#!/usr/bin/env bash
# verse-local.sh setup — what a verse on this machine needs before pitchfork runs it (pitchfork.toml;
# docs/platform/local-verse.md):
#
#   - verse, runnerd and verse-call-tool built from this tree, and the `o` it pins, in ~/.local/bin;
#   - ~/.config/verse-local.tony: who you are, where the verse is, and the issuer it signs as;
#   - ~/.verse/localhost_9797: the data dir, with the issuer's key and logd's config;
#   - ~/.verse/runner: the runner's config (scripts and claude) and its token, 0600;
#   - Claude Code's MCP server for the verse ($MCP_NAME);
#   - the runner's registration, once, where versed is up (else run setup again once it is).
#
# It writes only what is missing, and says what it kept. The runner's credentials are yours to
# write, in ~/.verse/runner/env, 0600: setup says how. Then: pitchfork start -l.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$HOME/.local/bin"
CONFIG="$HOME/.config/verse-local.tony"
DATA="$HOME/.verse/localhost_9797"
RUNNER="$HOME/.verse/runner"
ADDR="localhost:9797"
RUNNER_URL="http://127.0.0.1:9810"
RUNNER_NAME="${RUNNER_NAME:-laptop}"
MCP_NAME="${MCP_NAME:-verse-local}"

say() { printf '%s\n' "$*"; }
die() { printf 'verse-local: %s\n' "$*" >&2; exit 1; }

# write_once path mode content: write path where it does not exist, and say either way.
write_once() {
	if [[ -e "$1" ]]; then
		say "  kept    $1"
		return
	fi
	mkdir -p "$(dirname "$1")"
	(umask 077; printf '%s' "$3" >"$1")
	chmod "$2" "$1"
	say "  wrote   $1"
}

[[ "${1:-}" == setup ]] || { sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

say "binaries, in $BIN"
mkdir -p "$BIN"
(cd "$REPO" && go build -o "$BIN/" ./cmd/verse ./cmd/runnerd ./cmd/verse-call-tool)
say "  built   verse, runnerd, verse-call-tool"
pin="$(awk '$1 == "github.com/signadot/tony-format/go-tony" {print $2}' "$REPO/go.mod")"
[[ -n "$pin" ]] || die "no go-tony pin in go.mod"
if [[ -x "$BIN/o" ]] && go version -m "$BIN/o" 2>/dev/null | grep -q "go-tony.*$pin"; then
	say "  kept    o $pin"
elif [[ -x "$HOME/.verse/bin/$pin/o" ]]; then
	install -m 755 "$HOME/.verse/bin/$pin/o" "$BIN/o"
	say "  copied  o $pin, from verse's cache"
else
	GOBIN="$BIN" go install "github.com/signadot/tony-format/go-tony/cmd/o@$pin"
	say "  installed o $pin"
fi

say "config"
user="$(git -C "$REPO" config user.email || true)"
[[ -n "$user" ]] || die "no git user.email to name you by; set it, or write $CONFIG yourself"
issuer="auth:
  issuer: {url: \"http://$ADDR\", key: \"$DATA/issuer.pem\"}
"
write_once "$CONFIG" 644 "user: $user
addr: $ADDR
$issuer"
if ! grep -q 'issuer:' "$CONFIG"; then
	grep -q '^auth:' "$CONFIG" && die "$CONFIG has an auth block and no issuer; add: issuer: {url: \"http://$ADDR\", key: \"$DATA/issuer.pem\"}"
	printf '%s' "$issuer" >>"$CONFIG"
	say "  added   the issuer to $CONFIG"
fi

say "data dir"
mkdir -p "$DATA"
if [[ -e "$DATA/issuer.pem" ]]; then
	say "  kept    $DATA/issuer.pem"
else
	(umask 077; openssl genrsa -out "$DATA/issuer.pem" 2048 2>/dev/null)
	say "  wrote   $DATA/issuer.pem"
fi
# The logd config verse up writes: compaction with an hour's cutoff (cmd/verse/docd.go).
write_once "$DATA/logd.verse.tony" 644 "compaction: {
  cutoff: 1h
}
"

say "runner"
write_once "$RUNNER/runner.tony" 644 "listen: \"${RUNNER_URL#http://}\"
verse: {url: \"http://$ADDR\"}
script:
  shellEnv: [PATH, HOME, USER, LANG]
  systems:
    question: {ceiling: {}}
    verse: {ceiling: {}}
agents:
  claude:
    shellEnv: [PATH, HOME, USER, LANG]
    settings: {model: {default: sonnet}}
    systems:
      files: {ceiling: {}}
      commands: {ceiling: {}}
      question: {ceiling: {}}
      verse: {ceiling: {}}
"
write_once "$RUNNER/token" 600 "$(openssl rand -hex 32)
"
if [[ -s "$RUNNER/env" ]]; then
	say "  kept    $RUNNER/env"
else
	say "  missing $RUNNER/env: the runner's credentials, read into its environment alone. For claude:"
	say "            claude setup-token"
	say "            printf 'CLAUDE_CODE_OAUTH_TOKEN=%s\\n' '<the token>' >> $RUNNER/env && chmod 600 $RUNNER/env"
fi

say "sandbox"
if [[ "$(uname -s)" == Darwin ]]; then
	say "  macOS has no bubblewrap: the runner runs its tasks unconfined, as you, and builds only the"
	say "  host's network, so a spawn says network: {mode: open}"
elif bwrap --ro-bind / / --dev /dev --proc /proc --unshare-net true 2>/dev/null; then
	say "  bwrap runs here"
else
	say "  bwrap does not run here, and the runner's tasks need it. On Ubuntu 24.04, an AppArmor profile:"
	say "    printf 'abi <abi/4.0>,\\ninclude <tunables/global>\\nprofile bwrap /usr/bin/bwrap flags=(unconfined) {\\n  userns,\\n  include if exists <local/bwrap>\\n}\\n' | sudo tee /etc/apparmor.d/bwrap"
	say "    sudo apparmor_parser -r /etc/apparmor.d/bwrap"
	say "  or run its tasks unconfined, as you: VERSE_SANDBOX=none in $RUNNER/env"
fi

say "Claude Code"
if claude mcp get "$MCP_NAME" >/dev/null 2>&1; then
	say "  kept    the MCP server $MCP_NAME"
else
	claude mcp add "$MCP_NAME" -s user -e "VERSE_CONFIG=$CONFIG" -- "$BIN/verse" mcp >/dev/null
	say "  added   the MCP server $MCP_NAME (verse mcp, as $user)"
fi

say "registration"
export VERSE_CONFIG="$CONFIG"
if ! curl -sf "http://$ADDR/healthz" >/dev/null 2>&1; then
	say "  versed is not up: after \`pitchfork start -l\`, run setup again to register the runner"
elif "$BIN/verse" runner show "$RUNNER_NAME" >/dev/null 2>&1; then
	say "  kept    verse.runners.$RUNNER_NAME"
else
	# Unprobed where the runner is not up yet: its hello describes it when it starts. (No arrays:
	# macOS's bash 3.2 calls an empty one unbound under set -u.)
	if curl -sf "$RUNNER_URL/ready" >/dev/null 2>&1; then
		"$BIN/verse" runner add "$RUNNER_NAME" "$RUNNER_URL" -token-file "$RUNNER/token" >/dev/null
	else
		"$BIN/verse" runner add "$RUNNER_NAME" "$RUNNER_URL" -token-file "$RUNNER/token" -no-probe >/dev/null
	fi
	say "  added   verse.runners.$RUNNER_NAME"
fi
