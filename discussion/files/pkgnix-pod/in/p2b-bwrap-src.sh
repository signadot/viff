# p2b: nixpkgs ships 0.12.0, so bwrap 0.13.1 from source, by nixpkgs' own recipe with the
# source moved on (the image-build step, where building is allowed; not the runner's nix).
mkdir -p /tmp/nb
bwexpr() { cat <<EOF
let pkgs = (builtins.getFlake "github:NixOS/nixpkgs/$REV").legacyPackages.x86_64-linux; in
pkgs.bubblewrap.overrideAttrs (o: { version = "0.13.1"; patches = [];
  src = pkgs.fetchFromGitHub { owner = "containers"; repo = "bubblewrap"; rev = "v0.13.1"; hash = "$1"; }; })
EOF
}
h=$(nix build --option build-dir /tmp/nb --impure --no-link --expr "$(bwexpr sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=)" 2>&1 | tee /tmp/bwh | awk "/got:/{print \$2}")
tail -5 /tmp/bwh
echo "source hash: $h"
s=$(now); out=$(nix build --option build-dir /tmp/nb --impure --no-link --print-out-paths --expr "$(bwexpr $h)" 2>/tmp/bwb); rc=$?
echo "build rc=$rc in $(ms $s)ms: $out"; [ $rc -ne 0 ] && tail -15 /tmp/bwb
[ $rc -eq 0 ] && echo "$out/bin/bwrap" > ~/bw13 && echo "bw13: $($out/bin/bwrap --version); closure $(nix path-info -r $out | wc -l) paths"
