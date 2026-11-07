#!/bin/sh
. "$(dirname "$0")/common.sh"
pod_exec 03a-more-closures.sh 2>&1 | tee "$SPIKE/logs/03a-more-closures.log"
