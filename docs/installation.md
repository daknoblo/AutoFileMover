# Installation

AutoFileMover ships as a small, static Go binary in a distroless container. The
recommended way to run it is Docker Compose.

## Quick start (Docker Compose)

1. Grab `docker-compose.yml` and adjust the media volume to your setup.
2. Start it:

   ```bash
   docker compose up -d
   ```

3. Open the UI at <http://localhost:8080>.

```yaml
services:
  autofilemover:
    image: ghcr.io/daknoblo/autofilemover:latest
    container_name: autofilemover
    restart: unless-stopped
    ports:
      - "8080:8080"
    # Match your media owner so moved files keep correct permissions.
    # user: "1000:1000"
    volumes:
      - ./data:/data                       # SQLite database (persisted)
      - /path/to/media:/dataroot           # download + library root
```

> Everything under the mounted media root (`/dataroot`) is browsable in the UI.
> Both the source (download) folder and the target libraries must live inside it.

## First-run setup

1. **Source folder** — pick the folder your downloads land in.
2. **Libraries** — add one target per type, e.g. `Filme` (movie), `Serien`
   (series), `Dokus` (documentary). For series, the show sub-folders must
   already exist; files are sorted into the matching one.
3. **Settings** — configure the AI endpoint, threshold and ignore patterns.

## Build locally

```bash
make build          # static binary in bin/
make docker         # local image
go run ./cmd/autofilemover
```

## Permissions

Moved files are owned by the user the container runs as. Set `user: "PUID:PGID"`
in compose to match your media owner so Jellyfin keeps read access.
