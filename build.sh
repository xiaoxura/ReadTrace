#!/usr/bin/env bash
set -euo pipefail

VERSION="${1:-0.1.0}"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o readtrace ./cmd/readtrace
echo "built readtrace (${VERSION})"
