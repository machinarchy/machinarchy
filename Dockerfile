FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG MACHINARCHY_COMMAND=api
RUN mkdir -p /out/data \
    && case "$MACHINARCHY_COMMAND" in api|controlmanager|monolit|machinarchy) ;; *) echo "unsupported MACHINARCHY_COMMAND: $MACHINARCHY_COMMAND" >&2; exit 1 ;; esac \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/machinarchy ./cmd/"$MACHINARCHY_COMMAND"

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /
COPY --from=build --chown=65532:65532 /out/machinarchy /machinarchy
COPY --from=build --chown=65532:65532 /out/data /data

ENV MACHINARCHY_LISTEN_ADDR=:8080 \
    MACHINARCHY_DATABASE_ENGINE=sqlite \
    MACHINARCHY_DATABASE_PATH=/data/machinarchy.db

EXPOSE 8080
ENTRYPOINT ["/machinarchy"]
