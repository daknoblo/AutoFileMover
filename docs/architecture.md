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
| `internal/foundry`  | Azure AI Foundry deployment discovery via an Entra identity.      |
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
review queue every file can be confirmed individually, and each file shows what
happens to it: a destination, an explicit *deleted* marker, or a review note.

The byte sizes decide which of several videos is the feature. As a safety net
the largest video is never deleted automatically: a model that proposes it — for
example by mixing up the feature and a sample — has that file downgraded to
manual review, because discarded junk is cheap to redo while a deleted feature
is not. A video whose own name marks it as a sample or trailer stays deletable,
and an explicit choice by the user is never overridden.

## Keeping the lists current

A scan reads the filesystem, but records can outlive the files they describe. A
shared background reconciliation therefore re-reads the configured sources,
removes items whose source disappeared, refreshes stored file metadata and
prunes jobs that lost their item. It is triggered by the list endpoints but
never blocks them: they answer from the database immediately while
`/api/status` reports whether a refresh is running, when it last succeeded and
why it failed. Terminal history is kept and can be cleared explicitly.

## AI endpoint

The chat client speaks the OpenAI, Azure OpenAI and Foundry v1 request shapes
and resolves which one to use from the configured base URL. With an Azure
identity configured, `internal/foundry` discovers the account's endpoint and its
chat-capable deployments over ARM and signs each request with a short-lived
Entra token, so no endpoint or key is entered by hand. Discovery is cached and
coalesced; a failure never aborts a scan — detection keeps working and the
candidates go to manual review.

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

### Plan changes are database-only

Choosing a target library or toggling a per-file action is a review *decision*,
not a filesystem action, so it is persisted synchronously and answered with
`200`. That only holds as long as those handlers stay free of storage access:
Go's file syscalls cannot be cancelled by a context, so a single `Stat` or
directory listing on a stalled share would block the request past the browser
timeout and the user's choice would be lost.

Detecting whether a destination already holds a colliding file needs a directory
listing, so it is split off into a `detect_conflicts` job that the worker runs
right after the decision was stored. The review card therefore shows collisions a
moment later rather than immediately. Because that state can lag, `ApplyPlan`
re-scans the destination itself before moving anything — the queued scan drives
the display, the re-check in the worker is authoritative.

## Concurrency

Work is serialized per item, keyed by source path, not globally. A scan, an AI
call or a large move therefore only blocks actions on that one item. Database-
only actions in the request path take the item lock with a short timeout and
answer `409` rather than waiting, so a click always gets an immediate response.

## Storage

A pure-Go SQLite database (`modernc.org/sqlite`, no CGO) holds all state. It
contains the AI API key, so the file is created with `0600` permissions. Media
is bind-mounted at `/dataroot`; paths are validated to stay within it.
