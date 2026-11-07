# Q1d (in a fresh container with an empty store): clean substitution timings. Arg 1: nixpkgs rev.
set -u
export NIX_CONFIG="sandbox = false
max-jobs = 0
always-allow-substitutes = true"
P=github:NixOS/nixpkgs/$1
echo "store before: $(ls /nix/store | wc -l) entries ($(du -sh /nix/store | cut -f1)), the installer's nix"
t() { s=$(date +%s%N); "$@" >/tmp/o 2>/tmp/e; rc=$?; e=$(date +%s%N); echo "rc=$rc $(( (e-s)/1000000 ))ms"; }
echo "eval only (fetch nixpkgs source + evaluate nodejs_22.outPath): $(t nix eval --raw $P#nodejs_22.outPath)"
echo "eval go (source now cached): $(t nix eval --raw $P#go.outPath)"
for a in nodejs_22 go; do
  r=$(t nix build --no-link --print-out-paths "$P#$a"); out=$(cat /tmp/o)
  echo "$a: substitute $r; paths=$(nix path-info -r $out | wc -l); closure NAR=$(nix path-info -S $out | awk '{printf "%.0f MiB", $2/1048576}'); copied=$(grep -c '^copying path' /tmp/e)"
done
echo "--- a pre-evaluated store path instead of a flake ref (what a runner would do with a locked output):"
nix-store --gc >/dev/null 2>&1
GOP=$(nix eval --raw $P#go.outPath)
s=$(date +%s%N); nix-store --realise $GOP >/dev/null 2>&1; e=$(date +%s%N); echo "go by store path, after gc: $(( (e-s)/1000000 ))ms"
echo "store after: $(ls /nix/store | wc -l) entries ($(du -sh /nix/store | cut -f1))"
