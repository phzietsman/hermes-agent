#!/usr/bin/env bash
# Go port gate: vet, staticcheck, race tests with coverage.
# Scoped to cmd/ and internal/ because node_modules ships a stray Go package.
set -euo pipefail
cd "$(dirname "$0")/.."
PKGS=(./cmd/... ./internal/...)
go build "${PKGS[@]}"
go vet "${PKGS[@]}"
SC="$(command -v staticcheck || echo "$HOME/go/bin/staticcheck")"
"$SC" "${PKGS[@]}"
go test -race -cover "${PKGS[@]}"
