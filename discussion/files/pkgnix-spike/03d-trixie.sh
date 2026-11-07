#!/bin/sh
# Q3d (host side): 03b's timings with Debian trixie's packaged bwrap, over the same /nix and home volumes.
IMG=pkgnix-spike:trixie C=pkgnix-pod-trixie; export IMG C
. "$(dirname "$0")/common.sh"
pod_up; docker exec -i -e RUNS=3 $C bash -s < "$SPIKE/in/03b-bind-shapes.sh" 2>&1 | grep -E "^bwrap|shape=ro" | tee "$SPIKE/logs/03d-trixie.log"
pod_down
