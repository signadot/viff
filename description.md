# scripts: seeding fails silently on macOS's own bash

`scripts/seed-entities.sh`, `seed-demo.sh`, `lineage.sh` and `charter-shapes.sh` call verse through `v() { "$BIN" "${scope[@]}" "$@"; }`. With no `--scope`, `scope` is empty, and under `set -u` bash 3.2 — `/bin/bash` on macOS — treats `"${scope[@]}"` as an unbound variable. The first `v` is `v status >/dev/null 2>&1 || die …`, so the script exits 1 having printed nothing.

So the tutorial's first command (`./scripts/seed-entities.sh`, docs/tutorial/data.md) fails with no message on a Mac without a newer bash on PATH.

Fix: `${scope[@]+"${scope[@]}"}`, the form `charter-shapes.sh` already uses for `args`.