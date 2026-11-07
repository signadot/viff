#!/bin/sh
# Q1d (host side): fresh volumes, so nothing is already in the store. Removed afterwards.
. "$(dirname "$0")/common.sh"
REV=4fe25e12eb9f4c744966dd3de5a05246cf098601
docker run --rm -i $POD_OPTS -v pkgnix-nix-clean:/nix -v pkgnix-home-clean:/home/nonroot $IMG bash -s -- $REV \
  < "$SPIKE/in/01d-clean-timing.sh" 2>&1 | tee "$SPIKE/logs/01d-clean-timing.log"
docker volume rm pkgnix-nix-clean pkgnix-home-clean >/dev/null
