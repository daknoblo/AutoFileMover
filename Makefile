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

.PHONY: build run vet test lint tidy docker demo screenshots site

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

# Seeded playground instance on http://127.0.0.1:8099 (no AI, no watcher).
demo:
	go run ./cmd/afm-demo

# Regenerates docs/images/*.png and docs/demo.md (runs in the Playwright container).
screenshots:
	bash tools/screenshots/run.sh

# Renders README + docs/ into the static website in site/.
site:
	cd tools/site && go run . -repo ../.. -out ../../site
