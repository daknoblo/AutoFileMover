# syntax=docker/dockerfile:1

# ---- Build stage ----
# Run the Go compiler on the runner's NATIVE architecture and cross-compile for
# the requested target platform. CGO is off and modernc SQLite is pure Go, so a
# cross-compile is trivial and avoids slow QEMU emulation for the arm64 image.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src

# Pure-Go build (modernc SQLite) so no C toolchain is required.
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG CHANNEL=local
ARG COMMIT=unknown
ARG DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags "-s -w \
      -X github.com/daknoblo/AutoFileMover/internal/version.Version=${VERSION} \
      -X github.com/daknoblo/AutoFileMover/internal/version.Channel=${CHANNEL} \
      -X github.com/daknoblo/AutoFileMover/internal/version.Commit=${COMMIT} \
      -X github.com/daknoblo/AutoFileMover/internal/version.Date=${DATE}" \
    -o /out/autofilemover ./cmd/autofilemover

# ---- Runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/autofilemover /autofilemover

# Data volume holds the SQLite database; media is bind-mounted at runtime.
ENV AFM_HTTP_ADDR=:8080 \
    AFM_DB_PATH=/data/autofilemover.db \
    AFM_MEDIA_ROOT=/dataroot
VOLUME ["/data"]
EXPOSE 8080

# The binary implements its own healthcheck (distroless has no curl/wget).
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/autofilemover", "-healthcheck"]

ENTRYPOINT ["/autofilemover"]
