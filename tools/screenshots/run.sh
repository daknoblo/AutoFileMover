#!/usr/bin/env bash
# Regenerates docs/images/*.png and docs/demo.md.
#
# The capture runs inside the pinned Playwright container so the rendering
# (fonts, browser build, paths) is identical on every machine and in CI. Inside
# the container the demo uses the same /dataroot layout as the shipped image.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# The container ships the browser build, which must match the playwright package
# exactly. Deriving the tag from package.json keeps them in step, so a dependency
# bump of the package alone can no longer break the capture.
PLAYWRIGHT_VERSION="$(sed -n 's/.*"playwright"[[:space:]]*:[[:space:]]*"\^\{0,1\}\([0-9][^"]*\)".*/\1/p' \
	"$ROOT/tools/screenshots/package.json" | head -1)"
if [ -z "$PLAYWRIGHT_VERSION" ]; then
	echo "cannot read the playwright version from tools/screenshots/package.json" >&2
	exit 1
fi
IMAGE="mcr.microsoft.com/playwright:v${PLAYWRIGHT_VERSION}-noble"
# CI renders on linux/amd64; matching it locally keeps the committed PNGs
# byte-identical (set AFM_SHOT_PLATFORM=linux/arm64 for a faster preview run).
PLATFORM="${AFM_SHOT_PLATFORM:-linux/amd64}"

cd "$ROOT"

# The demo binary runs inside the Linux container.
CGO_ENABLED=0 GOOS=linux GOARCH="${PLATFORM##*/}" go build -trimpath -o bin/afm-demo-linux ./cmd/afm-demo

docker run --rm \
	--platform "$PLATFORM" \
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
