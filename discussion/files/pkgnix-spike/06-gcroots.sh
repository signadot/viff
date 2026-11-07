#!/bin/sh
# Q6 (host side). Destructive to the spike store: run it last.
. "$(dirname "$0")/common.sh"
docker rm -f pkgnix-pod-new >/dev/null 2>&1
pod_exec 06-gcroots.sh 2>&1 | tee "$SPIKE/logs/06-gcroots.log"
