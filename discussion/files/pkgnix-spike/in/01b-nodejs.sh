# Q1b (in the pod): why nodejs_22 "cannot build" under max-jobs 0, and the setting that fixes it.
set -u
export NIX_CONFIG="sandbox = false"
P=github:NixOS/nixpkgs/$(cat /home/nonroot/nixpkgs.rev)
drv=$(nix eval --raw "$P#nodejs_22.drvPath"); out=$(nix eval --raw "$P#nodejs_22.outPath")
echo "drv=$drv"; echo "out=$out"
echo "allowSubstitutes / preferLocalBuild in the derivation:"
nix derivation show "$drv" | tr ',' '\n' | grep -E '"(allowSubstitutes|preferLocalBuild|builder)"'
echo "is the output in cache.nixos.org?"
nix path-info --store https://cache.nixos.org "$out" 2>&1 | tail -2
echo "--- retry with always-allow-substitutes = true, still max-jobs 0"
s=$(date +%s.%N)
out=$(nix build --no-link --print-out-paths --option max-jobs 0 --option always-allow-substitutes true "$P#nodejs_22" 2>/tmp/err.node); rc=$?
e=$(date +%s.%N)
echo "rc=$rc wall=$(awk "BEGIN{printf \"%.1f\", $e-$s}")s out=$out"; tail -3 /tmp/err.node
if [ $rc -eq 0 ]; then
  nix path-info -r $out > /home/nonroot/closures/nodejs_22.paths
  echo "store paths in closure: $(wc -l < /home/nonroot/closures/nodejs_22.paths)"
  echo "closure NAR size: $(nix path-info -S $out | awk '{print $2}')"
  echo "on disk: $(xargs du -sch < /home/nonroot/closures/nodejs_22.paths | tail -1)"
fi
# Also fix up gcc's closure file (two outputs printed before).
g=$(nix build --no-link --print-out-paths --option max-jobs 0 "$P#gcc" | grep -v -- -man)
nix path-info -r $g > /home/nonroot/closures/gcc.paths
echo "gcc (main output) closure paths: $(wc -l < /home/nonroot/closures/gcc.paths), NAR $(nix path-info -S $g | awk '{print $2}')"
