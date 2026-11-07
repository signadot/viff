# Sourced by the host-side scripts. One long-lived "pod" container, approximating the runner
# pod: uid 65532, no capabilities, no-new-privileges, seccomp unconfined, /proc unmasked
# (systempaths=unconfined ~ procMount: Unmasked). /nix and the home are named volumes so the
# store survives between scripts. (/private/tmp is not a Docker Desktop file share, so the
# in-container scripts are piped in on stdin rather than bind-mounted.)
SPIKE=$(cd "$(dirname "$0")" && pwd)
IMG=${IMG:-pkgnix-spike:local}
C=${C:-pkgnix-pod}
POD_OPTS="--platform linux/arm64 --user 65532 --cap-drop ALL --security-opt no-new-privileges --security-opt seccomp=unconfined --security-opt systempaths=unconfined"
pod_up() {
  if [ "$(docker inspect -f '{{.State.Running}}' $C 2>/dev/null)" != true ]; then
    docker rm -f $C >/dev/null 2>&1
    docker run -d --name $C $POD_OPTS -v pkgnix-nix:/nix -v pkgnix-home:/home/nonroot \
      $IMG sleep infinity >/dev/null
  fi
}
# pod_exec <script-under-in/> [args]: run a spike script inside the pod container.
pod_exec() { pod_up; s=$1; shift; docker exec -i $C bash -s -- "$@" < "$SPIKE/in/$s"; }
pod_down() { docker rm -f $C >/dev/null 2>&1; }
