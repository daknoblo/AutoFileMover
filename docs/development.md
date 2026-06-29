# Development

## Prerequisites

- Go 1.25+
- Docker (optional, for container builds)
- golangci-lint (optional, for linting)

## Run locally

```bash
mkdir -p devmedia/Downloads devmedia/Filme data
AFM_MEDIA_ROOT=devmedia go run ./cmd/autofilemover
# UI: http://localhost:8080
```

The repo includes a dev container (`.devcontainer/`) with the Go toolchain.

## Common tasks

```bash
make build   # static binary with version metadata
make vet      # go vet ./...
make test     # go test ./...
make lint     # golangci-lint run
make docker   # build container image
```

## Security notes

- No built-in authentication. Run on a trusted network or behind a reverse
  proxy / VPN; do not expose it directly to the internet.
- The API key is stored in the database and only ever reported as "set" — never
  returned to the UI.
- All UI paths are validated to stay inside `AFM_MEDIA_ROOT`; file actions only
  apply to files already discovered by the scanner.
- Deletions are permanent. Use what-if mode to preview a plan before applying.
- The container runs as a non-root distroless image with its own healthcheck.

## Tests

```bash
go test ./...
```

Covers the file mover (move/delete/cleanup) and the AI per-file response parser.
