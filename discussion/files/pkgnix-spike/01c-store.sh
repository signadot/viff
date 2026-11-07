#!/bin/sh
# Q1c (host side): option 3 in the pod container, then in a stricter container (Docker's
# default seccomp, so no user namespaces at all) to see whether nix needs one to substitute.
. "$(dirname "$0")/common.sh"
{
echo "##### pod-like container (seccomp unconfined)"
pod_exec 01c-store.sh /home/nonroot/s3
echo "##### strict container (default seccomp: no user namespaces)"
docker run --rm -i --platform linux/arm64 --user 65532 --cap-drop ALL --security-opt no-new-privileges \
  -v pkgnix-home:/home/nonroot $IMG bash -s -- /home/nonroot/s3-strict < "$SPIKE/in/01c-store.sh"
} 2>&1 | tee "$SPIKE/logs/01c-store.log"
