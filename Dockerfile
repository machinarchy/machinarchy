# syntax=docker/dockerfile:1
#
# Machinarchy server and CLI images.
#
#   docker buildx build --build-context agent-protocol=../machinarchy-agent/protocol \
#     --build-arg MACHINARCHY_COMMAND=monolith -t machinarchy-monolith .
#   docker buildx build --build-context agent-protocol=../machinarchy-agent/protocol \
#     --target cli -t machinarchy-cli .
#
# The agent wire contract (github.com/machinarchy/machinarchy-agent/protocol)
# comes from the sibling checkout through the named build context
# "agent-protocol"; go.mod replaces it with ../machinarchy-agent/protocol.
#
# Base images are pinned by digest (multi-arch index). Update them together
# with the tag in the comment; Dependabot (docker ecosystem) proposes bumps.

# golang:1.27.4-alpine
FROM --platform=$BUILDPLATFORM golang@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src/machinarchy
COPY --from=agent-protocol . /src/machinarchy-agent/protocol
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg
COPY docs/embed.go ./docs/
COPY docs/api/swagger.json ./docs/api/

# Reproducible builds: no VCS stamping, trimmed paths, and version metadata
# only from these arguments (BUILD_TIME should be the commit time).
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
ENV CGO_ENABLED=0 GOFLAGS="-trimpath -buildvcs=false"
ENV BUILDINFO=github.com/machinarchy/machinarchy/internal/buildinfo

FROM build AS build-server
ARG MACHINARCHY_COMMAND=monolith
# "monolit" is the deprecated name of "monolith", accepted for one release.
RUN case "$MACHINARCHY_COMMAND" in api|controlmanager|monolith|monolit) ;; \
      *) echo "MACHINARCHY_COMMAND must be api, controlmanager or monolith (use --target cli for the CLI)" >&2; exit 1 ;; esac
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    command=$MACHINARCHY_COMMAND; [ "$command" = monolit ] && command=monolith; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
      -ldflags="-s -w -X ${BUILDINFO}.Version=${VERSION} -X ${BUILDINFO}.Commit=${COMMIT} -X ${BUILDINFO}.BuildTime=${BUILD_TIME}" \
      -o /out/machinarchy ./cmd/"$command" \
    && install -d -m 0700 /out/rootfs/data

FROM build AS build-cli
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
      -ldflags="-s -w -X ${BUILDINFO}.Version=${VERSION} -X ${BUILDINFO}.Commit=${COMMIT} -X ${BUILDINFO}.BuildTime=${BUILD_TIME}" \
      -o /out/machinarchy ./cmd/machinarchy

# gcr.io/distroless/static-debian12:nonroot
FROM gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2 AS cli
COPY --from=build-cli /out/machinarchy /machinarchy
USER 65532:65532
ENTRYPOINT ["/machinarchy"]

# Default target: the server process selected by MACHINARCHY_COMMAND.
# gcr.io/distroless/static-debian12:nonroot
FROM gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2 AS server
COPY --from=build-server /out/machinarchy /machinarchy
# Copy the parent so /data keeps its 0700 mode (the SQLite store requires it).
COPY --from=build-server --chown=65532:65532 /out/rootfs/ /
ENV MACHINARCHY_ENVIRONMENT=production \
    MACHINARCHY_LISTEN_ADDR=:8080 \
    MACHINARCHY_DATABASE_ENGINE=sqlite \
    MACHINARCHY_DATABASE_PATH=/data/machinarchy.db \
    MACHINARCHY_SHUTDOWN_TIMEOUT=25s
EXPOSE 8080
USER 65532:65532
STOPSIGNAL SIGTERM
# The binary probes its own /healthz/ready (the image has no shell or curl).
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=3 CMD ["/machinarchy", "healthcheck"]
ENTRYPOINT ["/machinarchy"]
