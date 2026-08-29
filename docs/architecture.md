# Architecture

AutoFileMover is a single Go binary with an embedded web UI. It watches source
folders, classifies each item per file via an AI endpoint and moves wanted media
into the matching library.

## Packages

| Package             | Responsibility                                                    |
| ------------------- | ----------------------------------------------------------------- |
| `cmd/autofilemover` | Wiring, graceful shutdown, container healthcheck (`-healthcheck`). |
| `internal/config`   | Environment configuration.                                        |
| `internal/store`    | SQLite (settings, sources, libraries, items, folder notes, jobs). |
| `internal/ai`       | OpenAI/Azure-compatible client + classifier (per-file decisions). |
| `internal/scanner`  | Detects stable downloads, lists all contained files.              |
| `internal/mover`    | Move (cross-device safe), delete, remove-if-empty.                |
| `internal/engine`   | Orchestrates scan → classify → plan → execute.                    |
| `internal/queue`    | Background worker for all filesystem work (retry, health gate).   |
| `internal/watcher`  | fsnotify + periodic fallback scan.                                |
| `internal/web`      | REST API + embedded SPA (vanilla JS, EN/DE i18n).                 |
| `internal/version`  | Build metadata injected via `-ldflags`.                           |

## Flow

```
download → watcher → scanner → engine → ai.Classify
                                   │
                                   ├─ per-file plan (move / delete / keep)
                                   ├─ confidence ≥ threshold & auto → execute
                                   └─ otherwise → review queue
```

## Per-file model

Each item (a folder or a loose file) carries a list of files; the AI assigns
each `move`, `delete` or `keep` with a probability. On execution the engine
moves wanted files, deletes junk and removes the emptied source folder. In the
review queue every file can be confirmed individually.

## Background queue

No HTTP request ever touches the media storage. Every filesystem action
(apply plan, single file action, create folder, AI re-check) is written to the
`jobs` table and answered with `202 Accepted`; a single worker executes them one
at a time. This keeps the UI responsive when the backing share is saturated.

```
click → POST /api/items/{id}/… → jobs row → 202 (UI stays responsive)
                                    │
                              queue worker (serial)
                                    ├─ health gate: media root writable?
                                    │    no  → pause, no attempt consumed
                                    ├─ run the engine operation
                                    ├─ transient error → exponential backoff
                                    └─ permanent error → failed, retry by hand
```

The worker is deliberately serial: the storage backend is the bottleneck, so
parallel jobs would only add contention. A job left `running` by a restart is
reset to `pending`; because every completed file is persisted immediately, a
resumed plan continues where it stopped instead of redoing work.

## Concurrency

Work is serialized per item, keyed by source path, not globally. A scan, an AI
call or a large move therefore only blocks actions on that one item. Database-
only actions in the request path take the item lock with a short timeout and
answer `409` rather than waiting, so a click always gets an immediate response.

## Storage

A pure-Go SQLite database (`modernc.org/sqlite`, no CGO) holds all state. It
contains the AI API key, so the file is created with `0600` permissions. Media
is bind-mounted at `/dataroot`; paths are validated to stay within it.
