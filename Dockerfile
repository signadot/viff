# verse runtime image — binaries are built LOCALLY (make dist-linux) and copied in, so
# `docker build` does no Go compilation and stays in sync with your local build cache.
# Build via `make docker` (which cross-compiles first); not a bare `docker build`.
#
# Bundles `verse` (engine + CLI), the pinned `o` (which `verse up` spawns for logd+docd), and
# `iod` (the shipped Sources: git, slack, linear); git is included for the git Source and
# for the mirror `iod git -mirror` clones; and `runnerd` (the shipped runners: verse), with
# bubblewrap, which confines each of a runner's tasks. One
# image, several roles — server, source connector, and (later) Jobs worker — because the
# deploy then has one image ref to pin and one build to cache. Separate PROCESSES, though: see
# cmd/iod on why a connector is not in the engine binary.
FROM debian:bookworm-slim
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git curl bubblewrap \
 && rm -rf /var/lib/apt/lists/*
COPY dist/linux/${TARGETARCH}/verse dist/linux/${TARGETARCH}/o dist/linux/${TARGETARCH}/iod \
     dist/linux/${TARGETARCH}/runnerd /usr/local/bin/
# logd+docd durable state — mount a volume here (the deploy StatefulSet does).
VOLUME ["/data"]
EXPOSE 9797
# Bare `docker run` = the monolith (the deploy overrides command/args per tier).
ENTRYPOINT ["verse"]
CMD ["up", "--data", "/data", "--o-bin", "/usr/local/bin/o", "--addr", ":9797"]
