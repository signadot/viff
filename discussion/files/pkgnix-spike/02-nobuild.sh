#!/bin/sh
. "$(dirname "$0")/common.sh"
pod_exec 02-nobuild.sh 2>&1 | tee "$SPIKE/logs/02-nobuild.log"
