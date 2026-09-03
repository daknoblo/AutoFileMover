# AutoFileMover

[![CI](https://github.com/daknoblo/AutoFileMover/actions/workflows/ci.yml/badge.svg)](https://github.com/daknoblo/AutoFileMover/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/daknoblo/AutoFileMover)](https://github.com/daknoblo/AutoFileMover/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/daknoblo/AutoFileMover)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![GHCR](https://img.shields.io/badge/ghcr.io-autofilemover-blue?logo=docker)](https://github.com/daknoblo/AutoFileMover/pkgs/container/autofilemover)
[![Docs](https://img.shields.io/badge/docs-github%20pages-4f8cff)](https://daknoblo.github.io/AutoFileMover/)

A self-hosted service that detects downloaded media, classifies it semantically
through an **AI endpoint** and moves it into the matching **Jellyfin library**
(movies, series, documentaries).

Written in **Go**, runs as a **Docker container** and ships a web interface
(intended to run behind a reverse proxy).

📖 **Documentation & demo:** <https://daknoblo.github.io/AutoFileMover/>

> **Note on language:** the web interface is bilingual (German/English). Code,
> comments and this documentation are English.

## Screenshots

All screenshots are generated automatically from a demo instance, so they always
show the current interface — see [docs/demo.md](docs/demo.md) for every section.

[![Review queue](docs/images/review-queue.png)](docs/demo.md)

| Per-file decisions | Collisions |
| --- | --- |
| [![Per-file decisions](docs/images/review-files.png)](docs/demo.md) | [![Collision](docs/images/review-conflict.png)](docs/demo.md) |

## How it works

1. The media folder is mounted into the container (`/dataroot`). It contains the
   download folder as well as the target libraries.
2. A **watcher** (`fsnotify`) monitors the configured **source folders**. As soon
   as a download is "stable" (unchanged for a configurable period), it is
   processed.
3. The **scanner** reads the root folder (or the single file) including all files
   it contains.
4. This information is passed, together with the available libraries (and for
   series the existing series folders), to an **OpenAI-compatible AI endpoint**
   (e.g. **Azure AI Foundry / Azure OpenAI**). The AI returns the type, the target
   library, optionally a series folder, and a **confidence** (0–1).
5. If the confidence is **above the configured threshold** (default 90 %) and a
   target resolves unambiguously, the item is **moved automatically**.
6. Otherwise (too uncertain, no matching series folder, AI unreachable) the item
   ends up in the **review queue**, where it can be confirmed or rejected
   manually in the UI.

### Manual target & error cases

If the AI does not return a usable assessment (endpoint error, 0 % confidence or
no resolvable target), the review card automatically shows the manual **target
selection** (library plus optional series folder). Cards with an already resolved
target additionally offer "choose target manually" to override it. If the desired
target folder does not exist yet, the same selection lets you **create a new
folder** (enter a name → "create folder"; it is created directly below the
library and set as the target). Choosing or creating a target is itself the
decision to move: every affected file switches to **move** automatically, so the
plan can be executed right away — only files explicitly marked `delete` keep that
action. Setting or creating a target by hand also clears the error and puts the
item back into normal review.

Picking a target and toggling a per-file action are stored immediately and never
wait for the media storage, so they stay instant while a share is saturated. The
check whether the destination already holds a colliding file needs to read that
folder and is therefore queued: the conflict box appears a moment after the
choice. Applying the plan re-checks the destination itself, so nothing is moved
past an unreviewed collision.

A **failed** AI call is **not** retried automatically on the next scan — the
endpoint is therefore not queried endlessly. A new attempt only happens
explicitly via "AI match".

### Assignment rules

Per library, the **"use a subfolder per title"** checkbox controls how items are
filed (switchable in the settings at any time; default: off for movies, on for
series and documentaries):

- **Subfolder off** (typical for movies) → the item lands directly in the
  library's root folder.
- **Subfolder on** (typical for series/documentaries) → the item is only filed
  into an **already existing subfolder**. If the AI finds no matching one, the
  item goes to the review queue, where the folder can be selected or created
  manually.
- Files are **moved**; across different file systems this automatically falls
  back to copy + delete (cross-device fallback).

### Per-file decision

A source folder often contains several files (movie, sample, NFO …). The AI
evaluates **each file individually** and proposes an action with a confidence:

- `move` – the actual media file (largest video plus subtitles) → to the target.
- `delete` – sample clips, `.nfo`, screenshots, checksums → deleted permanently.
- `keep` – uncertain → stays for manual review.

The review queue shows the folder with all files, action labels and percentages.
On a confident auto-move the movie is moved, leftovers are deleted and the empty
source folder is removed; in what-if mode everything can be controlled per file
beforehand.

### Background queue

File operations never block the interface. Every action that touches the storage
— "execute plan", a single move/delete, creating a folder, an AI re-check — is
handed to a background queue and confirmed immediately, so all buttons stay
usable even while the underlying share is saturated.

The **Queue** tab lists every job with its state, attempt count, next retry and
error message, and lets you retry or cancel one. A badge in the header shows how
much work is still outstanding.

Once an action is queued, that card's buttons are **locked** until the worker is
done with it, so the queue stays the single source of truth for what is still
outstanding and a second click cannot pile up duplicate work. The card badge
shows whether the job is waiting, running or failed; a **failed** job leaves the
buttons usable, because that one needs a new decision rather than more waiting.
The destination check that runs after every plan change is background
housekeeping and never locks a card.

If the media root becomes unwritable (share offline, no free IOPS), the queue
**pauses** instead of failing: jobs are kept, no retry budget is consumed, and
processing resumes automatically once storage responds again. Transient I/O
errors are retried with an exponential backoff; errors that can never succeed
(missing file, no target selected) are marked failed and wait for you.

Because each completed file is persisted immediately, restarting the container
mid-transfer resumes the plan where it stopped rather than redoing it.

### Collisions with existing files

Before a file is moved, the service checks the target directory for an
**existing** file — either with an identical name or with the same episode
(`SxxExx`) under a different release name. If one is found, the item goes to the
review queue automatically (no auto-move, no overwriting).

There, a **side-by-side comparison** shows both candidates: file name, size and
the quality attributes derived from the release name (resolution, codec, HDR/DV,
source — entirely without `ffprobe`, so the image stays small). You decide per
conflict:

- **Take the new one (replace)** – the existing file is deleted and the new one
  is moved.
- **Keep the existing one** – the existing file stays and the new (duplicate) one
  is removed from the source folder.

As long as a conflict is unresolved, "execute plan" stays disabled.

### Language & About

The interface is bilingual (German/English, switch in the header). The **About**
tab shows version, commit, build date and Go version; header and footer link to
the repository.

## Quick start (Docker Compose)

Adjust `docker-compose.yml` (especially the media volume) and start it:

```bash
docker compose up -d
```

Then open the web UI: <http://localhost:8080>

In the UI:

1. Add a **source folder**, e.g. `/dataroot/Downloads`.
2. Add **libraries**, e.g.
   - `Movies` (movie) → `/dataroot/Movies`
   - `Series` (series) → `/dataroot/Series`
   - `Docs` (documentary) → `/dataroot/Documentaries`
3. Under **settings**, configure the AI endpoint and set the threshold as well as
   "automatic moving".

> All paths entered in the UI must live **inside** `AFM_MEDIA_ROOT` and must
> exist.

## Configuring the AI endpoint (Azure AI Foundry / Azure OpenAI)

| Field              | Example                                    |
| ------------------ | ------------------------------------------ |
| Base URL           | `https://<resource>.openai.azure.com`      |
| Deployment/model   | `gpt-4o-mini` (the name of your deployment)|
| Azure API version  | `2024-06-01`                               |
| API key            | your Azure key                             |

- If an **API version** is set, **Azure mode** is used
  (`/openai/deployments/<model>/chat/completions?api-version=...`, header
  `api-key`).
- If the API version stays **empty**, the standard OpenAI path is used
  (`/v1/chat/completions`, header `Authorization: Bearer ...`). The base URL is then
  e.g. `https://api.openai.com/v1`.

The API key is stored in the database and only shown as "set" in the UI, never
returned.

## Configuration (environment variables)

| Variable                | Default                       | Description                                          |
| ----------------------- | ----------------------------- | ---------------------------------------------------- |
| `AFM_HTTP_ADDR`         | `:8080`                       | Listen address of the web server                     |
| `AFM_DB_PATH`           | `/appdata/autofilemover.db`   | Path of the SQLite database                          |
| `AFM_MEDIA_ROOT`        | `/dataroot`                   | Root of the mounted media directory                  |
| `AFM_STABILITY_WINDOW`  | `30s`                         | Quiet period before a download is processed          |
| `AFM_SCAN_INTERVAL`     | `5m`                          | Fallback interval for periodic scans                 |
| `AFM_LOG_LEVEL`         | `info`                        | `debug`, `info`, `warn`, `error`                     |

Application settings (AI config, threshold, sources, libraries) are stored in the
database and managed through the UI.

## Local development (dev container)

The repository contains a dev container (`.devcontainer/devcontainer.json`) with
the Go toolchain. Open it in VS Code via "Reopen in Container", then:

```bash
mkdir -p devmedia/Downloads devmedia/Movies data
go run ./cmd/autofilemover
```

The UI is then reachable on port `8080`. The dev container sets `AFM_MEDIA_ROOT`
to `devmedia` and a short stability window.

### Build & tests

```bash
go build ./...
go vet ./...
go test ./...
```

## Documentation

The full documentation is published at
<https://daknoblo.github.io/AutoFileMover/>.

- [docs/demo.md](docs/demo.md) — screenshots of every section
- [docs/installation.md](docs/installation.md)
- [docs/configuration.md](docs/configuration.md)
- [docs/architecture.md](docs/architecture.md)
- [docs/development.md](docs/development.md)

## Security

- No built-in authentication — run it behind a reverse proxy or VPN.
- The API key is stored in the database and never returned to the UI.
- Paths are validated against `AFM_MEDIA_ROOT`; file actions only apply to
  already scanned files. Deletion is permanent (use what-if first).
- The container runs as a non-root distroless image with its own healthcheck.

## Container image (GitHub Actions)

`.github/workflows/ci.yml` checks vet, lint, tests and static builds.
`.github/workflows/release.yml` builds and publishes the container image to
**GHCR** (`ghcr.io/<owner>/<repo>`) for `linux/amd64` and `linux/arm64`,
generates SBOM and provenance, signs keyless with cosign and uploads the Trivy
scan as SARIF.
`.github/workflows/docs.yml` regenerates the screenshots and the demo page and
deploys the documentation site to GitHub Pages.

- `main`: tag `stable`
- `develop`: tag `dev`
- Releases: via git tag `vX.Y.Z` (semver tags are generated)

## Project structure

```
cmd/autofilemover/    # main, wiring & graceful shutdown
cmd/afm-demo/         # seeded demo instance (screenshots, playground)
internal/config/      # env configuration
internal/store/       # SQLite (settings, sources, libraries, items)
internal/ai/          # OpenAI/Azure compatible client + classifier
internal/scanner/     # download detection & file reading
internal/mover/       # moving with cross-device fallback
internal/engine/      # orchestration: scan → classify → decide → move/queue
internal/watcher/     # fsnotify monitoring + periodic scan
internal/web/         # REST API + embedded web UI (DE/EN)
internal/demo/        # sample data for the demo instance
internal/version/     # build metadata (ldflags)
tools/screenshots/    # Playwright capture -> docs/images + docs/demo.md
tools/site/           # static site generator for GitHub Pages
```

## Notes on permissions

Moved files belong to the user the container runs as. For correct Jellyfin
permissions, set `user: "PUID:PGID"` in `docker-compose.yml` to match your media
folder.

## License

Released under the [MIT License](LICENSE).
