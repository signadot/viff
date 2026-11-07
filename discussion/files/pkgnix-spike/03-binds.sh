#!/bin/sh
. "$(dirname "$0")/common.sh"
pod_exec 03-binds.sh 2>&1 | tee "$SPIKE/logs/03-binds.log"
