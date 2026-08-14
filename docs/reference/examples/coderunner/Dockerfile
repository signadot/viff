# The coderunner image — a runner that codes, and the toolchain the work needs.
#
# Built via `make coderunner-docker` (which cross-compiles first); not a bare `docker build`,
# for the reason the root Dockerfile gives: binaries are built LOCALLY and copied in, so the
# image stays in sync with your build cache and does no Go compilation.
#
# It is a SEPARATE image from the verse runtime, deliberately. That one is thin on purpose —
# debian-slim plus three binaries — and its header explains that one image with several roles
# is what keeps the deploy to a single ref to pin. This is the opposite kind of thing: its size
# is set by what the repositories it serves need in order to build, so pinning it to the
# engine's release cadence would be wrong in both directions.
FROM debian:bookworm-slim
ARG TARGETARCH

# bubblewrap is the containment, and the reason this image exists rather than the runner being
# a process on somebody's laptop. `-confine bwrap` gives a task a read-only system rootfs with
# its workspace as the only writable path, plus pid/ipc/uts namespaces and — when the
# environment asks — no network at all.
#
# It needs unprivileged user namespaces, which is a kernel switch that varies by distribution
# and is disabled by some hardened kernels and some container runtimes. Where it is off, the
# runner REFUSES the containment rather than running work without it, so a wrong assumption
# here surfaces as a failed spawn naming the missing rung rather than as an escape.
#
# git is NOT here for the tasks. This runner speaks git through go-git — in the task process now,
# under a credential released per approved push — so `git` is absent from a task's PATH on
# purpose: the shell the model drives has no credential and no reason to run it. It is installed
# only because parts of a toolchain shell out to it (go, for module fetches).
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bubblewrap ca-certificates git curl \
 && rm -rf /var/lib/apt/lists/*

# The toolchain an environment's `commands:` claims. That field is a claim about THIS image and
# nothing checks the two agree — an environment naming a binary absent here produces a task
# whose commands are not found, with no better diagnosis than the agent's confusion. Keep them
# in step by hand, or narrow both.
ARG GO_VERSION=1.26.0
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
      | tar -C /usr/local -xz \
 && ln -s /usr/local/go/bin/go /usr/local/bin/go \
 && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt

# Under /usr/local/bin deliberately: each task runs THIS binary again, as `coderunner -task`,
# inside the sandbox — so the program has to exist there, and /usr is what the bwrap confiner
# binds read-only. A binary installed outside /usr would start fine and then fail to spawn
# anything.
COPY dist/linux/${TARGETARCH}/coderunner /usr/local/bin/coderunner

# Mirrors and workspaces. A volume, because a mirror is a cache worth keeping across restarts
# (it is the whole reason a task's clone costs no network) and a workspace is state a failed
# delivery deliberately leaves behind for somebody to look at.
VOLUME ["/state"]
EXPOSE 9802

# No default config: an environment names a repository and a push target, which is a deployment's
# to state and not an image's to guess. Mount one and point at it.
ENTRYPOINT ["coderunner"]
CMD ["-config", "/config/coderunner.tony", "-data", "/state", "-addr", ":9802", "-confine", "bwrap"]
