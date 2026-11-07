#!/bin/sh
# Q3c (host side): uses the bwrapnew image (see build-bwrap-new.sh) in its own pod container.
IMG=pkgnix-spike:bwrapnew C=pkgnix-pod-new; export IMG C
. "$(dirname "$0")/common.sh"
pod_exec 03c-big.sh 2>&1 | tee "$SPIKE/logs/03c-big.log"
