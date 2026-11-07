# Q1 (in the pod): realise closures by substitution only, measure time, size, path count.
set -u
export NIX_CONFIG="sandbox = false"
echo "id: $(id)"; echo "caps: $(grep CapEff /proc/self/status)"; nix --version
ls -ld /nix /nix/store
NIXPKGS=github:NixOS/nixpkgs/nixpkgs-unstable
# Lock to one revision so later scripts reuse it without another fetch.
t0=$(date +%s.%N)
REV=$(nix flake metadata --json $NIXPKGS | sed -n 's/.*"locked":{[^}]*"rev":"\([0-9a-f]*\)".*/\1/p')
t1=$(date +%s.%N)
echo "nixpkgs rev: $REV  (metadata+source fetch $(echo "$t1 - $t0" | bc 2>/dev/null || awk "BEGIN{print $t1-$t0}")s)"
echo "$REV" > /home/nonroot/nixpkgs.rev
P=github:NixOS/nixpkgs/$REV
mkdir -p /home/nonroot/closures
for a in nodejs_22 go python3 git gcc; do
  echo "--- $a"
  s=$(date +%s.%N)
  out=$(nix build --no-link --print-out-paths --option max-jobs 0 "$P#$a" 2>/tmp/err.$a); rc=$?
  e=$(date +%s.%N)
  echo "rc=$rc  wall=$(awk "BEGIN{printf \"%.1f\", $e-$s}")s  out=$out"
  [ $rc -ne 0 ] && { tail -5 /tmp/err.$a; continue; }
  grep -cE "^copying path|downloading" /tmp/err.$a >/dev/null
  nix path-info -r $out > /home/nonroot/closures/$a.paths
  echo "store paths in closure: $(wc -l < /home/nonroot/closures/$a.paths)"
  echo "closure size (nix path-info -S, NAR bytes): $(nix path-info -S $out | awk '{print $2}')"
  echo "closure size on disk (du -sh of the paths): $(xargs du -sch < /home/nonroot/closures/$a.paths | tail -1)"
done
echo "--- store total"; du -sh /nix/store; ls /nix/store | wc -l
