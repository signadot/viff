# The claude runner image: the verse runner image with Claude Code in it.
#
# Built via `make claude-runner-docker`; it COPYs the same locally cross-compiled runnerd the
# other images do, for the reason that Dockerfile gives. It is a separate image because what it
# adds is sized by a different thing again: a three-hundred-megabyte agent binary pinned to the
# version the claude agent was written against (runner/runnerd/claude), which neither the
# runtime image nor the plain runner has any reason to carry.
FROM debian:bookworm-slim
ARG TARGETARCH

# bubblewrap is the containment; git is for the shell and the toolchains that shell out to it;
# ripgrep is what Claude's Grep tool reaches for, and a search that falls back to grep is a
# slower agent rather than a broken one, so this is a convenience and not a requirement.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bubblewrap ca-certificates git curl ripgrep \
 && rm -rf /var/lib/apt/lists/*

ARG GO_VERSION=1.27.1
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
      | tar -C /usr/local -xz \
 && ln -s /usr/local/go/bin/go /usr/local/bin/go \
 && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt

# Claude Code, PINNED. The agent speaks this version's stream protocol; the message shapes it
# reads were read from this binary, and a newer one is a newer protocol until somebody checks.
# The release is one native binary per target, fetched from where `claude.ai/install.sh`
# fetches it and checked against the same manifest, so there is no node and no npm here. Under
# /usr/local/bin for the reason the runner is: /usr is what bwrap binds into the sandbox, and a
# binary installed anywhere else starts nowhere.
#
# The download retries, over HTTP/1.1: the CDN drops a long HTTP/2 stream partway and answers a
# quick retry with 429, and a truncated binary fails its checksum below rather than installing.
ARG CLAUDE_VERSION=2.1.304
RUN case "${TARGETARCH}" in \
      amd64) CLAUDE_PLATFORM=linux-x64 ;; \
      arm64) CLAUDE_PLATFORM=linux-arm64 ;; \
      *) echo "no claude build for ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && base="https://downloads.claude.ai/claude-code-releases/${CLAUDE_VERSION}" \
 && fetch="curl -fsSL --http1.1 --retry 5 --retry-all-errors --retry-delay 5" \
 && sum="$(${fetch} "${base}/manifest.json" | tr -d ' \n' \
      | grep -o "\"${CLAUDE_PLATFORM}\":{[^{}]*\"checksum\":\"[a-f0-9]\{64\}\"" | grep -o '[a-f0-9]\{64\}')" \
 && test -n "${sum}" \
 && ${fetch} "${base}/${CLAUDE_PLATFORM}/claude" -o /usr/local/bin/claude \
 && echo "${sum}  /usr/local/bin/claude" | sha256sum -c - \
 && chmod 0755 /usr/local/bin/claude \
 && claude --version

COPY dist/linux/${TARGETARCH}/runnerd /usr/local/bin/runnerd
# A task's scripts call its tools with verse-call-tool, which the runner finds beside itself and
# binds into each task's sandbox.
COPY dist/linux/${TARGETARCH}/verse-call-tool /usr/local/bin/verse-call-tool

VOLUME ["/state"]
EXPOSE 9802

ENTRYPOINT ["runnerd"]
CMD ["-config", "/config/verse-runner.tony", "-sandbox", "bwrap"]
