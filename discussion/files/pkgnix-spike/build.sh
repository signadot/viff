#!/bin/sh
# Build the spike image (local only, never pushed).
cd "$(dirname "$0")" && docker build --platform linux/arm64 -t pkgnix-spike:local . 2>&1 | tee logs/build.log
