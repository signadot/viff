#!/bin/sh
. "$(dirname "$0")/common.sh"
pod_exec 01b-nodejs.sh 2>&1 | tee "$SPIKE/logs/01b-nodejs.log"
