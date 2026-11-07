#!/bin/sh
. "$(dirname "$0")/common.sh"
pod_exec 04-hardlinks.sh 2>&1 | tee "$SPIKE/logs/04-hardlinks.log"
