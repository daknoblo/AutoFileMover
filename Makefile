# AutoFileMover — common development tasks.
APP        := autofilemover
PKG        := ./cmd/autofilemover
VERSION    ?= dev
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE       := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w \
  -X github.com/daknoblo/AutoFileMover/internal/version.Version=$(VERSION) \
  -X github.com/daknoblo/AutoFileMover/internal/version.Commit=$(COMMIT) \
  -X github.com/daknoblo/AutoFileMover/internal/version.Date=$(DATE)

.PHONY: build run vet test lint tidy docker

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP) $(PKG)

run:
	go run $(PKG)

vet:
	go vet ./...

test:
	go test ./...

lint:
	golangci-lint run

tidy:
	go mod tidy

docker:
	docker build -t $(APP):$(VERSION) .
