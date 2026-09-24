# Configuration

Infrastructure settings come from environment variables. Everything else (AI
endpoint, threshold, sources, libraries, language) is configured in the web UI
and stored in the SQLite database.

## Environment variables

| Variable               | Default                    | Description                                   |
| ---------------------- | -------------------------- | --------------------------------------------- |
| `AFM_HTTP_ADDR`        | `:8080`                    | Listen address of the web server.             |
| `AFM_DB_PATH`          | `/appdata/autofilemover.db`   | SQLite database path.                         |
| `AFM_MEDIA_ROOT`       | `/dataroot`                | Root of the mounted media volume.             |
| `AFM_STABILITY_WINDOW` | `30s`                      | Quiet time before a download is processed.    |
| `AFM_SCAN_INTERVAL`    | `5m`                       | Fallback periodic scan interval.              |
| `AFM_LOG_LEVEL`        | `info`                     | `debug`, `info`, `warn`, `error`.             |

All configured paths must stay **inside** `AFM_MEDIA_ROOT` and must exist.

### Azure AI Foundry identity (optional)

Setting any of these switches the AI endpoint into Foundry mode: the endpoint
and the selectable deployments are discovered from Azure instead of being typed
in. Each variable is also accepted with the project prefix
(`AFM_AZURE_RESOURCE_ID` and so on), which takes precedence.

| Variable              | Description                                                                                                               |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| `AZURE_RESOURCE_ID`   | Account resource ID: `/subscriptions/<id>/resourceGroups/<group>/providers/Microsoft.CognitiveServices/accounts/<account>` |
| `AZURE_TENANT_ID`     | Entra tenant ID (UUID).                                                                                                    |
| `AZURE_CLIENT_ID`     | Application/client ID of the service principal (UUID).                                                                     |
| `AZURE_CLIENT_SECRET` | **Secret.** Environment only; never written to the database or returned by the API.                                        |

The service principal needs **Reader** on the account to list deployments and
**Cognitive Services OpenAI User** to run inference.

## AI endpoint (Azure AI Foundry / Azure OpenAI / OpenAI)

In **Foundry mode** the settings page shows the discovered endpoint and a
dropdown of every chat-capable deployment, with a **Reload deployments** button.
Embedding, image, audio, batch and responses-only deployments are filtered out,
and models that accept no custom temperature are recognised from their metadata.
Requests are signed with a short-lived Entra token, so no endpoint, API version
or API key is entered. The cached listing never blocks a selection: a deployment
it does not currently mention is still saved and used, and marked in the
dropdown.

With none of the Azure variables set, the classic fields apply:

| Field           | Example                                  |
| --------------- | ---------------------------------------- |
| Base URL        | `https://<resource>.openai.azure.com`    |
| Deployment      | `gpt-4o-mini`                            |
| Azure version   | `2024-06-01`                             |
| API key         | stored in the DB, never returned to UI   |

The request shape follows the base URL:

- Azure resource URL **with** an API version →
  `/openai/deployments/<model>/chat/completions?api-version=...`.
- Foundry v1 root `https://<resource>.services.ai.azure.com/openai/v1` →
  `<base>/chat/completions`, deployment in the body, **API version empty** (a
  leftover value is ignored rather than breaking the URL).
- `https://api.openai.com/v1` or a compatible proxy → `<base>/chat/completions`;
  a URL already ending in `/chat/completions` is used unchanged.

Hosts under `*.azure.com` authenticate with the `api-key` header, everything
else with `Authorization: ****** that only accept their default
temperature (GPT-5 family, o-series) are detected automatically: the parameter
is dropped after the first rejection and skipped for that model afterwards.

Both modes offer a **Test connection** button that verifies the stored
configuration against the live endpoint. Because the endpoint is
user-configurable, its credential never follows a redirect, and an upstream
error is shortened and stripped of control characters before it is displayed.

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
