# AutoFileMover

Ein selbst gehosteter Service, der heruntergeladene Medien automatisch erkennt,
per **KI-Endpoint** semantisch klassifiziert und in die passende
**Jellyfin-Bibliothek** (Filme, Serien, Dokumentationen) verschiebt.

Geschrieben in **Go**, läuft als **Docker-Container** und bringt eine
Web-Oberfläche mit (für Betrieb hinter einem Reverse Proxy gedacht).

## Funktionsweise

1. Der Medienordner wird in den Container gemountet (`/dataroot`). Darin liegen der
   Download-Ordner sowie die Ziel-Bibliotheken.
2. Ein **Watcher** (`fsnotify`) überwacht die konfigurierten **Quellordner**.
   Sobald ein Download „stabil“ ist (eine konfigurierbare Zeit nicht mehr
   verändert), wird er verarbeitet.
3. Der **Scanner** liest den Wurzelordner (oder die Einzeldatei) inklusive aller
   enthaltenen Dateien aus.
4. Diese Informationen werden zusammen mit den verfügbaren Bibliotheken (und bei
   Serien den vorhandenen Serienordnern) an einen **OpenAI-kompatiblen
   KI-Endpoint** (z. B. **Azure AI Foundry / Azure OpenAI**) übergeben. Die KI
   liefert Typ, Zielbibliothek, ggf. Serienordner und eine **Wahrscheinlichkeit**
   (0–1) zurück.
5. Liegt die Wahrscheinlichkeit **über dem eingestellten Schwellwert** (Standard
   90 %) und ist ein Ziel eindeutig auflösbar, wird **automatisch verschoben**.
6. Andernfalls (zu unsicher, kein passender Serienordner, KI nicht erreichbar)
   landet das Element in der **Review-Queue** und kann in der UI manuell
   bestätigt oder abgelehnt werden.

### Regeln für die Zuordnung

- **Film / Dokumentation** → Element wird in den Bibliotheksordner verschoben.
- **Serie** → Es wird **nur in einen bereits existierenden Serienordner**
  einsortiert. Findet die KI keinen passenden, wandert das Element in die
  Review-Queue (so gewünscht konfiguriert).
- Dateien werden **verschoben** (move), bei unterschiedlichen Dateisystemen
  automatisch per copy + delete (Cross-Device-Fallback).

### Entscheidung pro Datei

Ein Quellordner enthält oft mehrere Dateien (Film, Sample, NFO …). Die KI bewertet
**jede Datei einzeln** und schlägt eine Aktion mit Wahrscheinlichkeit vor:

- `move` – die eigentliche Mediendatei (größtes Video + Untertitel) → ins Ziel.
- `delete` – Sample-Clips, `.nfo`, Screenshots, Prüfsummen → endgültig löschen.
- `keep` – unsicher → bleibt für manuelle Prüfung.

In der Review-Queue erscheint der Ordner mit allen Dateien, Aktions-Label und
Prozent. Bei sicherem Auto-Move wird der Film verschoben, Reste gelöscht und der
leere Quellordner entfernt; im What-If lässt sich alles vorab pro Datei steuern.

### Sprache & Über

Die Oberfläche ist zweisprachig (Deutsch/Englisch, Umschalter im Header). Der
**Über**-Tab zeigt Version, Commit, Build-Datum und Go-Version; Header/Footer
verlinken auf das Repository.

## Schnellstart (Docker Compose)

`docker-compose.yml` anpassen (insbesondere das Media-Volume) und starten:

```bash
docker compose up -d
```

Danach die Web-UI öffnen: <http://localhost:8080>

In der UI:

1. **Quellordner** anlegen, z. B. `/dataroot/Downloads`.
2. **Bibliotheken** anlegen, z. B.
   - `Filme` (Film) → `/dataroot/Filme`
   - `Serien` (Serie) → `/dataroot/Serien`
   - `Dokus` (Dokumentation) → `/dataroot/Dokumentationen`
3. Unter **Einstellungen** den KI-Endpoint konfigurieren und den Schwellwert
   sowie „Automatisches Verschieben“ festlegen.

> Alle in der UI angegebenen Pfade müssen **innerhalb** von `AFM_MEDIA_ROOT`
> liegen und existieren.

## KI-Endpoint konfigurieren (Azure AI Foundry / Azure OpenAI)

| Feld              | Beispiel                                   |
| ----------------- | ------------------------------------------ |
| Base URL          | `https://<resource>.openai.azure.com`      |
| Deployment/Modell | `gpt-4o-mini` (Name deines Deployments)    |
| Azure API-Version | `2024-06-01`                               |
| API-Key           | dein Azure-Key                             |

