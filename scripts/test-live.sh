#!/usr/bin/env bash
set -euo pipefail

# Live tests read credentials from ~/.config/prr/config.json (same as the app).
# No separate API key env var needed — just have the app configured.

echo "Running live API tests..."
echo "  Credentials: ~/.config/prr/config.json"
echo ""

export PRR_LIVE_TESTS=1
exec go test ./internal/ai/ -run TestLive -v -count=1 "$@"
