# syntax=docker/dockerfile:1

# ---- Build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# Pure-Go build (modernc SQLite) so no C toolchain is required.
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
ARG CHANNEL=local
ARG COMMIT=unknown
ARG DATE=unknown
RUN go build -trimpath -ldflags "-s -w \
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
