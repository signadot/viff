#!/bin/sh
. "$(dirname "$0")/common.sh"
pod_exec 03b-bind-shapes.sh "$@" 2>&1 | tee "$SPIKE/logs/03b-bind-shapes${1:+-$(basename $1)}.log"
