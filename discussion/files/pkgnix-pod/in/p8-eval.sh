# p8: evaluating a flake under step 4's settings, in a bwrap without the runner's state, with
# the runner's own HOME and XDG directories; what it costs; what a flake cannot do to it.
set -u
C=/state/nixconf; H=/state/nixhome; F=~/flake
mkdir -p $C $H/{cache,config,data,state} /state/secrets $F
echo not-for-a-flake > /state/secrets/probe
cat > $C/nix.conf <<'EOF'
experimental-features = nix-command flakes
max-jobs = 0
builders =
always-allow-substitutes = true
allow-import-from-derivation = false
require-sigs = true
sandbox = false
substituters = https://cache.nixos.org
trusted-public-keys = cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=
accept-flake-config = false
pure-eval = true
restrict-eval = true
use-registries = false
flake-registry =
EOF
# A flake standing in for acme's: a list of packages at verse.<system>.ci, nixpkgs as an input,
# a nixConfig asking for another cache and key, and outputs that read a file and fetch a URL.
# Local here only so the run needs no repository; the runner refuses a local reference.
cat > $F/flake.nix <<EOF
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/$REV";
  nixConfig = {
    extra-substituters = [ "https://cache.example.invalid" ];
    extra-trusted-public-keys = [ "cache.example.invalid-1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" ];
  };
  outputs = { nixpkgs, ... }: let pkgs = nixpkgs.legacyPackages.x86_64-linux; in {
    verse.x86_64-linux.ci = with pkgs; [ jq go ripgrep ];
    verse.x86_64-linux.one = pkgs.hello;
    verse.x86_64-linux.reads = builtins.readFile "/state/secrets/probe";
    verse.x86_64-linux.fetches = builtins.readFile (builtins.fetchurl "https://example.com/");
  };
}
EOF
( cd $F && git init -q 2>/dev/null; git add -A >/dev/null 2>&1 )
APPLY='ps: map (p: map (o: p.${o}.outPath) (p.meta.outputsToInstall or [ "out" ])) ps'
NIXBIN=$(dirname $(readlink -f $(command -v nix)))
# The runner's nix: one config, its own HOME and XDG, nothing else in its environment.
rnix() { env -i PATH=$NIXBIN:/usr/bin:/bin HOME=$H XDG_CACHE_HOME=$H/cache XDG_CONFIG_HOME=$H/config \
  XDG_DATA_HOME=$H/data XDG_STATE_HOME=$H/state NIX_CONF_DIR=$C NIX_USER_CONF_FILES= \
  SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt nix "$@"; }
# The same, in a bwrap: the system dirs and nix read-only, the store apart and the nix home
# writable, the network shared, nothing else of /state.
EB=(--die-with-parent --new-session --cap-drop ALL --unshare-pid --unshare-ipc --unshare-uts
    --proc /proc --dev /dev --tmpfs /tmp)
for d in /usr /bin /sbin /lib /lib64 /etc /opt; do EB+=(--ro-bind-try $d $d); done
EB+=(--ro-bind /nix /nix --bind $S $S --bind $H $H --ro-bind $C $C --ro-bind $F $F)
bnix() { $BW13 "${EB[@]}" -- env -i PATH=$NIXBIN:/usr/bin:/bin HOME=$H XDG_CACHE_HOME=$H/cache \
  XDG_CONFIG_HOME=$H/config XDG_DATA_HOME=$H/data XDG_STATE_HOME=$H/state NIX_CONF_DIR=$C \
  NIX_USER_CONF_FILES= SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt nix "$@"; }

echo "=== 1. the settings nix reads (nix config show, the ones step 4 sets)"
rnix config show 2>&1 | grep -E '^(max-jobs|builders|always-allow-substitutes|allow-import-from-derivation|require-sigs|accept-flake-config|pure-eval|restrict-eval|use-registries|flake-registry|substituters|trusted-public-keys) ='
echo "=== 2. the list, cold (empty nix home), in the bwrap, under restrict-eval"
peak bnix eval --store $S --json "git+file://$F#verse.x86_64-linux.ci" --apply "$APPLY" 2>&1 | tail -4
echo "=== 3. warm"
peak bnix eval --store $S --json "git+file://$F#verse.x86_64-linux.ci" --apply "$APPLY" 2>&1 | tail -2
echo "=== 4. pure-eval: a flake reading the runner's files"
bnix eval --store $S --raw "git+file://$F#verse.x86_64-linux.reads" 2>&1 | grep -E 'error|forbidden|not-for' | head -2
echo "    and does the bwrap show /state/secrets at all?"; $BW13 "${EB[@]}" -- ls /state/secrets 2>&1 | head -1
echo "=== 5. restrict-eval: a flake fetching a URL at evaluation"
bnix eval --store $S --raw "git+file://$F#verse.x86_64-linux.fetches" 2>&1 | grep -E 'error|forbidden' | head -2
echo "=== 6. nixConfig, with accept-flake-config = false: is the extra cache used?"
bnix build --store $S --dry-run -v "git+file://$F#verse.x86_64-linux.one" 2>&1 | grep -iE 'example.invalid|untrusted|ignoring|nixConfig' | head -4
echo "=== 7. the same, with a trusted-settings.json in the runner's own XDG_DATA_HOME accepting it"
mkdir -p $H/data/nix
echo '{"extra-substituters":{"https://cache.example.invalid":true},"extra-trusted-public-keys":{"cache.example.invalid-1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=":true}}' > $H/data/nix/trusted-settings.json
bnix build --store $S --dry-run -v "git+file://$F#verse.x86_64-linux.one" 2>&1 | grep -iE 'example.invalid|untrusted|ignoring|trusted' | head -4
rm $H/data/nix/trusted-settings.json
echo "=== 8. github's host attribute: does nix fetch from the host it names?"
rnix flake metadata "github:NixOS/nixpkgs/$REV?host=example.invalid" 2>&1 | grep -iE 'example.invalid|error' | head -2
echo "=== 9. a remote flake in the runner's form (nixpkgs itself): eval of a list, cold home"
rm -rf $H/cache/*
peak bnix eval --store $S --json "$NP#legacyPackages.x86_64-linux" --apply 'p: map (x: map (o: x.${o}.outPath) (x.meta.outputsToInstall or ["out"])) [ p.jq p.go p.ripgrep ]' 2>&1 | tail -3
