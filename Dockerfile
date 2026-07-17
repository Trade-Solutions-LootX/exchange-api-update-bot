# syntax=docker/dockerfile:1

# ---- Build stage ------------------------------------------------------------
# Pinned Go version for reproducible builds. The project has ZERO third-party
# Go dependencies, so no module download step is required and the build is
# fully hermetic.
FROM golang:1.24-alpine AS build

WORKDIR /src

# Copy the module definition first for better layer caching.
COPY go.mod ./
# (No go.sum — the project uses only the standard library.)

COPY . .

# Static, stripped binary. CGO disabled so it runs on scratch/distroless.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/bot ./cmd/bot

# Run vet + the (hermetic, network-free) test suite as part of the image build
# so a broken build never ships.
RUN go vet ./... && go test ./...

# Pre-create the state dir with the nonroot uid so a first-mounted named volume
# inherits writable ownership (uid 65532 == distroless nonroot).
RUN mkdir -p /data && chown -R 65532:65532 /data

# ---- Runtime stage ----------------------------------------------------------
# Distroless static: no shell, no package manager — minimal attack surface.
# It ships CA certificates (needed for HTTPS to the exchange APIs) and runs as
# a non-root user by default.
FROM gcr.io/distroless/static-debian12:nonroot

# State volume for the "seen" set so restarts never re-send old news. Ownership
# is carried over from the build stage so the nonroot user can write to it when
# a fresh NAMED volume is mounted here. (For a host bind-mount, chown the host
# directory to 65532:65532 yourself — Docker does not copy ownership for those.)
COPY --from=build --chown=65532:65532 /data /data
VOLUME ["/data"]

ENV STATE_PATH=/data/state.json \
    HEALTH_ADDR=:8080

EXPOSE 8080

COPY --from=build /out/bot /bot

# Distroless nonroot uid is 65532.
USER 65532:65532

# Self-probe via the binary (no shell/curl available in distroless).
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD ["/bot", "healthcheck"]

ENTRYPOINT ["/bot"]
