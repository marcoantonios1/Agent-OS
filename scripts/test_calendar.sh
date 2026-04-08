#!/usr/bin/env bash
# test_calendar.sh — manual test runner for the calendar tools
# Builds and runs the calendartest harness. When OUTLOOK_CAL_* or
# GOOGLE_CAL_* vars are present in .env it uses a live calendar provider.
#
# Usage:
#   ./scripts/test_calendar.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Load .env and export all vars so the binary inherits them.
if [ -f "$ROOT/.env" ]; then
    set -a
    # shellcheck source=/dev/null
    source "$ROOT/.env"
    set +a
fi

echo "Building calendar test harness..."
go build -o /tmp/agentos-calendartest ./cmd/calendartest/ 2>&1

echo ""
/tmp/agentos-calendartest
