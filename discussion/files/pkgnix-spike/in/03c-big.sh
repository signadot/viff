# Q3c (in the pod, image pkgnix-spike:bwrapnew): past bwrap's 9000-argument ceiling with a
# patched bwrap (MAX_ARGS 200000), to see what the kernel costs and where it stops.
set -u
RUNS=${RUNS:-5}
C=/home/nonroot/closures; SYN=/nix/synth
mkdir -p /home/nonroot/ws
BASE=(--die-with-parent --new-session --cap-drop ALL --unshare-pid --unshare-ipc --unshare-uts
      --proc /proc --dev /dev --tmpfs /tmp)
for d in /usr /bin /sbin /lib /lib64 /etc /opt; do BASE+=(--ro-bind-try $d $d); done
BASE+=(--bind /home/nonroot/ws /home/nonroot/ws --chdir /home/nonroot/ws --unshare-net)
list() { { cat $C/ALL.union; ls -d $SYN/* ; } | head -n "$1"; }
mk() { list "$1" | while read -r p; do printf -- '--ro-bind\0%s\0%s\0' "$p" "$p"; done > /tmp/a; }
median() { sort -n | awk '{a[NR]=$1} END{print a[int((NR+1)/2)]}'; }
run() { BW=$1; t=(); rc=0; for r in $(seq $RUNS); do s=$(date +%s%N); $BW --args 3 "${BASE[@]}" -- /usr/bin/true 3</tmp/a 2>/tmp/err; rc=$?; e=$(date +%s%N); t+=($(( (e-s)/1000000 ))); [ $rc -ne 0 ] && break; done
        echo "rc=$rc median=$(printf '%s\n' "${t[@]}" | median)ms all=[${t[*]}] $( [ $rc -ne 0 ] && head -c 200 /tmp/err)"; }
echo "bwrap-big: $(bwrap-big --version) (MAX_ARGS patched to 200000); mount-max=$(cat /proc/sys/fs/mount-max)"
for n in 471 2980 3000 10000 20000 30471; do mk $n; echo "n=$n: $(run bwrap-big)"; done
echo "=== at 10000 binds (471 real + synthetic): do programs run, how many mounts, is ro enforced?"
mk 10000
NODE=$(grep -E -- '-nodejs-22[^/]*$' $C/nodejs_22.paths | grep -v slim | head -1)
GO=$(grep -E -- '-go-1[^/]*$' $C/go.paths | head -1)
bwrap-big --args 3 "${BASE[@]}" -- sh -c "$NODE/bin/node --version; $GO/bin/go version; echo mounts: \$(wc -l < /proc/self/mountinfo);
  echo store entries: \$(ls /nix/store | wc -l) synth entries: \$(ls /nix/synth | wc -l);
  chmod u+w $NODE 2>&1 | head -1; touch $NODE/x 2>&1 | head -1" 3</tmp/a
echo "=== the kernel's ceiling: N binds of one directory to N distinct destinations"
for n in 50000 99990 100010 120000; do
  seq 1 $n | awk -v s="$SYN/$(ls $SYN | head -1)" '{printf "--ro-bind%c%s%c/m/%d%c", 0, s, 0, $1, 0}' > /tmp/a
  s=$(date +%s%N); bwrap-big --args 3 "${BASE[@]}" -- sh -c 'echo mounts inside: $(wc -l < /proc/self/mountinfo)' 3</tmp/a 2>&1 | tail -1; e=$(date +%s%N)
  echo "  n=$n wall=$(( (e-s)/1000000 ))ms"
done
echo "=== the same through released 0.13.1 (MAX_ARGS 9000) at 2980, for the record"
mk 2980; echo "bwrap-new n=2980: $(run bwrap-new)"
