# deploy: the claude runner image's Claude Code download survives a dropped stream or a 429

`make ship` failed while building the claude runner image (2026-10-22). The 288 MB `linux-x64/claude` download from downloads.claude.ai broke off partway with `curl: (92) HTTP/2 stream 1 was not closed cleanly: INTERNAL_ERROR`. The checksum then failed on the truncated file, and `claude --version` died with a bus error. Repeat downloads got `429`. The same URL over HTTP/1.1 downloaded whole and matched the manifest's checksum.

**Change:** fetch the manifest and the binary with `--retry 5 --retry-all-errors --retry-delay 5` and `--http1.1` (deploy/claude-runner/Dockerfile). The checksum stays the gate on what's installed.