#!/bin/sh
# Build pkgnix-spike:bwrapnew = the spike image + bubblewrap v0.13.1 from source at /usr/local/bin/bwrap-new.
cd "$(dirname "$0")/bwrap-new" && docker build --platform linux/arm64 -t pkgnix-spike:bwrapnew . 2>&1 | tee ../logs/build-bwrap-new.log
