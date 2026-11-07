# Q1c (in a container): option 3, a store at another root via --store. Arg 1: the root dir.
set -u
R=$1
export NIX_CONFIG="sandbox = false"
P=github:NixOS/nixpkgs/$(cat /home/nonroot/nixpkgs.rev)
echo "seccomp: $(grep Seccomp: /proc/self/status)  caps: $(grep CapEff /proc/self/status)"
echo "can this container make a user namespace? $(unshare -U true 2>&1 && echo yes)"
rm -rf "$R" 2>/dev/null; chmod -R u+w "$R" 2>/dev/null; rm -rf "$R"
s=$(date +%s.%N)
out=$(nix build --store "$R" --no-link --print-out-paths --option max-jobs 0 --option always-allow-substitutes true "$P#go" 2>/tmp/err.s3); rc=$?
e=$(date +%s.%N)
echo "nix build --store $R go: rc=$rc wall=$(awk "BEGIN{printf \"%.1f\", $e-$s}")s out=$out"
grep -m2 -E "copying path|error" /tmp/err.s3
echo "logical path: $out ; physical: $R$out"
ls -ld "$R/nix/store" "$R$out" 2>&1 | head -3
echo "store paths physically under $R/nix/store: $(ls $R/nix/store | wc -l)"
echo "same output hash as the /nix store's go? $(ls -d /nix/store/*-go-1.26* | head -1)"
echo "--- does the logical path exist at /nix/store in THIS store's view? path-info --store $R:"
nix path-info --store "$R" -rS "$out" | tail -1
echo "--- run go from the alternative store without nix: bwrap it to /nix/store"
bwrap --die-with-parent --unshare-pid --proc /proc --dev /dev --ro-bind /usr /usr --ro-bind /lib /lib --ro-bind /bin /bin --ro-bind /etc /etc \
  --ro-bind "$R/nix/store" /nix/store -- "$out/bin/go" version 2>&1
echo "--- nix shell --store (nix makes the namespace itself)"
nix shell --store "$R" "$out" --command go version 2>&1 | tail -2
