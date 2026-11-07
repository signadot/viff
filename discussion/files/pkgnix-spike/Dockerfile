# pkg-nix spike image: the runner image's base (bookworm-slim + bubblewrap) plus a single-user
# nix owned by uid 65532, prepared at build time as root (what an image does), then installed
# by 65532 itself with the official --no-daemon installer.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bubblewrap ca-certificates git curl xz-utils procps time util-linux \
 && rm -rf /var/lib/apt/lists/*
# uid 65532 with a home (the installer writes ~/.nix-profile, ~/.local/state/nix).
RUN groupadd -g 65532 nonroot && useradd -u 65532 -g 65532 -m -d /home/nonroot nonroot \
 && mkdir -m 0755 /nix && chown 65532:65532 /nix
USER 65532
ENV USER=nonroot HOME=/home/nonroot
ARG NIX_VERSION=2.35.4
RUN curl -fsSL https://releases.nixos.org/nix/nix-${NIX_VERSION}/install -o /tmp/install \
 && sh /tmp/install --no-daemon --no-channel-add --no-modify-profile \
 && rm /tmp/install
ENV PATH=/home/nonroot/.nix-profile/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
# flakes and nix-command on; no build sandbox (single user, substitution only for now).
RUN mkdir -p /home/nonroot/.config/nix \
 && printf 'experimental-features = nix-command flakes\n' > /home/nonroot/.config/nix/nix.conf
WORKDIR /home/nonroot
