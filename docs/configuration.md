# Configuration

Infrastructure settings come from environment variables. Everything else (AI
endpoint, threshold, sources, libraries, language) is configured in the web UI
and stored in the SQLite database.

## Environment variables

| Variable               | Default                    | Description                                   |
| ---------------------- | -------------------------- | --------------------------------------------- |
| `AFM_HTTP_ADDR`        | `:8080`                    | Listen address of the web server.             |
| `AFM_DB_PATH`          | `/data/autofilemover.db`   | SQLite database path.                         |
| `AFM_MEDIA_ROOT`       | `/dataroot`                | Root of the mounted media volume.             |
| `AFM_STABILITY_WINDOW` | `30s`                      | Quiet time before a download is processed.    |
| `AFM_SCAN_INTERVAL`    | `5m`                       | Fallback periodic scan interval.              |
| `AFM_LOG_LEVEL`        | `info`                     | `debug`, `info`, `warn`, `error`.             |

All configured paths must stay **inside** `AFM_MEDIA_ROOT` and must exist.

## AI endpoint (Azure AI Foundry / Azure OpenAI / OpenAI)

| Field           | Example                                  |
| --------------- | ---------------------------------------- |
| Base URL        | `https://<resource>.openai.azure.com`    |
| Deployment      | `gpt-4o-mini`                            |
| Azure version   | `2024-06-01`                             |
| API key         | stored in the DB, never returned to UI   |

- With an API version set, Azure mode is used (`/openai/deployments/...`).
- Empty version uses the plain OpenAI path (`/v1/chat/completions`).

## Behaviour

- **Threshold** — minimum confidence for fully automatic processing.
- **Auto move** — when enabled and confidence ≥ threshold, the AI plan runs:
  the main media is moved, junk (sample/nfo/screenshots) is deleted, the empty
  source folder is removed.
- **What-if** — preview only; per-file buttons still let you move/delete
  manually.
- **Ignore patterns** — substring or glob, one per line. They skip top-level
  source folders (e.g. `_UNPACK`); files inside a folder are always listed so
  the AI can decide.

## Language

The UI ships English and German; switch in the header. The choice is stored in
the browser.
