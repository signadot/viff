# p8b: p8's parts that need a locked flake, memory measured on nix itself, and `host` with a
# reference nothing has cached.
set -u
C=/state/nixconf; H=/state/nixhome; F=~/flake
APPLY='ps: map (p: map (o: p.${o}.outPath) (p.meta.outputsToInstall or [ "out" ])) ps'
NIXBIN=$(dirname $(readlink -f $(command -v nix)))
ENV=(env -i PATH=$NIXBIN:/usr/bin:/bin HOME=$H XDG_CACHE_HOME=$H/cache XDG_CONFIG_HOME=$H/config
     XDG_DATA_HOME=$H/data XDG_STATE_HOME=$H/state NIX_CONF_DIR=$C NIX_USER_CONF_FILES=
     SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt)
rnix() { "${ENV[@]}" nix "$@"; }
EB=(--die-with-parent --new-session --cap-drop ALL --unshare-pid --unshare-ipc --unshare-uts
    --proc /proc --dev /dev --tmpfs /tmp)
for d in /usr /bin /sbin /lib /lib64 /etc /opt; do EB+=(--ro-bind-try $d $d); done
EB+=(--ro-bind /nix /nix --bind $S $S --bind $H $H --ro-bind $C $C --ro-bind $F $F)
bnix() { $BW13 "${EB[@]}" -- "${ENV[@]}" nix "$@"; }
echo "=== 0. lock the stand-in flake outside (a remote one is locked by its own lock file)"
rnix flake lock --store $S "git+file://$F" 2>&1 | tail -1; (cd $F && git add flake.lock)
ls $H/data/nix 2>&1
R="git+file://$F#verse.x86_64-linux"
echo "=== 2. the list, in the bwrap, under pure- and restrict-eval"
s=$(now); bnix eval --store $S --json "$R.ci" --apply "$APPLY" 2>&1 | tail -3; echo "wall $(ms $s)ms"
echo "=== 4. pure-eval: a flake reading a file of the runner's (outside the bwrap, where it exists)"
rnix eval --store $S --raw "$R.reads" 2>&1 | grep -E 'error|forbidden|not-for' | head -2
echo "=== 5. restrict-eval: a flake fetching a URL at evaluation"
bnix eval --store $S --raw "$R.fetches" 2>&1 | grep -E 'error|forbidden|restricted' | head -2
echo "=== memory and time of nix itself (outside the bwrap): the stand-in, then nixpkgs cold"
peak rnix eval --store $S --json "$R.ci" --apply "$APPLY" > /dev/null
rm -rf $H/cache/*
peak rnix eval --store $S --json "$NP#legacyPackages.x86_64-linux" --apply 'p: map (x: map (o: x.${o}.outPath) (x.meta.outputsToInstall or ["out"])) [ p.jq p.go p.ripgrep p.ffmpeg-full p.git ]' > /dev/null
peak rnix eval --store $S --json "$NP#legacyPackages.x86_64-linux" --apply 'p: map (x: map (o: x.${o}.outPath) (x.meta.outputsToInstall or ["out"])) [ p.jq p.go p.ripgrep p.ffmpeg-full p.git ]' > /dev/null
echo "=== 8. github's host attribute, with a reference nothing has cached"
rnix flake metadata "github:NixOS/nixpkgs?host=example.invalid" 2>&1 | grep -iE 'example.invalid|error' | head -3
rnix flake metadata "github:NixOS/nixpkgs/0000000000000000000000000000000000000001?host=example.invalid" 2>&1 | grep -iE 'example.invalid|error' | head -3
