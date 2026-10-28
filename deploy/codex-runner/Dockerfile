# The codex runner image: the verse runner image with Codex in it.
#
# Built via `make codex-runner-docker`; it COPYs the same locally cross-compiled runnerd the
# other image does, for the reason that Dockerfile gives. It is a separate image because what it
# adds is sized by a different thing again: a hundred-megabyte agent binary pinned to the
# version the codex agent was written against (runner/runnerd/codex), which neither the runtime
# image nor the plain runner has any reason to carry.
FROM debian:bookworm-slim
ARG TARGETARCH

# bubblewrap is the containment; git is for the shell and the toolchains that shell out to it;
# ripgrep is what Codex reaches for when it searches a tree, and a search that falls back to
# grep is a slower agent rather than a broken one, so this is a convenience and not a
# requirement.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bubblewrap ca-certificates git curl ripgrep \
 && rm -rf /var/lib/apt/lists/*

ARG GO_VERSION=1.27.1
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
      | tar -C /usr/local -xz \
 && ln -s /usr/local/go/bin/go /usr/local/bin/go \
 && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt

# Codex, PINNED. The agent speaks this version's app-server protocol; the message shapes it
# reads were generated from this binary, and a newer one is a newer protocol until somebody
# checks. The release ships one static binary per target, so there is no node and no npm here.
# Under /usr/local/bin for the reason the runner is: /usr is what bwrap binds into the sandbox,
# and a binary installed anywhere else starts nowhere.
ARG CODEX_VERSION=0.162.0
RUN case "${TARGETARCH}" in \
      amd64) CODEX_ARCH=x86_64 ;; \
      arm64) CODEX_ARCH=aarch64 ;; \
      *) echo "no codex build for ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSL "https://github.com/openai/codex/releases/download/rust-v${CODEX_VERSION}/codex-${CODEX_ARCH}-unknown-linux-musl.tar.gz" \
      | tar -C /usr/local/bin -xz \
 && mv "/usr/local/bin/codex-${CODEX_ARCH}-unknown-linux-musl" /usr/local/bin/codex \
 && chmod 0755 /usr/local/bin/codex \
 && codex --version

COPY dist/linux/${TARGETARCH}/runnerd /usr/local/bin/runnerd
# A task's scripts call its tools with verse-call-tool, which the runner finds beside itself and
# binds into each task's sandbox.
COPY dist/linux/${TARGETARCH}/verse-call-tool /usr/local/bin/verse-call-tool

VOLUME ["/state"]
EXPOSE 9802

ENTRYPOINT ["runnerd"]
CMD ["-config", "/config/verse-runner.tony", "-sandbox", "bwrap"]
