#!/bin/sh
# Q1 (host side): single-user nix as 65532, substitute toolchain closures from cache.nixos.org.
. "$(dirname "$0")/common.sh"
pod_exec 01-substitute.sh 2>&1 | tee "$SPIKE/logs/01-substitute.log"
