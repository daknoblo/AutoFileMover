# Architecture

AutoFileMover is a single Go binary with an embedded web UI. It watches source
folders, classifies each item per file via an AI endpoint and moves wanted media
into the matching library.

## Packages

| Package             | Responsibility                                                    |
| ------------------- | ----------------------------------------------------------------- |
| `cmd/autofilemover` | Wiring, graceful shutdown, container healthcheck (`-healthcheck`). |
| `internal/config`   | Environment configuration.                                        |
| `internal/store`    | SQLite (settings, sources, libraries, items, folder notes).       |
| `internal/ai`       | OpenAI/Azure-compatible client + classifier (per-file decisions). |
| `internal/scanner`  | Detects stable downloads, lists all contained files.              |
| `internal/mover`    | Move (cross-device safe), delete, remove-if-empty.                |
| `internal/engine`   | Orchestrates scan → classify → plan → execute.                    |
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

## Storage

A pure-Go SQLite database (`modernc.org/sqlite`, no CGO) holds all state. Media
is bind-mounted at `/dataroot`; paths are validated to stay within it.
