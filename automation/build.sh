#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$SCRIPT_DIR/.."

echo "Building GhostOperator..."
cd "$ROOT/backend"
go build -o "$ROOT/ghostoperator" ./cmd/server
echo "Binary written to: $ROOT/ghostoperator"
echo "Run with: ANTHROPIC_API_KEY=... ./ghostoperator --static ./frontend"
