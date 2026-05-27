#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$SCRIPT_DIR/.."

# Load .env from project root if it exists
if [ -f "$ROOT/.env" ]; then
  export $(grep -v '^#' "$ROOT/.env" | xargs)
fi

if [ -z "$GEMINI_API_KEY" ]; then
  echo "Error: GEMINI_API_KEY is not set."
  echo "Create a .env file at the project root with: GEMINI_API_KEY=your-key-here"
  echo ""
  echo "Get a free key at: https://aistudio.google.com/apikey"
  exit 1
fi

echo "Starting GhostOperator at http://localhost:8080"
cd "$ROOT/backend"
go run ./cmd/server --static ../frontend --port 8080
