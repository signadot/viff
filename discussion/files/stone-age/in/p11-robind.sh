# p11: the one mechanic the stone-age model rests on. A runner-owned install root $I bound
# read-only into the task once at start (bwrap, no caps). A task starts and reads $I/bin. The
# runner then writes a new tool into $I from its own side, mid-run. Does the task see it through
# the read-only bind, and is the task still kept from writing $I? No new mount is made after the
# task starts.
set -u
I=/state/tasks/i1; rm -rf $I; mkdir -p $I/bin $I/lib
printf '#!/bin/sh\necho "jq-v1 says hi"\n' > $I/bin/jq; chmod 755 $I/bin/jq   # one tool present at start
rm -f $WS/go* $WS/done*

TS='
go() { local i=0; while [ ! -e '$WS'/go$1 ]; do sleep 0.1; i=$((i+1)); [ $i -gt 600 ] && exit 1; done; }
say() { echo "[task] $*"; }
say "uid $(id -u) CapEff $(grep CapEff /proc/self/status | cut -f2); PATH=$PATH"
say "phase 0: /inst/bin holds [$(ls /inst/bin 2>&1 | tr "\n" " ")]"
say "   jq at start: $(jq 2>&1 | head -1)"
say "   go present?  $(command -v go || echo no)"
say "   task writes /inst/bin: $(touch /inst/bin/evil 2>&1 | head -1 | cut -c1-70)"
touch '$WS'/done0
go 1
say "phase 1, after the runner installed go and a newer jq, no new mount:"
say "   /inst/bin holds [$(ls /inst/bin 2>&1 | tr "\n" " ")]"
say "   go now: $(go 2>&1 | head -1)"
say "   jq now: $(jq 2>&1 | head -1)"
say "   a lib appeared: $(cat /inst/lib/libx.txt 2>&1 | head -1)"
say "   task writes the new file: $(touch /inst/bin/go.evil 2>&1 | head -1 | cut -c1-70)"
say "   the task mounts naming /inst: $(grep -c /inst /proc/self/mountinfo) (one bind, made at start)"
touch '$WS'/done1
'
echo "[runner] bwrap $($BW13 --version); binding $I read-only at /inst, once"
$BW13 "${BASE[@]}" --ro-bind $I /inst --setenv PATH "/inst/bin:/usr/bin:/bin" --remount-ro / -- bash -c "$TS" &
BW=$!
# let the task reach phase 0
i=0; until [ -e $WS/done0 ]; do sleep 0.1; i=$((i+1)); [ $i -gt 300 ] && break; done
echo "[runner] --- task is running; now installing into $I from the runner's side (plain writes, no mount)"
printf '#!/bin/sh\necho "go version stone-age"\n' > $I/bin/go; chmod 755 $I/bin/go
printf '#!/bin/sh\necho "jq-v2 says hi"\n' > $I/bin/jq; chmod 755 $I/bin/jq      # LRU would swap the tree; here just overwrite
echo "libx from the runner" > $I/lib/libx.txt
echo "[runner] wrote go, jq-v2, lib/libx.txt into $I"
touch $WS/go1
wait $BW
echo "[runner] --- task exited; /inst on the runner side still holds [$(ls $I/bin | tr '\n' ' ')], task's evil writes present? [$(ls $I/bin | grep evil || echo none)]"
