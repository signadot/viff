# p2: bwrap 0.13 from nixpkgs, as the plan's step 3 proposes. Which nixpkgs ships it?
for r in $REV nixpkgs-unstable nixos-unstable; do
  echo "nixpkgs $r: bubblewrap $(nix eval --raw github:NixOS/nixpkgs/$r#bubblewrap.version 2>&1 | tail -1)"
done
for r in $REV nixpkgs-unstable; do
  v=$(nix eval --raw github:NixOS/nixpkgs/$r#bubblewrap.version 2>/dev/null)
  case $v in 0.1[3-9]*|0.[2-9]*) out=$(nix build --no-link --print-out-paths "${SUB[@]}" github:NixOS/nixpkgs/$r#bubblewrap 2>&1 | tail -1)
    echo "built from $r: $out"; echo "$out/bin/bwrap" > ~/bw13; break;; esac
done
[ -s ~/bw13 ] && echo "bw13: $(cat ~/bw13) -> $($(cat ~/bw13) --version)" || echo "no nixpkgs here ships bwrap >= 0.13"
echo "its closure: $(nix path-info -r $(dirname $(dirname $(cat ~/bw13 2>/dev/null || echo /x/x))) 2>/dev/null | wc -l) paths"
