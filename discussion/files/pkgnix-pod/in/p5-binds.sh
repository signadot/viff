# p5 (the local run's 03b/03c): one --ro-bind per store path, from the store apart to
# /nix/store, at size, with 0.8 and 0.13, on the command line and through --args; then what the
# task sees and can write.
set -u
RUNS=${RUNS:-5}
SYN=/state/synth; mkdir -p $SYN
[ $(ls $SYN | wc -l) -ge 3000 ] || for i in $(seq 1 3000); do mkdir -p $SYN/s$i; done
list() { { sed "s#^/nix/store/##" $CL/ALL.union | awk -v s=$S '{print s"/nix/store/"$1" /nix/store/"$1}'
           ls $SYN | awk -v d=$SYN '{print d"/"$1" /nix/synth/"$1}'; } | head -n "$1"; }
mk() { list "$1" | while read -r a b; do if [ -L "$a" ]; then printf -- "--symlink\0%s\0%s\0" "$(readlink "$a")" "$b"; else printf -- "--ro-bind\0%s\0%s\0" "$a" "$b"; fi; done > /tmp/a; }
argv() { list "$1" | while read -r a b; do if [ -L "$a" ]; then echo --symlink "$(readlink "$a")" "$b"; else echo --ro-bind "$a" "$b"; fi; done | tr '\n' ' '; }
run() { local BW=$1 how=$2 n=$3 t=() rc=0 s; local A=(); [ $how = argv ] && A=($(argv $n))
  for r in $(seq $RUNS); do s=$(now)
    if [ $how = args ]; then $BW --args 3 "${BASE[@]}" --remount-ro / -- /usr/bin/true 3</tmp/a 2>/tmp/err
    else $BW "${BASE[@]}" "${A[@]}" --remount-ro / -- /usr/bin/true 2>/tmp/err; fi
    rc=$?; t+=($(ms $s)); [ $rc -ne 0 ] && break; done
  echo "rc=$rc median=$(printf '%s\n' "${t[@]}" | median)ms $( [ $rc -ne 0 ] && head -c 160 /tmp/err)"; }
N=$(wc -l < $CL/ALL.union)
echo "real paths: $N, of which symlinks: $(for p in $(cat $CL/ALL.union); do [ -L $S$p ] && echo $p; done | wc -l); base args: ${#BASE[@]}"
for n in $N 1000 2000 2980 2990; do
  mk $n
  echo "n=$n  0.8 args: $(run $BW8 args $n)"
  echo "n=$n 0.13 args: $(run $BW13 args $n)"
  echo "n=$n 0.13 argv: $(run $BW13 argv $n)"
done
echo "=== what the task sees, at the real paths, with 0.13 on the command line"
P=""; for a in $(ls $CL/*.outs); do for o in $(cat $a); do [ -d $S$o/bin ] && P="$P:$o/bin"; done; done
P=${P#:}
NODE=$(cat $CL/nodejs_22.outs | tr ' ' '\n' | head -1)
$BW13 "${BASE[@]}" $(argv $N) --remount-ro / --setenv PATH "$P:/usr/bin:/bin" -- bash -c "
  echo \"PATH has \$(echo \$PATH | tr : '\n' | wc -l) entries\"
  for c in node go python3 git gcc psql magick ffmpeg rustc cargo jq; do printf '%s: ' \$c; \$c --version 2>&1 | head -1; done
  echo \"/nix/store lists \$(ls /nix/store | wc -l)\"
  chmod u+w $NODE 2>&1 | head -1; touch $NODE/x 2>&1 | head -1; touch /nix/store/planted 2>&1 | head -1
  ls /state 2>&1 | head -1
  touch $WS/w && echo workspace-writable && rm $WS/w"
