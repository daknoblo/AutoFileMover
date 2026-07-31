# Copilot-Instruktionen — AutoFileMover

Diese Datei beschreibt die verbindlichen Konventionen, Best Practices und
Sicherheitsvorgaben für **AutoFileMover**. GitHub Copilot soll sich bei Code,
Dockerfile, GitHub Actions, Tests und Dokumentation daran halten.

> **Kontext:** AutoFileMover ist ein kleiner Go-Service, der heruntergeladene
> Dateien/Medien überwacht, per OpenAI-kompatiblem KI-Endpunkt klassifiziert und
> in passende Medienbibliotheken verschiebt. Die Anwendung läuft als einzelnes
> statisches Binary in Docker und nutzt SQLite für Persistenz.

## 1. Sprache, Runtime & Grundprinzipien

- **Sprache: Go 1.26**.
- **Module-Pfad:** `github.com/daknoblo/AutoFileMover`.
- **Ein einzelnes, statisches Binary** als Auslieferungsartefakt.
- **Pure-Go / CGO-frei:** Immer `CGO_ENABLED=0` bauen. Für SQLite wird
  `modernc.org/sqlite` verwendet; kein `mattn/go-sqlite3` und keine C-Toolchain.
- **Standardbibliothek zuerst.** Abhängigkeiten nur einführen, wenn sie klaren
  Mehrwert bieten.
- **Sprachregelung:** Code-Bezeichner und die Dokumentation (README, `docs/`,
  Release Notes) sind **Englisch**. Code-Kommentare folgen dem bestehenden Code;
  die Web-UI ist zweisprachig (DE/EN). Nutzerseitige Fehlertexte dürfen deutsch
  bleiben – deshalb ist `ST1005` in `.golangci.yml` deaktiviert.

## 2. Projektstruktur

```text
cmd/autofilemover/    # Einstiegspunkt, Wiring, Signal-Handling
internal/config/      # Env-Konfiguration mit AFM_-Präfix
internal/store/       # SQLite-Zugriff und Migrationen
internal/ai/          # OpenAI-/Azure-kompatibler Client und Klassifizierung
internal/scanner/     # Download-Erkennung und Datei-Auslesen
internal/mover/       # Verschieben mit Cross-Device-Fallback
internal/engine/      # Orchestrierung: scan → classify → decide → move/queue
internal/watcher/     # fsnotify-Überwachung und periodischer Scan
internal/web/         # REST-API und eingebettete Web-UI
```

`cmd/autofilemover/main.go` bleibt schlank: Argument-Parsing (inkl.
`-healthcheck`), Aufbau der Abhängigkeiten, Signal-Handling und Graceful
Shutdown. Nicht öffentlich wiederverwendbarer Code gehört unter `internal/`.

## 3. Docker

- Mehrstufiges Dockerfile: Build-Stage `golang:1.26-alpine`, Runtime
  `gcr.io/distroless/static-debian12:nonroot`.
- Immer `CGO_ENABLED=0` und statisch bauen.
- `go.mod`/`go.sum` vor dem restlichen Quellcode kopieren und `go mod download`
  separat ausführen, damit der Docker-Build-Cache greift.
- Der Container läuft als `nonroot:nonroot`; keine Shell, kein Paketmanager.
- Persistenz liegt unter `/appdata`, Medien werden unter `/dataroot` gemountet.
- Das Binary implementiert `-healthcheck`; Docker nutzt Exec-Form:
  `CMD ["/autofilemover", "-healthcheck"]`.
- `/healthz` liefert HTTP 200 mit Body `ok`.
- Multi-Arch-Images für `linux/amd64` und `linux/arm64` werden gebaut. Das
  Dockerfile darf Cross-Compile (`TARGETOS`/`TARGETARCH`) nutzen; QEMU bleibt im
  Release-Workflow als Fallback.
- `_ "time/tzdata"` importieren, damit `TZ` in distroless funktioniert.

## 4. GitHub Actions

- Workflows: `.github/workflows/ci.yml`, `release.yml`, `codeql.yml`.
- Actions immer mit festem Major/Version pinnen; keine `@main`/`@master`.
- CI läuft auf `main` und `develop` für Push/PR und prüft:
  `gofmt -l .`, `go vet ./...`, `golangci-lint`, `govulncheck`,
  `go test -race ./...`, `CGO_ENABLED=0 go build ./...`.
- Release baut und pusht nach GHCR, erzeugt SBOM/Provenance, signiert keyless
  mit cosign und lädt Trivy-SARIF in den Security-Tab.
- CodeQL nutzt für das reine Go-Repo `build-mode: autobuild`.

## 5. Linting & Formatierung

- `.golangci.yml` nutzt golangci-lint v2 mit Standard-Lintern plus `misspell`.
- Code ist immer `gofmt`-formatiert.
- `go vet ./...` muss fehlerfrei sein.
- Fehler behandeln; bewusst ignorierte `Close()`-Aufrufe über die zentralen
  errcheck-Ausnahmen konfigurieren.

## 6. Sicherheit

- Minimale Angriffsfläche: distroless, non-root, statisches Binary.
- Read-only Root-Filesystem in Compose anstreben; nur `/appdata` und der
  Medien-Mount sind beschreibbar.
- Keine Secrets oder API-Keys committen. API-Keys werden nicht in der UI
  zurückgegeben; sensible Persistenz muss verschlüsselt erfolgen, falls sie
  künftig erweitert wird.
- Die App hat keine eingebaute Authentifizierung und ist für ein vertrauenswürdiges
  Netz bzw. Reverse Proxy/VPN gedacht, nicht für direkte Internet-Exposition.
- SQL nur parametrisiert verwenden; keine String-Konkatenation mit Nutzereingaben.
- Pfade strikt gegen `AFM_MEDIA_ROOT` validieren.

## 7. Konfiguration & Env-Variablen

Konfiguration erfolgt über Env-Variablen mit Präfix `AFM_`:

- `AFM_HTTP_ADDR` (Default `:8080`) — Listen-Adresse.
- `AFM_DB_PATH` (Default `/appdata/autofilemover.db`) — SQLite-Datenbank.
- `AFM_MEDIA_ROOT` (Default `/dataroot`) — Medienwurzel.
- `AFM_STABILITY_WINDOW` (Default `30s`) — Ruhezeit vor Verarbeitung.
- `AFM_SCAN_INTERVAL` (Default `5m`) — Fallback-Scan-Intervall.
- `AFM_LOG_LEVEL` (Default `info`) — `debug`, `info`, `warn`, `error`.
- `TZ` — IANA-Zeitzone, z. B. `Europe/Berlin`.

## 8. Tests & Definition of Done

- Tests mit dem Standard-`testing`-Package, ausführbar über `go test ./...`.
- Vor Abschluss lokal verifizieren: `go mod tidy`, `gofmt -l .` leer,
  `go vet ./...`, `CGO_ENABLED=0 go build ./...`, `go test ./...`.
- Keine Secrets, keine unnötigen Abhängigkeiten, keine ungeprüften Workflow-
  oder Docker-Änderungen.
