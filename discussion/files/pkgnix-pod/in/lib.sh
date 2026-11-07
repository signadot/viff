# lib.sh: before every in-pod script after p1. HOME: one 65532 owns (nix refuses one it does not, and 65532 has no passwd entry).
export HOME=/home/nonroot/h
. ~/.nix-profile/etc/profile.d/nix.sh
export NIX_CONFIG='sandbox = false
experimental-features = nix-command flakes'
REV=4fe25e12eb9f4c744966dd3de5a05246cf098601        # the nixpkgs the local run used
NP=github:NixOS/nixpkgs/$REV
S=/state/nix                                          # the store apart: nix --store $S
SUB=(--option max-jobs 0 --option always-allow-substitutes true)
CL=~/closures; mkdir -p $CL
BW8=/usr/bin/bwrap
BW13=$(cat ~/bw13 2>/dev/null)
now() { date +%s%N; }
ms() { echo $(( ($(date +%s%N)-$1)/1000000 )); }
median() { sort -n | awk '{a[NR]=$1} END{print a[int((NR+1)/2)]}'; }
# peak <cmd...>: run it, and print its peak resident memory (VmHWM) and wall time after.
peak() { local s=$(now) hwm=0 h; "$@" & local p=$!
  while kill -0 $p 2>/dev/null; do h=$(awk '/VmHWM/{print $2}' /proc/$p/status 2>/dev/null); [ -n "$h" ] && [ "$h" -gt "$hwm" ] && hwm=$h; sleep 0.05; done
  wait $p; local rc=$?; echo "[peak] rc=$rc VmHWM=$((hwm/1024))MiB wall=$(ms $s)ms" >&2; return $rc; }
# Wrap's arguments, as BwrapSandbox.Wrap builds them, around a workspace.
WS=~/ws; mkdir -p $WS
BASE=(--die-with-parent --new-session --cap-drop ALL --unshare-pid --unshare-ipc --unshare-uts
      --proc /proc --dev /dev --tmpfs /tmp)
for d in /usr /bin /sbin /lib /lib64 /etc /opt; do BASE+=(--ro-bind-try $d $d); done
BASE+=(--bind $WS $WS --chdir $WS --unshare-net)
