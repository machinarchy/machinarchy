# =========================================================================
# STAGE 1: COMPILATION ENGINE & TELEMETRY INJECTION TRUCKS
# =========================================================================
FROM golang:1.26-alpine AS build

WORKDIR /src

# Core Idempotency Layer: Isolate package download cache strategies completely
COPY go.mod go.sum ./
RUN go mod download

# Senior Optimization: Construct output architecture layout directories BEFORE 
# copying code to preserve pristine cache layer boundaries.
RUN mkdir -p /out/data

# Copy source tree files
COPY . .

# Dynamic Telemetry Metadata Arguments Ingestions
ARG VERSION="dev"
ARG COMMIT="unknown"
ARG BUILD_TIME="unknown"

# Parameterized Runtime Entry Targets Configuration
ARG MACHINARCHY_COMMAND=monolit

# Senior Fix: Added 'agent' context alignment support inside validation statements 
# and mapped out your production-grade ldflags variables natively.
RUN case "$MACHINARCHY_COMMAND" in api|controlmanager|monolit|machinarchy|agent) ;; *) echo "::error::Unsupported system compilation target: $MACHINARCHY_COMMAND" >&2; exit 1 ;; esac \
    && CGO_ENABLED=0 go build -trimpath \
       -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildTime=${BUILD_TIME}" \
       -o /out/machinarchy ./cmd/"$MACHINARCHY_COMMAND"

# =========================================================================
# STAGE 2: PRODUCTION HARDENED RUNTIME (DISTROLESS SECURE BASELINE)
# =========================================================================
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /

# Map explicitly to the official system static nonroot group identities
COPY --from=build --chown=65532:65532 /out/machinarchy /machinarchy
COPY --from=build --chown=65532:65532 /out/data /data

# System Invariant Execution Defaults
ENV MACHINARCHY_LISTEN_ADDR=:8080 \
    MACHINARCHY_DATABASE_ENGINE=sqlite \
    MACHINARCHY_DATABASE_PATH=/data/machinarchy.db

EXPOSE 8080

# Enforce secure system non-privileged runtime profiles explicitly
USER 65532:65532

ENTRYPOINT ["/machinarchy"]
