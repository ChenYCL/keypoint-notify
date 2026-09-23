# syntax=docker/dockerfile:1
#
# Two stages, and the first one is the only one that needs Go.
# CGO_ENABLED=0 works because the SQLite driver is pure Go (modernc.org/sqlite),
# so the runtime image needs no C library and no shell.

FROM golang:1.25-alpine AS build
WORKDIR /src

# Dependencies first: this layer only rebuilds when go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X github.com/ChenYCL/keypoint-notify/internal/cli.Version=${VERSION}" \
      -o /out/kp ./cmd/keypoint

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 -h /data kp
COPY --from=build /out/kp /usr/local/bin/kp

# The database and uploaded blobs both live here; mount a volume over it.
VOLUME /data
USER kp
EXPOSE 8787

# 0.0.0.0 inside the container is not a downgrade: the host decides what to
# publish, and docs/deploy-tunnel.md maps it to 127.0.0.1 on the host.
ENTRYPOINT ["kp", "serve", "--addr", "0.0.0.0:8787", "--data", "/data"]

# Readiness is the unauthenticated health endpoint, so no key is baked in.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:8787/api/v1/health || exit 1
