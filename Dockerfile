# syntax=docker/dockerfile:1

# ---- Build stage ----
FROM golang:1.23-alpine AS build
WORKDIR /src

# Pure-Go build (modernc SQLite) so no C toolchain is required.
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
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

ENTRYPOINT ["/autofilemover"]