- Ist eine **API-Version** gesetzt, wird der **Azure-Modus** verwendet
  (`/openai/deployments/<model>/chat/completions?api-version=...`, Header
  `api-key`).
- Bleibt die API-Version **leer**, wird der Standard-OpenAI-Pfad genutzt
  (`/v1/chat/completions`, Header `Authorization: Bearer ...`). Base URL dann
  z. B. `https://api.openai.com/v1`.

Der API-Key wird in der Datenbank gespeichert und in der UI nur als „gesetzt“
angezeigt, nie zurückgegeben.

## Konfiguration (Umgebungsvariablen)

| Variable                | Standard                      | Beschreibung                                         |
| ----------------------- | ----------------------------- | ---------------------------------------------------- |
| `AFM_HTTP_ADDR`         | `:8080`                       | Listen-Adresse des Webservers                        |
| `AFM_DB_PATH`           | `/data/autofilemover.db`      | Pfad der SQLite-Datenbank                            |
| `AFM_MEDIA_ROOT`        | `/dataroot`                   | Wurzel des gemounteten Medienverzeichnisses          |
| `AFM_STABILITY_WINDOW`  | `30s`                         | Ruhezeit, bevor ein Download verarbeitet wird        |
| `AFM_SCAN_INTERVAL`     | `5m`                          | Fallback-Intervall für periodische Scans             |
| `AFM_LOG_LEVEL`         | `info`                        | `debug`, `info`, `warn`, `error`                     |

Anwendungseinstellungen (KI-Config, Schwellwert, Quellen, Bibliotheken) werden
in der Datenbank gespeichert und über die UI verwaltet.

## Lokale Entwicklung (Dev Container)

Das Repository enthält einen Dev Container (`.devcontainer/devcontainer.json`)
mit Go-Toolchain. In VS Code per „Reopen in Container“ öffnen, dann:

```bash
mkdir -p devmedia/Downloads devmedia/Filme data
go run ./cmd/autofilemover
```

Die UI ist anschließend unter Port `8080` erreichbar. Der Dev Container setzt
`AFM_MEDIA_ROOT` auf `devmedia` und ein kurzes Stability-Window.

### Build & Tests

```bash
go build ./...
go vet ./...
go test ./...
```

## Dokumentation

- [docs/installation.md](docs/installation.md)
- [docs/configuration.md](docs/configuration.md)
- [docs/architecture.md](docs/architecture.md)
- [docs/development.md](docs/development.md)

## Sicherheit

- Keine eingebaute Authentifizierung – hinter Reverse Proxy/VPN betreiben.
- API-Key wird in der DB gespeichert, nie an die UI zurückgegeben.
- Pfade werden gegen `AFM_MEDIA_ROOT` validiert; Datei-Aktionen wirken nur auf
  bereits gescannte Dateien. Löschen ist endgültig (vorher What-If nutzen).
- Container läuft als non-root Distroless-Image mit eigenem Healthcheck.

## Container-Image (GitHub Action)

`.github/workflows/docker-build.yml` baut das Image bei jedem Push auf `main`
und bei Tags `v*` und veröffentlicht es nach **GHCR**
(`ghcr.io/<owner>/<repo>`) für `linux/amd64` und `linux/arm64`.

- Branch-Builds: Tag `main`
- Releases: per Git-Tag `vX.Y.Z` (semver-Tags werden erzeugt)

## Projektstruktur

```
cmd/autofilemover/    # main, Verdrahtung & Graceful Shutdown
internal/config/      # Env-Konfiguration
internal/store/       # SQLite (Settings, Sources, Libraries, Items)
internal/ai/          # OpenAI-/Azure-kompatibler Client + Classifier
internal/scanner/     # Download-Erkennung & Datei-Auslesen
internal/mover/       # Verschieben mit Cross-Device-Fallback
internal/engine/      # Orchestrierung: scan → classify → decide → move/queue
internal/watcher/     # fsnotify-Überwachung + periodischer Scan
internal/web/         # REST-API + eingebettete Web-UI (DE/EN)
internal/version/     # Build-Metadaten (ldflags)
```

## Hinweise zu Rechten

Verschobene Dateien gehören dem Nutzer, unter dem der Container läuft. Für
korrekte Jellyfin-Rechte in `docker-compose.yml` `user: "PUID:PGID"` passend zu
deinem Medienordner setzen.