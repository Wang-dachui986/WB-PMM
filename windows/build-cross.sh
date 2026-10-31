#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${OUT:-$ROOT/windows/dist/app}"
DOTNET="${DOTNET:-dotnet}"
GO="${GO:-go}"
VERSION="${VERSION:-1.0.16}"
export DOTNET_CLI_TELEMETRY_OPTOUT=1
export CGO_ENABLED=0 GOOS=windows GOARCH=amd64
cd "$ROOT/panel/web"
if [[ ! -d node_modules ]]; then npm ci --no-audit --no-fund; fi
npm run build
"$DOTNET" publish "$ROOT/windows/WBPMM/WBPMM.csproj" -c Release -r win-x64 --self-contained true -p:Version="$VERSION" -o "$OUT"
mkdir -p "$OUT/Helpers"
cd "$ROOT/panel"
"$GO" build -buildvcs=false -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$OUT/Helpers/wbgui.exe" ./cmd/server
cd "$ROOT/gateway"
"$GO" build -buildvcs=false -trimpath -ldflags '-s -w' -o "$OUT/Helpers/wb2api.exe" ./cmd/server
cp "$ROOT/LICENSE" "$OUT/LICENSE.txt"
cp "$ROOT/windows/README.md" "$OUT/README-Windows.md"
printf 'Windows x64 application: %s\n' "$OUT"
