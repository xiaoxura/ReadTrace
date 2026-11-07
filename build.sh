#!/usr/bin/env bash
set -euo pipefail

VERSION="${1:-1.0.0}"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o wxread-panel ./cmd/wxread-panel
echo "built wxread-panel (${VERSION})"
