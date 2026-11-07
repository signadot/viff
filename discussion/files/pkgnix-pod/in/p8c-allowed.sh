# p8c: restrict-eval with allowed-uris naming the sources the operator trusts: does a flake
# whose inputs are within them evaluate, and is one outside refused? And nix's own memory.
set -u
C=/state/nixconf; H=/state/nixhome; F=~/flake
APPLY='ps: map (p: map (o: p.${o}.outPath) (p.meta.outputsToInstall or [ "out" ])) ps'
NIXBIN=$(dirname $(readlink -f $(command -v nix)))
ENV=(env -i PATH=$NIXBIN:/usr/bin:/bin HOME=$H XDG_CACHE_HOME=$H/cache XDG_CONFIG_HOME=$H/config
     XDG_DATA_HOME=$H/data XDG_STATE_HOME=$H/state NIX_CONF_DIR=$C NIX_USER_CONF_FILES=
     SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt)
EB=(--die-with-parent --new-session --cap-drop ALL --unshare-pid --unshare-ipc --unshare-uts
    --proc /proc --dev /dev --tmpfs /tmp)
for d in /usr /bin /sbin /lib /lib64 /etc /opt; do EB+=(--ro-bind-try $d $d); done
EB+=(--ro-bind /nix /nix --bind $S $S --bind $H $H --ro-bind $C $C --ro-bind $F $F)
R="git+file://$F#verse.x86_64-linux"
grep -v '^allowed-uris' $C/nix.conf > /tmp/c && cp /tmp/c $C/nix.conf
echo "=== 1. allowed-uris = github:NixOS/nixpkgs (the input's source), and the flake's own place"
echo "allowed-uris = github:NixOS/nixpkgs git+file://$F" >> $C/nix.conf
$BW13 "${EB[@]}" -- "${ENV[@]}" nix eval --store $S --json "$R.ci" --apply "$APPLY" 2>&1 | grep -v '^warning\|^Pass' | tail -2
echo "    the URL fetch still refused?"
$BW13 "${EB[@]}" -- "${ENV[@]}" nix eval --store $S --raw "$R.fetches" 2>&1 | grep -E 'error:' | tail -1
echo "=== 2. allowed-uris = github:acme (the input's source outside it)"
grep -v '^allowed-uris' $C/nix.conf > /tmp/c && cp /tmp/c $C/nix.conf
echo "allowed-uris = github:acme git+file://$F" >> $C/nix.conf
$BW13 "${EB[@]}" -- "${ENV[@]}" nix eval --store $S --json "$R.ci" --apply "$APPLY" 2>&1 | grep -E 'error:' | tail -1
echo "=== 3. a prefix: does allowed-uris = github:NixOS/nix admit github:NixOS/nixpkgs?"
grep -v '^allowed-uris' $C/nix.conf > /tmp/c && cp /tmp/c $C/nix.conf
echo "allowed-uris = github:NixOS/nix git+file://$F" >> $C/nix.conf
$BW13 "${EB[@]}" -- "${ENV[@]}" nix eval --store $S --json "$R.ci" --apply "$APPLY" 2>&1 | grep -v '^warning\|^Pass' | tail -1 | cut -c1-120
grep -v '^allowed-uris' $C/nix.conf > /tmp/c && cp /tmp/c $C/nix.conf
echo "allowed-uris = github:NixOS/nixpkgs git+file://$F" >> $C/nix.conf
echo "=== 4. nix's own memory: nixpkgs list cold (cache emptied), then warm"
rm -rf $H/cache/*
peak "${ENV[@]}" nix eval --store $S --json "$NP#legacyPackages.x86_64-linux" --apply 'p: map (x: map (o: x.${o}.outPath) (x.meta.outputsToInstall or ["out"])) [ p.jq p.go p.ripgrep p.ffmpeg-full p.git ]' > /dev/null
peak "${ENV[@]}" nix eval --store $S --json "$NP#legacyPackages.x86_64-linux" --apply 'p: map (x: map (o: x.${o}.outPath) (x.meta.outputsToInstall or ["out"])) [ p.jq p.go p.ripgrep p.ffmpeg-full p.git ]' > /dev/null
peak "${ENV[@]}" nix eval --store $S --json "$R.ci" --apply "$APPLY" > /dev/null 2>&1
