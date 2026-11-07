# p6b: a running task with no root, in the store apart: does a collection see the process's
# mapped files (/proc/<pid>/maps name /nix/store/…, which the store apart also names its paths by)?
set -u
JQ=$(cat $CL/jq.outs); JB=$(echo $JQ | tr ' ' '\n' | grep -- -bin$)
nix build --store $S --no-link "${SUB[@]}" $JQ 2>&1 | tail -1
JA=(); for p in $(nix path-info --store $S -r $JQ); do if [ -L $S$p ]; then JA+=(--symlink $(readlink $S$p) $p); else JA+=(--ro-bind $S$p $p); fi; done
$BW13 "${BASE[@]}" "${JA[@]}" -- sh -c "sleep 600 | $JB/bin/jq ." & J=$!
sleep 1; echo "jq running: $(pgrep -f "$JB/bin/jq" | head -1)"
echo "roots nix sees: $(nix-store --store $S --gc --print-roots 2>/dev/null | grep -c jq)"
nix-store --store $S --gc 2>&1 | tail -1
nix path-info --store $S $JB >/dev/null 2>&1 && echo "jq-bin (running, no root): kept" || echo "jq-bin (running, no root): GONE"
kill $J 2>/dev/null; wait $J 2>/dev/null
nix-store --store $S --gc 2>&1 | tail -1
