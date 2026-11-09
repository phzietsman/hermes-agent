#!/usr/bin/env bash
# Go port gate: vet, staticcheck, race tests with coverage.
# Scoped to cmd/ and internal/ because node_modules ships a stray Go package.
set -euo pipefail
cd "$(dirname "$0")/.."
PKGS=(./cmd/... ./internal/...)
go build "${PKGS[@]}"
go vet "${PKGS[@]}"
# Pinned as a tool dependency in go.mod (go.sum-verified), so it always matches the module's Go version.
go tool staticcheck "${PKGS[@]}"
# Coverage only for internal/: `go test -cover` on a package without tests (cmd/hermes until 4.1) breaks
# under the auto-switched Go 1.26 toolchain with a compile/tool version mismatch.
go test -race ./cmd/...
go test -race -cover ./internal/...
