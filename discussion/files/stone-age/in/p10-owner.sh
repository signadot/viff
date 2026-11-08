# p10 (step 0c-i): the decisive feasibility check for the userns-ownership rework. If the runner
# constructs the task's user+mount namespace and holds it, can the runner (uid 65532, no caps, as
# it runs) re-enter as the namespace's owner and add a read-only bind the task sees, with the
# source reachable by no path the task holds?
#
# Here an `owner` process stands for the namespace the runner would build for a task: it unshares
# a user+mount namespace, maps 65532 identically, makes /work (where the task's view lives) and an
# empty /work/nix-store, and sleeps. A `task` process lives inside it (nsenter), reporting what it
# sees. The runner (this shell) then re-enters the owner's namespaces and binds a store path into
# /work/nix-store. The question each line answers: does the runner get capabilities in the
# owner's userns, and does the bind land in the task's mount namespace?
set -u
OWNER=/state/0c; rm -rf $OWNER; mkdir -p $OWNER/work/nix-store $OWNER/src
# a stand-in "store path" the runner would bind in, and a file in it the task should come to see:
echo "a package file" > $OWNER/src/hello
rm -f $OWNER/ready $OWNER/cmd $OWNER/task.log; mkfifo $OWNER/cmd

echo "[runner] uid $(id -u) CapEff $(grep CapEff /proc/self/status | cut -f2) in userns $(readlink /proc/self/ns/user)"

# The owner: creates the task's user+mount namespace and holds it. `unshare -U` makes the userns
# (its creator holds every capability in it), `-m` the mount namespace, --map-user pins 65532.
# --propagation private so a bind here does not leak to the pod, and so the owner's own mounts are
# the task's. It marks /work as a mount point (a bind of itself) so move_mount/bind targets under
# it resolve, then waits.
unshare -U -m --map-user=65532 --map-group=65532 --propagation private -- bash -c '
  echo "[owner] created: uid $(id -u) CapEff $(grep CapEff /proc/self/status | cut -f2) userns $(readlink /proc/self/ns/user) mntns $(readlink /proc/self/ns/mnt)"
  mount --bind '"$OWNER"'/work '"$OWNER"'/work
  mount --make-private '"$OWNER"'/work
  echo ready > '"$OWNER"'/ready
  # the task lives here, reading go<N> markers, reporting; it never learns a path to the source.
  exec bash -c '"'"'
    seen() { echo "[task] $*"; }
    seen "uid $(id -u) CapEff $(grep CapEff /proc/self/status | cut -f2) userns $(readlink /proc/self/ns/user)"
    seen "phase 0: /work/nix-store has [$(ls -A '"$OWNER"'/work/nix-store 2>&1 | tr "\n" " ")]"
    while [ ! -e '"$OWNER"'/work/go1 ]; do sleep 0.1; done
    seen "phase 1: /work/nix-store has [$(ls -A '"$OWNER"'/work/nix-store 2>&1 | tr "\n" " ")]; read: $(cat '"$OWNER"'/work/nix-store/pkg/hello 2>&1)"
    seen "   write it: $(touch '"$OWNER"'/work/nix-store/pkg/x 2>&1 | head -1 | cut -c1-80)"
    seen "   a path to the source? /state/0c/src: $(ls /state/0c/src 2>&1 | head -1) (the runner-side dir; the task has the same /state only if /state is in its mounts)"
    seen "   the task mounts naming the source dir: $(grep -c "0c/src" /proc/self/mountinfo)"
    while [ ! -e '"$OWNER"'/work/go2 ]; do sleep 0.1; done
    seen "phase 2, after unbind: /work/nix-store has [$(ls -A '"$OWNER"'/work/nix-store 2>&1 | tr "\n" " ")]"
    touch '"$OWNER"'/work/taskdone
  '"'"'
' >$OWNER/task.log 2>&1 &
UNSHARE=$!
i=0; until [ -e $OWNER/ready ]; do sleep 0.1; i=$((i+1)); [ $i -gt 100 ] && { echo "[runner] owner never came up"; cat $OWNER/task.log; exit 1; }; done
# The owner's holder pid: the bash under unshare. Its ns files are what the runner opens.
HOLDER=$(pgrep -P $UNSHARE | head -1); [ -n "$HOLDER" ] || HOLDER=$UNSHARE
TASK=$(pgrep -P $HOLDER | head -1)
echo "[runner] owner holder pid $HOLDER (userns $(readlink /proc/$HOLDER/ns/user 2>&1), mntns $(readlink /proc/$HOLDER/ns/mnt 2>&1)); task pid $TASK"
sed 's/^/  /' $OWNER/task.log

echo "[runner] --- does the runner get capabilities entering the owner's userns?"
echo "[runner] nsenter -U (userns only), preserve-credentials: $(nsenter -U -t $HOLDER --preserve-credentials -- grep CapEff /proc/self/status 2>&1 | tr -s '\t' ' ')"
echo "[runner] nsenter -U -m (user+mount): / under it shows [$(nsenter -U -m -t $HOLDER --preserve-credentials -- ls /state/0c/work 2>&1 | tr '\n' ' ')]"

echo "[runner] --- the runner binds a store path into the task's view, from inside the owner's namespaces"
nsenter -U -m -t $HOLDER --preserve-credentials -- bash -c '
  echo "[runner/in] uid $(id -u) CapEff $(grep CapEff /proc/self/status | cut -f2)"
  mkdir -p /state/0c/work/nix-store/pkg
  mount --bind /state/0c/src /state/0c/work/nix-store/pkg && echo "[runner/in] bind rc=0" || echo "[runner/in] bind failed rc=$?"
  mount -o remount,bind,ro /state/0c/work/nix-store/pkg && echo "[runner/in] remount ro rc=0" || echo "[runner/in] remount ro failed"
  grep -c "0c/work/nix-store/pkg" /proc/self/mountinfo | sed "s/^/[runner\/in] the pkg mount is now in the ns, count: /"
'
touch $OWNER/work/go1; sleep 0.5
echo "[runner] --- now unbind it"
nsenter -U -m -t $HOLDER --preserve-credentials -- umount /state/0c/work/nix-store/pkg && echo "[runner] unbind rc=0" || echo "[runner] unbind failed"
touch $OWNER/work/go2
i=0; until [ -e $OWNER/work/taskdone ]; do sleep 0.1; i=$((i+1)); [ $i -gt 100 ] && break; done
echo "[runner] --- the task's log:"; sed 's/^/  /' $OWNER/task.log
echo "[runner] --- does the runner's bind leak to the pod? /state/0c/work/nix-store here shows [$(ls -A $OWNER/work/nix-store 2>&1 | tr '\n' ' ')]"
kill $UNSHARE 2>/dev/null; wait 2>/dev/null
