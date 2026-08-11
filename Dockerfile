# syntax=docker/dockerfile:1

# cmd/bridge is the only thing this image ships — the persistent Bridge
# daemon plus its admin web UI (ADR 0015). cmd/swarm-bridge and the other
# one-shot CLIs stay host-side developer tools; they are not built here.

FROM golang:1.24-alpine AS build
WORKDIR /src

# Dependency-free build (see go.mod): no go.sum to prime a separate layer
# with, so the whole module is copied up front.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/bridge ./cmd/bridge

# distroless/nonroot's image has no shell to `mkdir`/`chown` a persistent
# data directory at runtime, and a freshly created Docker volume otherwise
# starts out root-owned — which the nonroot (uid 65532) process can't write
# to. Docker seeds a new named/anonymous volume's ownership from whatever is
# already at the mount point in the image, so an empty, correctly-owned
# /data here is what makes the volume usable on first boot.
RUN mkdir -p /out/data && chown 65532:65532 /out/data

# Distroless, non-root: minimal attack surface for a process holding a RomM
# API token. No shell, so HEALTHCHECK below runs the binary's own
# `-healthcheck` mode instead of shelling out to curl/wget.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bridge /bridge
COPY --from=build --chown=65532:65532 /out/data /data

EXPOSE 8080
VOLUME ["/data"]

ENV BRIDGE_CONFIG_DIR=/data
ENV BRIDGE_LISTEN_ADDR=:8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/bridge", "-healthcheck", "-addr", ":8080"]

ENTRYPOINT ["/bridge"]
