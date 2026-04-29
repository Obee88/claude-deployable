# syntax=docker/dockerfile:1
#
# Multi-stage build for services/hello.
#
# Build context is the repo root (so go.mod is reachable). The
# GitHub Actions deploy workflow does:
#   docker build -f services/hello/Dockerfile --build-arg VERSION=<sha> .
#
# Image is intentionally minimal but keeps `wget` (busybox in alpine)
# so HEALTHCHECK can curl /healthz from inside the container without
# pulling in extra layers. M3's /containers/{name}/health reads
# State.Health.Status from `docker inspect`, which depends on this
# HEALTHCHECK existing.

FROM golang:1.23-alpine AS build
WORKDIR /src

# Layer caching: copy module files first; copy source second.
COPY go.mod ./
RUN go mod download

COPY services/ ./services/

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/hello \
    ./services/hello

FROM alpine:3.20

# wget is part of busybox on alpine, so HEALTHCHECK works without
# adding a package. Run as a non-root user — Alpine's `nobody` (UID
# 65534) is fine; the binary doesn't need to write anywhere.
COPY --from=build /out/hello /usr/local/bin/hello
USER nobody

EXPOSE 8080

# A passing healthcheck is the M3 deploy gate. Tune values in compose
# if a downstream service has a slower cold start.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/hello"]
