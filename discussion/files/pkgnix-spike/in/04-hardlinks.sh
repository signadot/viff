# Q4 (in the pod): option 1. A per-task directory T of hard links into the store, on the store's
# filesystem, bound at /nix/store under the base's per-path binds, then read-only.
set -u
BW=${BW:-bwrap}
C=/home/nonroot/closures
T=/nix/tasks/t1; rm -rf /nix/tasks 2>/dev/null; chmod -R u+w /nix/tasks 2>/dev/null; rm -rf /nix/tasks; mkdir -p $T
WS=/nix/tasks/t1-ws; mkdir -p $WS      # a workspace ON THE SAME filesystem, the worst case for links
echo "same filesystem? store dev=$(stat -c %d /nix/store) T dev=$(stat -c %d $T) ws dev=$(stat -c %d $WS) home dev=$(stat -c %d /home/nonroot)"
echo "fs.protected_hardlinks=$(cat /proc/sys/fs/protected_hardlinks)"
BASE=(--die-with-parent --new-session --cap-drop ALL --unshare-pid --unshare-ipc --unshare-uts
      --proc /proc --dev /dev --tmpfs /tmp)
for d in /usr /bin /sbin /lib /lib64 /etc /opt; do BASE+=(--ro-bind-try $d $d); done
BASE+=(--bind $WS $WS --chdir $WS --unshare-net)
# The base: go's closure, one ro-bind each, on top of T bound at /nix/store, then /nix/store ro.
ARGS=(--bind $T /nix/store); for p in $(cat $C/go.paths); do ARGS+=(--ro-bind $p $p); done; ARGS+=(--remount-ro /nix/store)
JQ=$(grep -E -- '-jq-[0-9.]*-bin$' $C/jq.paths); JQC=$(cat $C/jq.paths)
GO=$(grep -E -- '-go-1[^/]*$' $C/go.paths)
mkfifo /tmp/go.fifo 2>/dev/null
# Start the task; it waits for a signal on a fifo in its workspace, then looks for jq.
mkfifo $WS/go 2>/dev/null
$BW "${BASE[@]}" "${ARGS[@]}" -- sh -c "
  echo '[task] started; /nix/store has' \$(ls /nix/store | wc -l) 'entries; jq there?' \$(ls -d $JQ 2>/dev/null || echo no)
  $GO/bin/go version
  read x < $WS/go
  echo '[task] after the runner linked jq: entries=' \$(ls /nix/store | wc -l)
  $JQ/bin/jq --version && echo '[task] jq RUNS from links made after the sandbox started'
  echo '[task] link out of the store into the workspace (same fs, other mount):'; ln $JQ/bin/jq $WS/jq-link 2>&1
  echo '[task] link inside T (ro mount):'; ln $JQ/bin/jq /nix/store/jq-link 2>&1
  echo '[task] symlink into ws (allowed, harmless?):'; ln -s $JQ/bin/jq $WS/jq-sym && ls -l $WS/jq-sym | cut -c1-80
  echo '[task] write via chmod on a linked file:'; chmod u+w $JQ/bin/jq 2>&1
  echo '[task] mountpoints the base binds left in T (seen from inside):' \$(ls /nix/store | wc -l)
" &
TASK=$!
sleep 1
echo "[runner] T on the host now holds $(ls $T | wc -l) entries (bwrap's mountpoints for the base binds)"
# The runner's install: hard-link the paths of jq's closure that the base does not give.
s=$(date +%s%N); n=0
for p in $JQC; do grep -qx "$p" $C/go.paths && continue; cp -al "$p" "$T/" && n=$((n+1)); done
e=$(date +%s%N)
echo "[runner] linked $n store paths ($(find $T -mindepth 1 -type f | wc -l) files, $(find $T -mindepth 1 -type d | wc -l) dirs) into T in $(( (e-s)/1000000 ))ms; link count of jq now $(stat -c %h $JQ/bin/jq)"
echo go > $WS/go
wait $TASK
echo "[runner] after the task: does the workspace hold a hard link to a store inode? $(ls -li $WS 2>/dev/null | grep -c jq-link)"
echo "=== the hazard to know about: the store's inodes are owned by the task's uid"
stat -c '%U %a %n' $JQ/bin/jq
echo "[runner, outside any sandbox] a hard link made by the RUNNER into a writable dir would let 65532 write the store:"
mkdir -p /nix/tasks/hazard && ln $JQ/bin/jq /nix/tasks/hazard/jq && chmod u+w /nix/tasks/hazard/jq && echo "  chmod through a runner-made link changed the STORE inode: now mode $(stat -c %a $JQ/bin/jq)" ; chmod 555 /nix/tasks/hazard/jq; rm -rf /nix/tasks/hazard; echo "  restored: $(stat -c %a $JQ/bin/jq)"
echo "[runner] cleanup: rm -rf T alone fails, the copied dirs are r-x:"; rm -rf $T 2>&1 | head -1
echo "  (a first version of this script ran chmod -R u+w T here: that chmods the STORE's files, which are the same inodes. Restored by hand; see REPORT.)"
find $T -type d -exec chmod u+w {} + && rm -rf $T && echo "  removed after chmod u+w on T's DIRECTORIES only"
echo "  store files still read-only? writable files in jq's closure: $(for p in $JQC; do find $p -perm -u+w ! -type l; done | wc -l)"
rm -rf $WS
