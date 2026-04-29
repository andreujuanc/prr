#!/usr/bin/env bash
set -euo pipefail

# Load .env if it exists
ENV_FILE="${PRR_ENV_FILE:-.env}"
if [[ -f "$ENV_FILE" ]]; then
    set -a
    source "$ENV_FILE"
    set +a
fi

if [[ -z "${PRR_API_KEY:-}" ]]; then
    echo "error: PRR_API_KEY not set"
    echo "  Set it in .env or export it directly"
    echo ""
    echo "  Example .env:"
    echo "    PRR_API_KEY=your-key-here"
    echo "    PRR_MODEL=gemini-2.5-flash"
    exit 1
fi

echo "Running live API tests (model: ${PRR_MODEL:-gemini-2.5-flash})"
echo ""

exec go test ./internal/ai/ -run TestLive -v -count=1 "$@"
