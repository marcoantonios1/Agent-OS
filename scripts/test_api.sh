#!/usr/bin/env bash
# test_api.sh — manual integration tests for Agent OS HTTP API
# Usage:
#   ./scripts/test_api.sh              # auto-starts the server, runs tests, stops it
#   BASE_URL=http://localhost:9000 ./scripts/test_api.sh  # test against a running server
set -uo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
SERVER_PID=""
PASS=0
FAIL=0

# ── colours ────────────────────────────────────────────────────────────────────
GREEN="\033[0;32m"
RED="\033[0;31m"
YELLOW="\033[0;33m"
RESET="\033[0m"

# ── helpers ────────────────────────────────────────────────────────────────────
pass() { echo -e "  ${GREEN}PASS${RESET}  $1"; ((PASS++)) || true; }
fail() { echo -e "  ${RED}FAIL${RESET}  $1${YELLOW} — $2${RESET}"; ((FAIL++)) || true; }

header() { echo -e "\n${YELLOW}▶ $1${RESET}"; }

# assert_status <test-name> <expected-status> <actual-status>
assert_status() {
    local name="$1" expected="$2" actual="$3"
    if [[ "$actual" == "$expected" ]]; then
        pass "$name"
    else
        fail "$name" "HTTP $actual, want $expected"
    fi
}

# assert_body_contains <test-name> <substring> <body>
assert_body_contains() {
    local name="$1" needle="$2" body="$3"
    if echo "$body" | grep -q "$needle"; then
        pass "$name"
    else
        fail "$name" "body does not contain '$needle'\n         body: $body"
    fi
}

# assert_header_present <test-name> <header-name> <headers>
assert_header_present() {
    local name="$1" header="$2" headers="$3"
    if echo "$headers" | grep -iq "$header"; then
        pass "$name"
    else
        fail "$name" "response missing header '$header'"
    fi
}

# assert_header_value <test-name> <header-name> <expected-value> <headers>
assert_header_value() {
    local name="$1" header="$2" expected="$3" headers="$4"
    local actual
    actual=$(echo "$headers" | grep -i "$header" | awk '{print $2}' | tr -d '\r')
    if [[ "$actual" == "$expected" ]]; then
        pass "$name"
    else
        fail "$name" "$header: got '$actual', want '$expected'"
    fi
}

# post_chat <session_id> <user_id> <text> — returns "<status>\n<body>"
post_chat() {
    local session_id="$1" user_id="$2" text="$3"
    curl -s -o /tmp/agentos_body.txt -w "%{http_code}" \
        -X POST "$BASE_URL/v1/chat" \
        -H "Content-Type: application/json" \
        -d "{\"session_id\":\"$session_id\",\"user_id\":\"$user_id\",\"text\":\"$text\"}"
}

post_chat_raw() {
    local payload="$1"
    curl -s -o /tmp/agentos_body.txt -w "%{http_code}" \
        -X POST "$BASE_URL/v1/chat" \
        -H "Content-Type: application/json" \
        -d "$payload"
}

post_chat_with_headers() {
    local session_id="$1" user_id="$2" text="$3"
    shift 3
    # remaining args are passed directly as extra curl flags (e.g. -H "X-Request-ID: foo")
    curl -s -D /tmp/agentos_headers.txt -o /tmp/agentos_body.txt -w "%{http_code}" \
        -X POST "$BASE_URL/v1/chat" \
        -H "Content-Type: application/json" \
        "$@" \
        -d "{\"session_id\":\"$session_id\",\"user_id\":\"$user_id\",\"text\":\"$text\"}"
}

body() { cat /tmp/agentos_body.txt; }
headers() { cat /tmp/agentos_headers.txt 2>/dev/null; }

# ── server lifecycle ───────────────────────────────────────────────────────────
server_running() {
    curl -sf "$BASE_URL/healthz" > /dev/null 2>&1
}

