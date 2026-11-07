# p7: concurrency on one store apart. Two realises at once, then a collection during a realise.
set -u
FF=$(cat $CL/ffmpeg-full.outs); IM=$(cat $CL/imagemagick.outs); PY=$(cat $CL/python3.outs)
gone() { for p in "$@"; do nix path-info --store $S $p >/dev/null 2>&1 && echo kept || echo GONE; done | sort | uniq -c | tr '\n' ' '; }
echo "--- 0. collect everything unrooted, so the realises have work"
nix-store --store $S --gc 2>&1 | tail -1
echo "ffmpeg-full closure: $(gone $(cat $CL/ffmpeg-full.paths)); imagemagick: $(gone $(cat $CL/imagemagick.paths))"
echo "--- 1. two realises at once (overlapping closures)"
s=$(now)
nix build --store $S --no-link "${SUB[@]}" $FF > /tmp/r1 2>&1 & A=$!
nix build --store $S --no-link "${SUB[@]}" $IM > /tmp/r2 2>&1 & B=$!
wait $A; ra=$?; wait $B; rb=$?
echo "ffmpeg-full rc=$ra, imagemagick rc=$rb, wall $(ms $s)ms"; tail -2 /tmp/r1 /tmp/r2
echo "verify: $(nix store verify --store $S --no-trust -r $FF $IM 2>&1 | tail -2)"
echo "ffmpeg-full: $(gone $(cat $CL/ffmpeg-full.paths))"
echo "--- 2. a collection during a realise (python3's and ffmpeg's closures collected first)"
nix-store --store $S --gc 2>&1 | tail -1
s=$(now)
nix-store --store $S --add-root /state/roots/during --indirect --realise $FF "${SUB[@]}" > /tmp/r3 2>&1 & A=$!
sleep 3
g=$(now); nix-store --store $S --gc > /tmp/g 2>&1; grc=$?; echo "gc during: rc=$grc wall $(ms $g)ms: $(tail -1 /tmp/g)"
wait $A; echo "realise rc=$? wall $(ms $s)ms: $(tail -1 /tmp/r3)"
echo "ffmpeg-full after both: $(gone $(cat $CL/ffmpeg-full.paths))"
echo "verify: $(nix store verify --store $S --no-trust -r $FF 2>&1 | tail -2)"
rm -f /state/roots/during
