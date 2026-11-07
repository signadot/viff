# Newer bubblewrap built from source, to compare with bookworm's 0.8.0: /usr/local/bin/bwrap-new is
# v0.13.1 as released; /usr/local/bin/bwrap-big is the same with MAX_ARGS raised from 9000 to
# 200000 (a one-line patch), to measure what the kernel does past bwrap's own ceiling.
FROM debian:bookworm-slim AS build
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git meson ninja-build gcc pkg-config libcap-dev libselinux1-dev \
 && rm -rf /var/lib/apt/lists/*
ARG BWRAP_REF=v0.13.1
RUN git clone --depth 1 --branch ${BWRAP_REF} https://github.com/containers/bubblewrap /src \
 && grep -n "MAX_ARGS" /src/bubblewrap.c | tee /maxargs.txt \
 && cd /src && meson setup _build -Dman=disabled -Dtests=false -Dselinux=disabled && ninja -C _build \
 && cp _build/bwrap /bwrap-new \
 && sed -i 's/MAX_ARGS = 9000;/MAX_ARGS = 200000;/' bubblewrap.c && grep -n "MAX_ARGS = " bubblewrap.c \
 && ninja -C _build && cp _build/bwrap /bwrap-big
FROM pkgnix-spike:local
COPY --from=build /bwrap-new /usr/local/bin/bwrap-new
COPY --from=build /bwrap-big /usr/local/bin/bwrap-big
COPY --from=build /maxargs.txt /usr/local/share/bwrap-new-maxargs.txt
