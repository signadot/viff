#!/usr/bin/env bash
# verse-local.sh setup — what a verse on this machine needs before pitchfork runs it (pitchfork.toml;
# docs/platform/local-verse.md), from its settings: scripts/verse-local.env, with yours over them in
# ~/.config/verse-local.env.
#
#   - the settings, rendered into ~/.verse/local/local.env, which pitchfork's run lines source;
#   - verse, runnerd and verse-call-tool built from this tree, and the `o` it pins, in the bin dir;
#   - the verse's config: who you are, where the verse is, and the issuer it signs as;
#   - the data dir, with the issuer's key, and logd's config with the compaction cutoff;
#   - the runner's dir: its config (scripts and claude) and its token, 0600;
#   - Claude Code's MCP server for the verse;
#   - the runner's registration, once, where versed is up (else run setup again once it is).
#
# Run it again after a pull, or a setting changed. It rebuilds the binaries, renders the settings
# and the logd config afresh, and writes the rest only where missing, saying what it kept. The
# runner's credentials are yours to write, in its dir's env, 0600: setup says how. Then:
# pitchfork start -l, or a restart of what a change touched.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEFAULTS="$REPO/scripts/verse-local.env"
OVERRIDES="$HOME/.config/verse-local.env"
RENDERED="$HOME/.verse/local/local.env"

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

# write_rendered path mode content: write path where it is missing or says something else.
write_rendered() {
	if [[ -e "$1" ]] && [[ "$(cat "$1")" == "${3%$'\n'}" ]]; then
		say "  kept    $1"
		return
	fi
	local what=wrote
	[[ -e "$1" ]] && what=updated
	mkdir -p "$(dirname "$1")"
	(umask 077; printf '%s' "$3" >"$1")
	chmod "$2" "$1"
	say "  $what $(printf '%*s' $((7 - ${#what})) '')$1"
}

