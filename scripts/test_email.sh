#!/usr/bin/env bash
# test_email.sh — manual test runner for the email tools
# Builds and runs the emailtest harness against a stub email provider.
#
# Usage:
#   ./scripts/test_email.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "Building email test harness..."
go build -o /tmp/agentos-emailtest ./cmd/emailtest/ 2>&1

echo ""
/tmp/agentos-emailtest
