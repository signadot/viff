# Q2 (in the pod): can substitution-only be enforced? A derivation not in any cache must fail.
set -u
export NIX_CONFIG="sandbox = false"
U="spike-$(date +%s%N)"   # unique name, so no cache can have it
EXPR="derivation { name = \"$U\"; system = \"aarch64-linux\"; builder = \"/bin/sh\"; args = [ \"-c\" \"echo built-by-\$(id -u) > \$out\" ]; }"
try() { echo "--- $1"; shift; out=$(nix build --no-link --print-out-paths --impure --expr "$EXPR" "$@" 2>/tmp/e); rc=$?
        echo "rc=$rc out=$out"; [ -n "$out" ] && cat "$out"; grep -E "error|Reason|Hint|warning" /tmp/e | head -4; }
echo "nix show-config (defaults that matter):"; nix config show 2>/dev/null | grep -E "^(max-jobs|builders|substituters|trusted-public-keys|require-sigs|sandbox|always-allow-substitutes|allow-import-from-derivation) " 
try "max-jobs 0 (no builders configured: builders is empty by default)" --option max-jobs 0
try "max-jobs 0 + builders '' explicit" --option max-jobs 0 --option builders ""
try "--offline only (does NOT stop builds; expect it to build)" --offline
U="$U-b"; EXPR="derivation { name = \"$U\"; system = \"aarch64-linux\"; builder = \"/bin/sh\"; args = [ \"-c\" \"echo built-by-\$(id -u) > \$out\" ]; }"
try "max-jobs 0 + --offline" --option max-jobs 0 --offline
echo "=== the same, fixed in a config file the runner owns (NIX_CONF_DIR), then overridden on the command line"
mkdir -p /tmp/nixconf && printf 'max-jobs = 0\nbuilders =\nsandbox = false\nexperimental-features = nix-command flakes\nalways-allow-substitutes = true\nallow-import-from-derivation = false\n' > /tmp/nixconf/nix.conf
export NIX_CONF_DIR=/tmp/nixconf
U="$U-c"; EXPR="derivation { name = \"$U\"; system = \"aarch64-linux\"; builder = \"/bin/sh\"; args = [ \"-c\" \"echo built-by-\$(id -u) > \$out\" ]; }"
try "config max-jobs = 0"
try "config max-jobs = 0, caller passes --option max-jobs 1 (single-user: nothing stops it)" --option max-jobs 1
echo "=== a cached package still substitutes under that config:"
P=github:NixOS/nixpkgs/$(cat /home/nonroot/nixpkgs.rev)
nix build --no-link --print-out-paths "$P#jq" 2>&1 | tail -2
echo "=== nix-store --realise of an uncached .drv under max-jobs 0:"
U="$U-d"; d=$(nix-instantiate --expr "derivation { name = \"$U\"; system = \"aarch64-linux\"; builder = \"/bin/sh\"; args = [ \"-c\" \"echo > \$out\" ]; }" 2>/dev/null)
nix-store --realise "$d" 2>&1 | tail -3
