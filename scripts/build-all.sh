#!/usr/bin/env bash
# Build frontend (Vite) and embed into the Go server binary.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION_VALUE="$(tr -d '[:space:]' < "$ROOT/VERSION")"
if [[ -z "$VERSION_VALUE" ]]; then
  echo "VERSION must not be empty" >&2
  exit 1
fi

echo "==> building frontend"
(cd frontend && npm install --no-audit --no-fund && npm run build)

echo "==> building server binary"
mkdir -p bin
go build \
  -trimpath \
  -ldflags "-X github.com/kuroky/claude-code-monitor/internal/buildinfo.version=${VERSION_VALUE}" \
  -o bin/server \
  ./cmd/server

echo "==> done: bin/server"
ls -lh bin/server
