# The verse runner image — a runner that codes, and the toolchain the work needs.
#
# Built via `make runner-docker` (which cross-compiles first); not a bare `docker build`, for
# the reason build/Dockerfile gives: binaries are built LOCALLY and copied in, so the image
# stays in sync with your build cache and does no Go compilation.
#
# It is a SEPARATE image from the verse runtime, deliberately. That one is thin on purpose —
# debian-slim plus three binaries — and its header explains that one image with several roles
# is what keeps the deploy to a single ref to pin. This is the opposite kind of thing: its size
# is set by what the repositories it serves need in order to build, so pinning it to the
# engine's release cadence would be wrong in both directions.
FROM debian:bookworm-slim
ARG TARGETARCH

# bubblewrap is the containment, and the reason this image exists rather than the runner being
# a process on somebody's laptop. `-sandbox bwrap` gives a task a read-only system rootfs with
# its workspace as the only writable path, plus pid/ipc/uts namespaces and — when the
# launch asks — no network but its model's.
#
# It needs unprivileged user namespaces, which is a kernel switch that varies by distribution
# and is disabled by some hardened kernels and some container runtimes. Where it is off, the
# runner REFUSES the containment rather than running work without it, so a wrong assumption
# here surfaces as a failed spawn naming the missing rung rather than as an escape.
#
# git is here for the task's shell: an agent commits what it means to deliver and the runner
# pushes it from its own mirror, so the shell the model drives has git and no credential.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bubblewrap ca-certificates git curl \
 && rm -rf /var/lib/apt/lists/*

# The toolchain a task's shell finds on its PATH. What the charters spawned here run is a claim
# about THIS image, and nothing checks the two agree — a script naming a binary absent here fails
# with "command not found". The version is go.mod's: a task whose `go` has to download a
# toolchain asks a host its network may not admit.
ARG GO_VERSION=1.27.1
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
      | tar -C /usr/local -xz \
 && ln -s /usr/local/go/bin/go /usr/local/bin/go \
 && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt

# Under /usr/local/bin deliberately: each task runs THIS binary again, as `runnerd -task`,
# inside the sandbox — so the program has to exist there, and /usr is what the bwrap sandbox
# binds read-only. A binary installed outside /usr would start fine and then fail to spawn
# anything.
COPY dist/linux/${TARGETARCH}/runnerd /usr/local/bin/runnerd
# A task's scripts call its tools with verse-call-tool, which the runner finds beside itself and
# binds into each task's sandbox.
COPY dist/linux/${TARGETARCH}/verse-call-tool /usr/local/bin/verse-call-tool

# The mirrors (the config's `repos.mirrors`). A volume, because a mirror is a cache worth keeping
# across restarts — it is the whole reason a task's clone costs no network — and work a delivery
# could not land waits in it.
VOLUME ["/state"]
EXPOSE 9802

# No default config: what the runner offers, where it keeps its mirrors and which forge they are
# on are a deployment's to state and not an image's to guess. Mount one and point at it.
ENTRYPOINT ["runnerd"]
CMD ["-config", "/config/verse-runner.tony", "-sandbox", "bwrap"]
