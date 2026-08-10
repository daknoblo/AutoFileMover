#!/usr/bin/env bash
# Regenerates docs/images/*.png and docs/demo.md.
#
# The capture runs inside the pinned Playwright container so the rendering
# (fonts, browser build, paths) is identical on every machine and in CI. Inside
# the container the demo uses the same /dataroot layout as the shipped image.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IMAGE="mcr.microsoft.com/playwright:v1.62.1-noble"

cd "$ROOT"

# The demo binary runs inside the Linux container.
CGO_ENABLED=0 GOOS=linux go build -trimpath -o bin/afm-demo-linux ./cmd/afm-demo

docker run --rm \
	-v "$ROOT":/work \
	-e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
	"$IMAGE" bash -c '
set -euo pipefail
# Keep node_modules out of the mounted repository.
mkdir -p /tmp/shots
cp /work/tools/screenshots/package.json /work/tools/screenshots/package-lock.json /tmp/shots/
cp /work/tools/screenshots/*.mjs /tmp/shots/
cd /tmp/shots
npm ci --no-audit --no-fund --loglevel=error
node capture.mjs \
	--bin /work/bin/afm-demo-linux \
	--root /dataroot \
	--db /appdata/demo.db \
	--out /work/docs/images \
	--docs /work/docs/demo.md
chown -R "${HOST_UID}:${HOST_GID}" /work/docs/images /work/docs/demo.md
'
