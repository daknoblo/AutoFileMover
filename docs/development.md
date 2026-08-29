# Development

## Prerequisites

- Go 1.27+
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
make demo         # seeded demo instance on http://127.0.0.1:8099
make screenshots  # regenerate docs/images/*.png and docs/demo.md
make site         # render README + docs/ into site/
```

## Demo instance, screenshots & website

`cmd/afm-demo` starts a throw-away instance seeded by `internal/demo`: a sample
media tree under `/tmp/afm-demo/dataroot`, three libraries, a review queue with a
collision and an AI error, a history and a fixed log. It never calls an AI
endpoint and never watches the filesystem, so the state stays stable.

`make screenshots` starts that instance inside the pinned Playwright container
(`tools/screenshots/run.sh`), captures every section listed in
`tools/screenshots/shots.mjs` and writes `docs/images/*.png` plus the generated
`docs/demo.md`. Running it in the container keeps fonts, browser build and the
`/dataroot` paths identical everywhere — screenshots taken on the host would
differ. The container is pinned to `linux/amd64` to match CI; set
`AFM_SHOT_PLATFORM=linux/arm64` for a faster native preview run. CI is
authoritative: it regenerates the screenshots on `main` and commits them if its
rendering differs by a pixel. Adding a new UI section only means adding one
entry to `shots.mjs`.

`make site` renders `README.md` and `docs/*.md` into the static site in `site/`
(`tools/site` is a separate Go module so the service keeps its dependency set).
The `Docs & demo` workflow regenerates the screenshots on every push to `main`,
commits them when they changed and deploys the site to GitHub Pages. Publishing
requires *Settings → Pages → Source: GitHub Actions* to be enabled once.

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
