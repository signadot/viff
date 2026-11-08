# p9 (step 0b): store paths added to a running sandbox, from the runner's side, with the store
# reachable by no path the task has.
#
# The runner (this shell, uid 65532, no capabilities) starts the task's bwrap with the runner's
# state bound at /.verse/state, an empty read-only dir of the task's own at /nix/store, and a
# profile dir at /verse/bin. It then enters the sandbox's user and mount namespaces with nsenter
# (the uid that made a user namespace has every capability in it) and starts the agent there,
# which opens an fd on /.verse/state, mounts a tmpfs over /.verse so the task has no path to the
# state, and then binds store paths into /nix/store on command (open_tree from the fd,
# mount_setattr read-only, move_mount). The task inside reports what it sees at each phase.
set -u
TASK=/state/tasks/t1; rm -rf $TASK; mkdir -p $TASK/store $TASK/bin $TASK/synth
SYN=/state/synth; mkdir -p $SYN; [ $(ls $SYN | wc -l) -ge 2000 ] || for i in $(seq 1 2000); do mkdir -p $SYN/s$i; done
rm -f $TASK/cmd; mkfifo $TASK/cmd
rm -f $WS/go* $WS/done*
JQBIN=$(for o in $(cat $CL/jq.outs); do [ -d $S$o/bin ] && echo ${o#/nix/store/}; done | head -1)
waitfor() { local i=0; until [ -e "$1" ]; do sleep 0.1; i=$((i+1)); [ $i -gt 600 ] && { echo "[runner] timeout waiting for $1"; return 1; }; done; }
waitlog() { local i=0; until grep -q "$1" $TASK/agent.log 2>/dev/null; do sleep 0.05; i=$((i+1)); [ $i -gt 1200 ] && { echo "[runner] timeout waiting for the agent: $1"; return 1; }; done; }

TS='
go() { local i=0; while [ ! -e '$WS'/go$1 ]; do sleep 0.1; i=$((i+1)); [ $i -gt 1200 ] && { echo "[task] timed out waiting for go$1"; exit 1; }; done; }
say() { echo "[task] $*"; }
say "uid $(id -u), CapEff $(grep CapEff /proc/self/status | cut -f2), NoNewPrivs $(grep NoNewPrivs /proc/self/status | cut -f2), $(grep -c . /proc/self/mountinfo) mounts"
go 0
say "phase 0, before any install:"
say "  /nix/store has $(ls /nix/store | wc -l) entries; /.verse shows [$(ls -A /.verse 2>&1 | tr "\n" " ")]; /.verse/state: $(ls /.verse/state 2>&1 | head -1)"
say "  jq: $(jq --version 2>&1 | head -1)"
say "  the task running the agent itself: $(/verse/helper/mounter agent /verse /tmp 2>&1 | tail -1)"
say "  a mount by the task: $(mount -t tmpfs none /tmp 2>&1 | head -1)"
touch '$WS'/done0
go 1
say "phase 1, after the installs:"
say "  /nix/store has $(ls /nix/store | wc -l) entries, $(grep -c /nix/store/ /proc/self/mountinfo) of them mounts; PATH dir: [$(ls /verse/bin | tr "\n" " ")]"
for c in jq go python3 gcc; do say "  $c: $($c --version 2>&1 | head -1)"; done
say "  a symlinked store path: $(ls -ld /nix/store/*-man 2>/dev/null | head -1 | cut -c1-120)"
say "  write a store path: $(touch /nix/store/'$JQBIN'/bin/x 2>&1 | head -1); create in the store: $(touch /nix/store/planted 2>&1 | head -1); write the profile dir: $(touch /verse/bin/x 2>&1 | head -1)"
say "  /.verse shows [$(ls -A /.verse 2>&1 | tr "\n" " ")]; /.verse/state: $(ls /.verse/state 2>&1 | head -1); chmod /.verse: $(chmod 755 /.verse 2>&1 | head -1); umount /.verse: $(umount /.verse 2>&1 | head -1)"
say "  in a nested user namespace, as its root:"
unshare -Urm sh -c '"'"'
  echo "[task]    uid $(id -u), CapEff $(grep CapEff /proc/self/status | cut -f2)"
  echo "[task]    a tmpfs on /tmp there: $(mount -t tmpfs none /tmp 2>&1 | head -1; [ -z "$(mount -t tmpfs none /tmp 2>&1)" ] && echo mounted)"
  echo "[task]    umount /.verse: $(umount /.verse 2>&1 | head -1); lazily: $(umount -l /.verse 2>&1 | head -1); /.verse shows [$(ls -A /.verse 2>&1 | tr "\n" " ")]; /.verse/state: $(ls /.verse/state 2>&1 | head -1)"
  echo "[task]    the store: $(ls /nix/store | wc -l) entries; a bind of it: $(mount --bind /nix/store /tmp 2>&1 | head -1; ls /tmp | wc -l) entries under /tmp; write through it: $(touch /tmp/planted 2>&1 | head -1)"
  echo "[task]    pids seen: $(ls /proc | grep -c "^[0-9]")"
'"'"' 2>&1
touch '$WS'/done1
go 2
say "phase 2, after jq is uninstalled: jq: $(jq --version 2>&1 | head -1); its path lists [$(ls /nix/store/'$JQBIN' 2>&1 | tr "\n" " ")]; go: $(go version 2>&1 | head -1)"
touch '$WS'/done2
go 3
say "phase 3, after 2000 more binds: /nix/synth has $(ls /nix/synth | wc -l) entries; $(grep -c . /proc/self/mountinfo) mounts; go: $(go version 2>&1 | head -1)"
touch '$WS'/done3
'

echo "[runner] uid $(id -u), CapEff $(grep CapEff /proc/self/status | cut -f2); bwrap $($BW13 --version)"
$BW13 "${BASE[@]}" --ro-bind /state /.verse/state --ro-bind $TASK/store /nix/store --ro-bind $TASK/synth /nix/synth \
  --ro-bind $TASK/bin /verse/bin --ro-bind /state/helper /verse/helper \
  --setenv PATH /verse/bin:/usr/bin:/bin --remount-ro / --info-fd 3 -- bash -c "$TS" 3>$TASK/info &
BWPID=$!
sleep 1
CHILD=$(sed -n 's/.*"child-pid": *\([0-9]*\).*/\1/p' $TASK/info)
echo "[runner] bwrap pid $BWPID, its child $CHILD; the child's uid_map: $(cat /proc/$CHILD/uid_map 2>&1 | tr -s ' ')"
echo "[runner] after joining the child's user namespace alone: $(nsenter -U -t $CHILD --preserve-credentials -- grep CapEff /proc/self/status 2>&1 | tr -s '\t' ' ')"
T=""
for p in $CHILD $BWPID; do
  seen=$(nsenter -U -m -t $p --preserve-credentials -- /bin/ls / 2>&1 | tr '\n' ' ')
  echo "[runner] nsenter -U -m -t $p: / shows [$seen]"
  [ -z "$T" ] && echo "$seen" | grep -q '\.verse' && T=$p
done
[ -n "$T" ] || { echo "[runner] no pid enters the sandbox; giving up"; kill $BWPID; exit 1; }
echo "[runner] entering through pid $T"

exec 7<>$TASK/cmd
nsenter -U -m -t $T --preserve-credentials -- /verse/helper/mounter agent /.verse/state /.verse <$TASK/cmd >$TASK/agent.log 2>&1 &
AGENT=$!
waitlog hidden || { cat $TASK/agent.log; kill $BWPID; exit 1; }
echo "[runner] $(cat $TASK/agent.log | tr '\n' '|')"
touch $WS/go0; waitfor $WS/done0

N=$(wc -l < $CL/ALL.union)
s=$(now)
for p in $(cat $CL/ALL.union); do n=${p#/nix/store/}
  if [ -L $S$p ]; then ln -s "$(readlink $S$p)" $TASK/store/$n; else mkdir $TASK/store/$n; echo "bind nix/store/$n /nix/store/$n" >&7; fi
done
echo "mark bound the closures, $N paths" >&7
waitlog "bound the closures"
echo "[runner] the closures bound in $(ms $s)ms end to end; the agent: $(grep 'bound the closures' $TASK/agent.log)"
echo "[runner] the task's mounts now: $(grep -c . /proc/$CHILD/mountinfo)"
for a in jq go gcc python3; do for o in $(cat $CL/$a.outs); do
  [ -d $S$o/bin ] && for b in $S$o/bin/*; do ln -sf /nix/store/${o#/nix/store/}/bin/$(basename $b) $TASK/bin/$(basename $b); done
done; done
touch $WS/go1; waitfor $WS/done1

for o in $(cat $CL/jq.outs); do echo "unbind /nix/store/${o#/nix/store/}" >&7; done
rm -f $TASK/bin/jq
echo "mark unbound jq" >&7; waitlog "unbound jq"
echo "[runner] $(grep 'unbound jq' $TASK/agent.log)"
touch $WS/go2; waitfor $WS/done2

s=$(now); for i in $(seq 1 2000); do mkdir $TASK/synth/s$i; done; echo "[runner] 2000 mount points made outside in $(ms $s)ms"
s=$(now); for i in $(seq 1 2000); do echo "bind synth/s$i /nix/synth/s$i"; done >&7
echo "mark bound 2000 synthetic paths" >&7; waitlog "bound 2000"
echo "[runner] 2000 binds in $(ms $s)ms end to end; the agent: $(grep 'bound 2000' $TASK/agent.log)"
touch $WS/go3; waitfor $WS/done3

echo peek >&7; sleep 0.3; echo quit >&7
wait $BWPID; echo "[runner] the task exited $?"
wait $AGENT; echo "[runner] the agent exited $?"
echo "[runner] the agent's log:"; sed 's/^/  /' $TASK/agent.log
echo "[runner] the task's dir on the runner's side after: store $(ls $TASK/store | wc -l) entries (plain dirs again), bin [$(ls $TASK/bin | tr '\n' ' ')]"