[[ "${1:-}" == setup ]] || { sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

say "settings"
# shellcheck source=verse-local.env
. "$DEFAULTS"
default_data="$VERSE_LOCAL_DATA"
if [[ -f "$OVERRIDES" ]]; then
	# shellcheck disable=SC1090
	. "$OVERRIDES"
	say "  read    $OVERRIDES, over $DEFAULTS"
else
	say "  read    $DEFAULTS ($OVERRIDES, yours over it, is not there)"
fi
names="$(sed -n 's/^\(VERSE_LOCAL_[A-Z_]*\)=.*/\1/p' "$DEFAULTS")"
for n in $names; do
	[[ -n "${!n:-}" ]] || die "$n is empty; scripts/verse-local.env gives it a default, so ~/.config/verse-local.env set it to nothing"
done
# A cutoff logd refuses would take the store down at start; one it reads as zero would leave its
# own hour in place, the one this setting exists to replace.
[[ "$VERSE_LOCAL_COMPACTION_CUTOFF" =~ ^([0-9]*\.?[0-9]+(ns|us|ms|s|m|h|d|w|y))+$ ]] &&
	[[ "$VERSE_LOCAL_COMPACTION_CUTOFF" =~ [1-9] ]] ||
	die "VERSE_LOCAL_COMPACTION_CUTOFF=$VERSE_LOCAL_COMPACTION_CUTOFF: a duration longer than nothing, as logd reads one (1d, 36h, 90m)"
rendered="# The local verse's settings, rendered by scripts/verse-local.sh setup from
# $DEFAULTS
# and $OVERRIDES. pitchfork's run lines source it.
# Edits here do not survive a setup: change a setting in $OVERRIDES.
"
for n in $names; do
	rendered+="$n=$(printf '%q' "${!n}")"$'\n'
done
# Before ~/.verse/local, setup kept the store in ~/.verse/localhost_9797, which a plain verse up on
# localhost:9797 uses too, and the runner in ~/.verse/runner. Starting a fresh store over such a
# setup would leave the old one and its runner's registration behind without a word.
old_data="$HOME/.verse/localhost_9797"
if [[ "$VERSE_LOCAL_DATA" == "$default_data" && ! -e "$VERSE_LOCAL_DATA" &&
	-e "$old_data/issuer.pem" && -e "$HOME/.verse/runner/runner.tony" ]]; then
	die "a local verse set up before ~/.verse/local is in $old_data, with its runner in ~/.verse/runner.
  To keep using it where it is, say so in $OVERRIDES:
    VERSE_LOCAL_DATA=\"\$HOME/.verse/localhost_9797\"
    VERSE_LOCAL_RUNNER_DIR=\"\$HOME/.verse/runner\"
    VERSE_LOCAL_CONFIG=\"\$HOME/.config/verse-local.tony\"
  To move it, stop it (pitchfork stop -l), move the three to $VERSE_LOCAL_DATA,
  $VERSE_LOCAL_RUNNER_DIR and $VERSE_LOCAL_CONFIG, and change the issuer's key in the config
  to $VERSE_LOCAL_DATA/issuer.pem.
  To start a new one beside it: mkdir -p $VERSE_LOCAL_DATA"
fi

write_rendered "$RENDERED" 644 "$rendered"

BIN="$VERSE_LOCAL_BIN"
CONFIG="$VERSE_LOCAL_CONFIG"
DATA="$VERSE_LOCAL_DATA"
RUNNER="$VERSE_LOCAL_RUNNER_DIR"
ADDR="$VERSE_LOCAL_ADDR"
RUNNER_URL="http://$VERSE_LOCAL_RUNNER_LISTEN"
RUNNER_NAME="$VERSE_LOCAL_RUNNER_NAME"
MCP_NAME="$VERSE_LOCAL_MCP_NAME"

say "binaries, in $BIN"
mkdir -p "$BIN"
# Together, always: verse refuses a runnerd of another build than its own.
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
issuer="auth:
  issuer: {url: \"http://$ADDR\", key: \"$DATA/issuer.pem\"}
"
if [[ ! -e "$CONFIG" ]]; then
	[[ -n "$user" ]] || die "no git user.email to name you by; set it, or write $CONFIG yourself"
fi
write_once "$CONFIG" 644 "user: $user
addr: $ADDR
$issuer"
# The config is yours once written, so a setting it carries is checked, not rewritten.
grep -q 'issuer:' "$CONFIG" ||
	die "$CONFIG names no issuer, and this verse signs its runner's tokens as one; add to its auth block: issuer: {url: \"http://$ADDR\", key: \"$DATA/issuer.pem\"}"
grep -Eq "^addr: *\"?$ADDR\"? *$" "$CONFIG" ||
	die "$CONFIG does not say addr: $ADDR, which VERSE_LOCAL_ADDR does; make them agree"
grep -q "url: \"http://$ADDR\"" "$CONFIG" ||
	die "$CONFIG's issuer url is not \"http://$ADDR\", which VERSE_LOCAL_ADDR makes it; make them agree"
grep -q "key: \"$DATA/issuer.pem\"" "$CONFIG" ||
	die "$CONFIG's issuer key is not \"$DATA/issuer.pem\", in the data dir VERSE_LOCAL_DATA names; make them agree"

say "data dir"
mkdir -p "$DATA"
if [[ -e "$DATA/issuer.pem" ]]; then
	say "  kept    $DATA/issuer.pem"
else
	(umask 077; openssl genrsa -out "$DATA/issuer.pem" 2048 2>/dev/null)
	say "  wrote   $DATA/issuer.pem"
fi
# The store's logd config, from the setting; the store reads it at its next start.
write_rendered "$DATA/logd.verse.tony" 644 "# Written by scripts/verse-local.sh setup on every run; edits here do not survive one.
# Its cutoff is VERSE_LOCAL_COMPACTION_CUTOFF, from scripts/verse-local.env or, over it,
# ~/.config/verse-local.env: change it there, run setup, and restart the store. A plain verse up
# on this data dir writes its own over this one, and setup puts this back.
compaction: {
  cutoff: $VERSE_LOCAL_COMPACTION_CUTOFF
}
"

say "runner"
write_once "$RUNNER/runner.tony" 644 "listen: \"$VERSE_LOCAL_RUNNER_LISTEN\"
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
grep -Eq "^listen: *\"?$VERSE_LOCAL_RUNNER_LISTEN\"? *$" "$RUNNER/runner.tony" ||
	die "$RUNNER/runner.tony does not say listen: \"$VERSE_LOCAL_RUNNER_LISTEN\", which VERSE_LOCAL_RUNNER_LISTEN does; make them agree"
grep -q "url: \"http://$ADDR\"" "$RUNNER/runner.tony" ||
	die "$RUNNER/runner.tony's verse url is not \"http://$ADDR\", which VERSE_LOCAL_ADDR makes it; make them agree"
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
if ! command -v claude >/dev/null 2>&1; then
	say "  no claude on PATH: add the MCP server once it is, by running setup again"
elif mcp="$(claude mcp get "$MCP_NAME" 2>/dev/null)"; then
	# Kept where it runs this verse on this config; else it is an older setup's, or another
	# verse's under the same name, and Claude Code would go on reaching that one.
	if grep -qF "Command: $BIN/verse" <<<"$mcp" && grep -qF "VERSE_CONFIG=$CONFIG" <<<"$mcp"; then
		say "  kept    the MCP server $MCP_NAME"
	else
		claude mcp remove "$MCP_NAME" -s user >/dev/null ||
			die "the MCP server $MCP_NAME is not $BIN/verse on $CONFIG, and is not in your user scope to replace; remove it, or set VERSE_LOCAL_MCP_NAME"
		claude mcp add "$MCP_NAME" -s user -e "VERSE_CONFIG=$CONFIG" -- "$BIN/verse" mcp >/dev/null
		say "  updated the MCP server $MCP_NAME, to $BIN/verse on $CONFIG"
	fi
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
