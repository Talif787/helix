# syntax=docker/dockerfile:1

# --- build stage -----------------------------------------------------------
# Pin the builder to Go 1.22 and force the local toolchain, matching CI, so a module that
# requests a newer Go cannot silently pull a different toolchain.
FROM golang:1.22 AS builder
WORKDIR /src

# Build entirely from the committed vendor/ tree, so the image build needs no network. This
# matters in environments where BuildKit's build sandbox cannot reach the Go module proxy.
COPY . .
ENV GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=vendor
RUN go build -trimpath -o /out/kvnode ./cmd/kvnode \
 && go build -trimpath -o /out/helixctl ./cmd/helixctl

# --- runtime stage ---------------------------------------------------------
# Alpine keeps the image small and provides a busybox wget for the healthcheck. The binaries
# are static (CGO disabled), so they run without glibc.
FROM alpine:3.20
RUN adduser -D -u 10001 helix \
 && mkdir -p /data \
 && chown helix /data
COPY --from=builder /out/kvnode /usr/local/bin/kvnode
COPY --from=builder /out/helixctl /usr/local/bin/helixctl

USER helix
WORKDIR /data
VOLUME ["/data"]

# 7070 is the gRPC listener (node, membership, and client planes); 9090 is the metrics/health
# HTTP server. Both are only exposed when the matching HELIX_* addresses are configured.
EXPOSE 7070 9090

ENTRYPOINT ["kvnode"]
