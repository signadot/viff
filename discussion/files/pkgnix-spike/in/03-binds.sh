# Q3 (in the pod): bwrap with the task's flags plus one --ro-bind per store path.
set -u
C=/home/nonroot/closures
RUNS=${RUNS:-7}
echo "kernel: $(uname -r)  mount-max: $(cat /proc/sys/fs/mount-max)  ARG_MAX(getconf): $(getconf ARG_MAX)  stack: $(ulimit -s)"
echo "mounts in the pod's own namespace: $(wc -l < /proc/self/mountinfo)"
# Synthetic store-like paths, to pad the real union up to the target counts. One file each.
SYN=/nix/synth
if [ ! -d $SYN ] || [ $(ls $SYN | wc -l) -lt 30000 ]; then
  mkdir -p $SYN; for i in $(seq 1 30000); do d=$SYN/$(printf '%032d' $i)-synth; mkdir -p $d/bin; echo x > $d/bin/f; done
fi
mkdir -p /home/nonroot/ws
BASE=(--die-with-parent --new-session --cap-drop ALL --unshare-pid --unshare-ipc --unshare-uts
      --proc /proc --dev /dev --tmpfs /tmp)
for d in /usr /bin /sbin /lib /lib64 /etc /opt; do BASE+=(--ro-bind-try $d $d); done
BASE+=(--bind /home/nonroot/ws /home/nonroot/ws --chdir /home/nonroot/ws --unshare-net)
# list <n>: the first n paths, real union first, then synthetic.
list() { { cat $C/ALL.union; ls -d $SYN/* ; } | head -n "$1"; }
mkargs() { list "$1" | while read -r p; do printf -- '--ro-bind\0%s\0%s\0' "$p" "$p"; done > /tmp/args.$1; }
median() { sort -n | awk '{a[NR]=$1} END{print a[int((NR+1)/2)]}'; }
ms() { echo $(( ($2 - $1) / 1000000 )); }
echo
echo "n_binds | argv bytes | direct argv: rc / median ms | --args fd: rc / median ms (runs=$RUNS) | mounts inside"
for n in 0 32 471 1000 3000 10000 20000 30000; do
  mkargs $n
  bytes=$(( $(wc -c < /tmp/args.$n) + 600 ))
  # direct argv
  mapfile -d '' -t B < /tmp/args.$n
  dt=(); drc=0
  for r in $(seq $RUNS); do s=$(date +%s%N); bwrap "${BASE[@]}" "${B[@]}" -- /usr/bin/true 2>/tmp/derr; drc=$?; e=$(date +%s%N); dt+=($(ms $s $e)); [ $drc -ne 0 ] && break; done
  dmed=$(printf '%s\n' "${dt[@]}" | median)
  # --args fd
  at=(); arc=0
  for r in $(seq $RUNS); do s=$(date +%s%N); bwrap --args 3 "${BASE[@]}" -- /usr/bin/true 3</tmp/args.$n 2>/tmp/aerr; arc=$?; e=$(date +%s%N); at+=($(ms $s $e)); [ $arc -ne 0 ] && break; done
  amed=$(printf '%s\n' "${at[@]}" | median)
  mi=$(bwrap --args 3 "${BASE[@]}" -- sh -c 'wc -l < /proc/self/mountinfo' 3</tmp/args.$n 2>&1)
  echo "$n | $bytes | $drc / ${dmed}ms $( [ $drc -ne 0 ] && head -c 200 /tmp/derr) | $arc / ${amed}ms $( [ $arc -ne 0 ] && head -c 200 /tmp/aerr) | $mi"
  unset B
done
echo
echo "=== do the programs run from the binds? (union of real closures only: 471 binds)"
mkargs 471
NODE=$(grep -E -- '-nodejs-22[^/]*$' $C/nodejs_22.paths | grep -v slim | head -1)
GO=$(grep -E -- '-go-1[^/]*$' $C/go.paths | head -1)
FF=$(grep -E -- '-ffmpeg-full-[0-9.]*-bin$|-ffmpeg-full-[0-9.]*$' $C/ffmpeg-full.paths | head -1)
BINPATH=$(for p in $(cat $C/ALL.union); do [ -d $p/bin ] && printf '%s:' $p/bin; done)
echo "PATH made of the closures' bin dirs: $(echo "$BINPATH" | tr ':' '\n' | grep -c .) dirs, ${#BINPATH} bytes"
bwrap --args 3 "${BASE[@]}" -- sh -c "
  echo 'ls /nix/store | wc -l ->' \$(ls /nix/store | wc -l)
  echo 'by store path:'; $NODE/bin/node --version; $GO/bin/go version
  echo 'by PATH:'; PATH='${BINPATH}/usr/bin:/bin'; export PATH
  node -e 'console.log(\"node\", process.version, process.execPath)'
  go version; command -v go; python3 -c 'import sys,ssl; print(\"python\", sys.version.split()[0], ssl.OPENSSL_VERSION)'
  git --version; ffmpeg -version | head -1; psql --version; rustc --version; cargo --version; jq --version; magick -version | head -1
  echo 'GOCACHE build test:'; mkdir -p /tmp/h && cd /tmp/h && printf 'package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"hello from go build\")}\n' > m.go && HOME=/tmp GOCACHE=/tmp/gc GOTOOLCHAIN=local go run m.go
  echo 'writability:'; touch /nix/store/planted 2>&1 && echo 'CAN create /nix/store/planted (sandbox root tmpfs)'; touch $NODE/x 2>&1 | head -1; touch /newfile-at-root 2>&1 | head -1
  ls -ld / /nix /nix/store
" 3</tmp/args.471 2>&1
echo "=== same, with --remount-ro / added at the end of the args"
bwrap --args 3 "${BASE[@]}" --remount-ro / -- sh -c "touch /nix/store/planted 2>&1 | head -1; touch /newfile-at-root 2>&1 | head -1; echo ws-writable: \$(touch /home/nonroot/ws/ok && echo yes)" 3</tmp/args.471 2>&1
