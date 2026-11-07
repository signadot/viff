# Q6 (in the pod): per-task GC roots as uid 65532; what nix-store --gc keeps and drops.
set -u
export NIX_CONFIG="sandbox = false"
C=/home/nonroot/closures
GO=$(grep -E -- '-go-1[^/]*$' $C/go.paths)
JQB=$(grep -E -- '-jq-[0-9.]*-bin$' $C/jq.paths)
IM=$(grep -E -- '-imagemagick-[0-9.-]*$' $C/imagemagick.paths | head -1)
PG=$(grep -E -- '-postgresql-[0-9.]*$' $C/postgresql.paths | head -1)
echo "go=$GO"; echo "jq-bin=$JQB"; echo "imagemagick=$IM"; echo "postgresql=$PG"
mkdir -p /home/nonroot/roots
echo "--- 1. a per-task root (indirect: a symlink the runner owns, registered under gcroots/auto)"
nix-store --add-root /home/nonroot/roots/task-1-go --indirect --realise $GO 2>&1 | tail -1
ls -l /home/nonroot/roots/task-1-go | cut -c1-120; ls -l /nix/var/nix/gcroots/auto/ | tail -n +2 | cut -c1-140
BASE=(--die-with-parent --new-session --cap-drop ALL --unshare-pid --unshare-ipc --unshare-uts --proc /proc --dev /dev --tmpfs /tmp)
for d in /usr /bin /sbin /lib /lib64 /etc /opt; do BASE+=(--ro-bind-try $d $d); done
echo "--- 2. a running task using jq (no root): runtime roots via /proc"
JA=(); for p in $(nix-store -qR $JQB); do JA+=(--ro-bind $p $p); done
bwrap "${BASE[@]}" "${JA[@]}" --unshare-net -- sh -c "sleep 600 | $JQB/bin/jq ." & J=$!
echo "--- 3. an idle task with postgresql's closure bound, but running nothing from it (no root)"
PA=(); for p in $(nix-store -qR $PG); do PA+=(--ro-bind $p $p); done
mkfifo /tmp/pg.fifo 2>/dev/null
bwrap "${BASE[@]}" "${PA[@]}" --ro-bind /tmp/pg.fifo /tmp/pg.fifo --unshare-net -- sh -c "read x < /tmp/pg.fifo; echo '[idle task] after gc: psql there?'; ls $PG/bin/psql 2>&1; $PG/bin/psql --version 2>&1 | head -1; echo '[idle task] mounts on its store paths:' \$(grep -c /nix/store /proc/self/mountinfo)" & P=$!
sleep 1
echo "roots nix sees (--print-roots), counted by kind:"
nix-store --gc --print-roots 2>/dev/null | awk '{print $1}' | sed -E 's#^/proc/[0-9]+/.*#/proc/<pid>/...#; s#^/nix/var/nix/profiles/.*#profiles#; s#^/home/nonroot/(roots|\.local).*#home:\1#' | sort | uniq -c
DEAD=$(nix-store --gc --print-dead 2>/dev/null | wc -l); echo "dead paths before gc: $DEAD"
s=$(date +%s%N); nix-store --gc 2>&1 | tail -1; e=$(date +%s%N); echo "gc wall: $(( (e-s)/1000000 ))ms"
v() { [ -e "$1" ] && nix-store --check-validity "$1" 2>/dev/null && echo kept || echo GONE; }
echo "go (rooted):              $(v $GO)  closure $(for p in $(cat $C/go.paths); do v $p; done | sort | uniq -c | tr '\n' ' ')"
echo "jq-bin (in use, runtime): $(v $JQB)  closure $(for p in $(cat $C/jq.paths); do v $p; done | sort | uniq -c | tr '\n' ' ') (jq.paths includes the -man output, which is not jq-bin's closure)"
echo "imagemagick (unrooted):   $(v $IM)"
echo "postgresql (bound, idle): $(v $PG)"
echo go > /tmp/pg.fifo; wait $P
kill $J 2>/dev/null; wait $J 2>/dev/null
echo "nix still works after gc: $(nix --version)"
echo "--- 4. dropping the per-task root, then gc again: go goes"
rm /home/nonroot/roots/task-1-go; nix-store --gc 2>&1 | tail -1; echo "go: $(v $GO)"
echo "store after: $(ls /nix/store | wc -l) entries, $(du -sh /nix/store | cut -f1)"
