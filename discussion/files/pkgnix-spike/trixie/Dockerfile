# Debian trixie's packaged bubblewrap, to see whether a distro bump alone fixes the 0.8.0 timing.
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends bubblewrap procps && rm -rf /var/lib/apt/lists/*
