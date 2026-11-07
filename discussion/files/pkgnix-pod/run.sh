#!/bin/sh
# run.sh <in/script> [args]: run one in-pod script in the staging pod, with in/lib.sh before it,
# and log to logs/<script>.log. The target is named here, never taken from the current context.
CTX=my-cluster.example.ts.net
NS=pkgnix-try
D=$(cd "$(dirname "$0")" && pwd)
s=$1; shift
mkdir -p "$D/logs"
{ [ "$s" = p1-nix.sh ] || [ "$s" = p0-env.sh ] || cat "$D/in/lib.sh"; cat "$D/in/$s"; } |
  kubectl --context $CTX -n $NS exec -i pkgnix -- bash -s -- "$@" 2>&1 | tee "$D/logs/${s%.sh}.log"
