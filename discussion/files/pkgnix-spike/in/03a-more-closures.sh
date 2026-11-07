# Q3a (in the pod): substitute a few larger closures, to have a real union of store paths.
set -u
export NIX_CONFIG="sandbox = false
always-allow-substitutes = true
max-jobs = 0"
P=github:NixOS/nixpkgs/$(cat /home/nonroot/nixpkgs.rev)
for a in jq rustc cargo postgresql ffmpeg-full imagemagick; do
  s=$(date +%s.%N)
  outs=$(nix build --no-link --print-out-paths "$P#$a" 2>/tmp/e.$a); rc=$?
  e=$(date +%s.%N)
  if [ $rc -ne 0 ]; then echo "$a rc=$rc"; grep -m2 error /tmp/e.$a; continue; fi
  nix path-info -r $outs > /home/nonroot/closures/$a.paths
  echo "$a: rc=$rc wall=$(awk "BEGIN{printf \"%.1f\", $e-$s}")s paths=$(wc -l < /home/nonroot/closures/$a.paths) NAR=$(nix path-info -S $outs | awk '{s+=$2} END{print s}') (sum over outputs)"
done
sort -u /home/nonroot/closures/*.paths > /home/nonroot/closures/ALL.union
echo "union of all closures: $(wc -l < /home/nonroot/closures/ALL.union) store paths"
du -sh /nix/store
