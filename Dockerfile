# verse runtime image — binaries are built LOCALLY (make dist-linux) and copied in, so
# `docker build` does no Go compilation and stays in sync with your local build cache.
# Build via `make docker` (which cross-compiles first); not a bare `docker build`.
#
# Bundles `verse` (engine + CLI) and the pinned `o` (which `verse up` spawns for logd+docd);
# git is included for the git Source. One image, two roles — server and (later) Jobs worker.
FROM debian:bookworm-slim
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git curl \
 && rm -rf /var/lib/apt/lists/*
COPY dist/linux/${TARGETARCH}/verse dist/linux/${TARGETARCH}/o /usr/local/bin/
# logd+docd durable state — mount a volume here (the deploy StatefulSet does).
VOLUME ["/data"]
EXPOSE 9797 9090
# Bare `docker run` = the monolith (the deploy overrides command/args per tier).
ENTRYPOINT ["verse"]
CMD ["up", "--data", "/data", "--o-bin", "/usr/local/bin/o", "--addr", ":9797", "--broker-addr", ":9090"]
