#!/bin/sh
# Q5 (host side). First with bookworm's bwrap 0.8.0 (pod image), then with 0.13.1 (bwrapnew image).
. "$(dirname "$0")/common.sh"
{ echo "##### bwrap 0.8.0"; pod_exec 05-overlay.sh 2>&1 | grep -E "bwrap|overlay-src"
  echo "##### bwrap 0.13.1"; IMG=pkgnix-spike:bwrapnew C=pkgnix-pod-new
  docker exec pkgnix-pod-new true 2>/dev/null || { docker stop pkgnix-pod >/dev/null; }
  pod_exec 05-overlay.sh; } 2>&1 | tee "$SPIKE/logs/05-overlay.log"
