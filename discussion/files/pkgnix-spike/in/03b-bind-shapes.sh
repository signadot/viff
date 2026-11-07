# Q3b (in the pod): where the time goes, and the ceiling. Shapes of the per-path binds:
#  ro   : --ro-bind p p for each path (what the proposal says)
#  rw   : --bind p p for each path (NOT safe alone: the task's uid owns the store)
#  rw+ro: --tmpfs /nix/store, --bind p p each, then one --remount-ro /nix/store
# Arg 1 (optional): the bwrap binary to use.
set -u
BW=${1:-bwrap}; RUNS=${RUNS:-5}
echo "bwrap: $($BW --version)"
C=/home/nonroot/closures; SYN=/nix/synth
mkdir -p /home/nonroot/ws
BASE=(--die-with-parent --new-session --cap-drop ALL --unshare-pid --unshare-ipc --unshare-uts
      --proc /proc --dev /dev --tmpfs /tmp)
for d in /usr /bin /sbin /lib /lib64 /etc /opt; do BASE+=(--ro-bind-try $d $d); done
BASE+=(--bind /home/nonroot/ws /home/nonroot/ws --chdir /home/nonroot/ws --unshare-net)
echo "base args: ${#BASE[@]}"
list() { { cat $C/ALL.union; ls -d $SYN/* ; } | head -n "$1"; }
mk() { # mk <shape> <n> -> /tmp/a
  { [ "$1" = rwro ] && printf -- '--tmpfs\0/nix/store\0--tmpfs\0/nix/synth\0'
    list "$2" | while read -r p; do case $1 in ro) printf -- '--ro-bind\0%s\0%s\0' "$p" "$p";; *) printf -- '--bind\0%s\0%s\0' "$p" "$p";; esac; done
    [ "$1" = rwro ] && printf -- '--remount-ro\0/nix/store\0--remount-ro\0/nix/synth\0'; } > /tmp/a; }
median() { sort -n | awk '{a[NR]=$1} END{print a[int((NR+1)/2)]}'; }
run() { t=(); rc=0; for r in $(seq $RUNS); do s=$(date +%s%N); $BW --args 3 "${BASE[@]}" -- /usr/bin/true 3</tmp/a 2>/tmp/err; rc=$?; e=$(date +%s%N); t+=($(( (e-s)/1000000 ))); [ $rc -ne 0 ] && break; done
        echo "rc=$rc median=$(printf '%s\n' "${t[@]}" | median)ms $( [ $rc -ne 0 ] && head -c 160 /tmp/err)"; }
for n in 471 1000 2000 2980 2990; do
  for shape in ro rw rwro; do mk $shape $n; echo "n=$n shape=$shape: $(run)"; done
done
echo "=== rw+ro shape: is a bound store path writable inside? (n=471)"
mk rwro 471
NODE=$(grep -E -- '-nodejs-22[^/]*$' $C/nodejs_22.paths | grep -v slim | head -1)
$BW --args 3 "${BASE[@]}" -- sh -c "touch $NODE/x 2>&1 | head -1; touch /nix/store/planted 2>&1 | head -1; $NODE/bin/node --version; grep -c ' ro,' /proc/self/mountinfo; grep -c ' rw,' /proc/self/mountinfo" 3</tmp/a
echo "=== rw shape: is it writable? (it must not be, and it IS here) n=471"
mk rw 471
$BW --args 3 "${BASE[@]}" -- sh -c "chmod u+w $NODE && touch $NODE/x && echo 'WROTE INTO THE SHARED STORE: $NODE/x' && rm $NODE/x && chmod u-w $NODE" 3</tmp/a 2>&1
