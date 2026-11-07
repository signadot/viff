# p6 (the local run's 06), in the store apart: realise-and-root in one command, what a
# collection keeps, a running task with no root, and a root removed. Run last: it empties the store.
set -u
R=/state/roots; mkdir -p $R; echo "roots left by p7: $(ls $R)"; rm -f $R/during*
GO=$(cat $CL/go.outs); JQ=$(cat $CL/jq.outs); PG=$(cat $CL/postgresql.outs | tr ' ' '\n' | head -1); IM=$(cat $CL/imagemagick.outs | tr ' ' '\n' | head -1)
v() { nix path-info --store $S "$1" >/dev/null 2>&1 && echo kept || echo GONE; }
echo "--- 1. realise and root in one command, indirect, under the runner's roots directory"
nix-store --store $S --add-root $R/task-1 --indirect --realise $GO "${SUB[@]}" 2>&1 | tail -2; echo "rc=$?"
ls -l $R/ | tail -n +2 | cut -c40-; echo "gcroots/auto: $(ls -l $S/nix/var/nix/gcroots/auto 2>&1 | tail -n +2 | cut -c40-)"
echo "--- 1b. the same with nix build --out-link"
nix build --store $S --out-link $R/task-1b "${SUB[@]}" $GO 2>&1 | tail -1; ls -l $R/ | tail -n +2 | cut -c40-
rm -f $R/task-1b
echo "--- 2. a running task with jq's closure bound and jq executing, no root"
JA=(); for p in $(nix path-info --store $S -r $JQ); do if [ -L $S$p ]; then JA+=(--symlink $(readlink $S$p) $p); else JA+=(--ro-bind $S$p $p); fi; done
JB=$(echo $JQ | tr ' ' '\n' | grep -- -bin$)
$BW13 "${BASE[@]}" "${JA[@]}" -- sh -c "sleep 600 | $JB/bin/jq ." & J=$!
sleep 1
echo "dead before: $(nix-store --store $S --gc --print-dead 2>/dev/null | wc -l)"
s=$(now); nix-store --store $S --gc 2>&1 | tail -1; echo "gc wall: $(ms $s)ms"
echo "go (rooted):           $(v $GO)  closure: $(for p in $(cat $CL/go.paths); do v $p; done | sort | uniq -c | tr '\n' ' ')"
echo "jq (running, no root): $(v $JB)"
echo "postgresql (nothing):  $(v $PG)"
echo "imagemagick (nothing): $(v $IM)"
kill $J 2>/dev/null; wait $J 2>/dev/null
echo "--- 3. remove the root, collect: go goes"
rm $R/task-1; nix-store --store $S --gc 2>&1 | tail -1; echo "go: $(v $GO)"
echo "store after: $(ls $S/nix/store | wc -l) entries, $(du -sh $S/nix/store | cut -f1)"