start_server() {
    echo -e "${YELLOW}Starting Agent OS server...${RESET}"
    cd "$(dirname "$0")/.."
    go run ./cmd/agentos/ > /tmp/agentos_server.log 2>&1 &
    SERVER_PID=$!

    local retries=15
    while ! server_running && (( retries-- > 0 )); do
        sleep 0.5
    done

    if ! server_running; then
        echo -e "${RED}Server failed to start. Log:${RESET}"
        cat /tmp/agentos_server.log
        exit 1
    fi
    echo -e "${GREEN}Server ready (PID $SERVER_PID)${RESET}"
}

stop_server() {
    if [[ -n "$SERVER_PID" ]]; then
        kill "$SERVER_PID" 2>/dev/null || true
        echo -e "\n${YELLOW}Server stopped${RESET}"
    fi
}

trap stop_server EXIT

# ── main ───────────────────────────────────────────────────────────────────────
if server_running; then
    echo -e "${GREEN}Using running server at $BASE_URL${RESET}"
else
    start_server
fi

# ── 1. Health check ────────────────────────────────────────────────────────────
header "Health check"

status=$(curl -s -o /tmp/agentos_body.txt -w "%{http_code}" "$BASE_URL/healthz")
assert_status "GET /healthz returns 200"   "200" "$status"
assert_body_contains "body is 'ok'"        "ok"  "$(body)"

# ── 2. Single chat turn ───────────────────────────────────────────────────────
header "Single chat turn"

status=$(post_chat "t-single" "user-1" "Hello, write me a sort function")
assert_status "POST /v1/chat returns 200"            "200" "$status"
assert_body_contains "session_id echoed"             "t-single"   "$(body)"
assert_body_contains "response text is non-empty"    "\"text\":"  "$(body)"

# ── 3. Multi-turn — history preserved ─────────────────────────────────────────
header "Multi-turn conversation"

status=$(post_chat "t-multi" "user-1" "First message")
assert_status "turn 1: 200" "200" "$status"

status=$(post_chat "t-multi" "user-1" "Second message same session")
assert_status "turn 2: 200"                         "200"      "$status"
assert_body_contains "turn 2: session_id preserved" "t-multi"  "$(body)"

# ── 4. Request ID ─────────────────────────────────────────────────────────────
header "Request ID middleware"

status=$(post_chat_with_headers "t-rid" "user-1" "hi")
assert_header_present "X-Request-ID generated on every response" \
    "X-Request-Id" "$(headers)"

# Honour incoming X-Request-ID
status=$(post_chat_with_headers "t-rid2" "user-1" "hi" -H "X-Request-ID: my-trace-id-123")
assert_header_value "incoming X-Request-ID echoed back" \
    "X-Request-Id" "my-trace-id-123" "$(headers)"

# ── 5. Validation — missing fields ────────────────────────────────────────────
header "Validation — missing required fields"

status=$(post_chat_raw '{"user_id":"u1","text":"no session"}')
assert_status "missing session_id → 400" "400" "$status"
assert_body_contains "error field present" "\"error\"" "$(body)"

status=$(post_chat_raw '{"session_id":"s1","text":"no user"}')
assert_status "missing user_id → 400" "400" "$status"

status=$(post_chat_raw '{"session_id":"s1","user_id":"u1"}')
assert_status "missing text → 400" "400" "$status"

# ── 6. Validation — bad JSON ──────────────────────────────────────────────────
header "Validation — malformed JSON"

status=$(curl -s -o /tmp/agentos_body.txt -w "%{http_code}" \
    -X POST "$BASE_URL/v1/chat" \
    -H "Content-Type: application/json" \
    -d '{not valid json at all')
assert_status "malformed JSON → 400" "400" "$status"
assert_body_contains "error field present" "\"error\"" "$(body)"

# ── 7. Unknown route ──────────────────────────────────────────────────────────
header "Unknown route"

status=$(curl -s -o /tmp/agentos_body.txt -w "%{http_code}" "$BASE_URL/does-not-exist")
assert_status "unknown route → 404" "404" "$status"

# ── summary ───────────────────────────────────────────────────────────────────
echo ""
echo "────────────────────────────────────────"
total=$(( PASS + FAIL ))
echo -e "Results: ${GREEN}$PASS passed${RESET} / ${RED}$FAIL failed${RESET} / $total total"
echo "────────────────────────────────────────"

[[ $FAIL -eq 0 ]] && exit 0 || exit 1
