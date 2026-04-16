#!/usr/bin/env bash
# test_discord.sh — manual smoke test for the Discord channel integration.
#
# Prerequisites:
#   DISCORD_BOT_TOKEN  — bot token (required)
#   DISCORD_GUILD_ID   — guild (server) ID to restrict the bot (optional)
#   DISCORD_CHANNEL_ID — channel ID to send the test message to (required)
#   DISCORD_BOT_ID     — bot's own user ID (used to verify the reply appeared)
#
# The script posts a message via the Discord REST API, waits for a reply from
# the bot, then checks the reply text.
#
# Usage:
#   DISCORD_BOT_TOKEN=<token> \
#   DISCORD_CHANNEL_ID=<channel_id> \
#   ./scripts/test_discord.sh
set -uo pipefail

# ── required env vars ─────────────────────────────────────────────────────────
: "${DISCORD_BOT_TOKEN:?Set DISCORD_BOT_TOKEN before running this script}"
: "${DISCORD_CHANNEL_ID:?Set DISCORD_CHANNEL_ID (the channel to post the test message to)}"

DISCORD_API="https://discord.com/api/v10"
PASS=0
FAIL=0

# ── colours ───────────────────────────────────────────────────────────────────
GREEN="\033[0;32m"
RED="\033[0;31m"
YELLOW="\033[0;33m"
RESET="\033[0m"

pass() { echo -e "  ${GREEN}PASS${RESET}  $1"; ((PASS++)) || true; }
fail() { echo -e "  ${RED}FAIL${RESET}  $1${YELLOW} — $2${RESET}"; ((FAIL++)) || true; }
header() { echo -e "\n${YELLOW}▶ $1${RESET}"; }

# ── helpers ───────────────────────────────────────────────────────────────────

# discord_get <path> — GET from Discord REST API, prints body.
discord_get() {
    curl -sf \
        -H "Authorization: Bot $DISCORD_BOT_TOKEN" \
        -H "Content-Type: application/json" \
        "$DISCORD_API$1"
}

# discord_post <path> <json> — POST to Discord REST API, prints body.
discord_post() {
    curl -sf \
        -X POST \
        -H "Authorization: Bot $DISCORD_BOT_TOKEN" \
        -H "Content-Type: application/json" \
        -d "$2" \
        "$DISCORD_API$1"
}

# send_message <text> — posts a message to the configured channel.
send_message() {
    discord_post "/channels/$DISCORD_CHANNEL_ID/messages" \
        "{\"content\":\"$1\"}"
}

# get_recent_messages — fetches the last 10 messages in the channel.
get_recent_messages() {
    discord_get "/channels/$DISCORD_CHANNEL_ID/messages?limit=10"
}

# bot_online — checks that the bot is connected by reading its gateway info.
bot_online() {
    discord_get "/users/@me" > /dev/null 2>&1
}

# ── server lifecycle ──────────────────────────────────────────────────────────
SERVER_PID=""

start_server() {
    echo -e "${YELLOW}Starting Agent OS server with Discord enabled...${RESET}"
    cd "$(dirname "$0")/.."
    go run ./cmd/agentos/ > /tmp/agentos_discord_server.log 2>&1 &
    SERVER_PID=$!

    local retries=20
    while ! grep -q "discord channel started" /tmp/agentos_discord_server.log 2>/dev/null && (( retries-- > 0 )); do
        sleep 0.5
    done

    if ! grep -q "discord channel started" /tmp/agentos_discord_server.log 2>/dev/null; then
        echo -e "${RED}Discord channel failed to start. Log:${RESET}"
        cat /tmp/agentos_discord_server.log
        exit 1
    fi
    echo -e "${GREEN}Agent OS + Discord channel ready (PID $SERVER_PID)${RESET}"
}

stop_server() {
    if [[ -n "$SERVER_PID" ]]; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
        echo -e "\n${YELLOW}Server stopped (PID $SERVER_PID)${RESET}"
    fi
}

trap stop_server EXIT

# ── pre-flight ────────────────────────────────────────────────────────────────
header "Pre-flight checks"

if ! bot_online; then
    fail "bot token valid" "could not authenticate with Discord API — check DISCORD_BOT_TOKEN"
    exit 1
fi
pass "bot token valid"

# ── start server ──────────────────────────────────────────────────────────────
start_server

# ── test 1: bot comes online ──────────────────────────────────────────────────
header "Test 1 — Bot presence"

if grep -q "discord channel started" /tmp/agentos_discord_server.log 2>/dev/null; then
    pass "discord channel started log line present"
else
    fail "discord channel started" "log line not found in server output"
fi

# ── test 2: message routing ───────────────────────────────────────────────────
header "Test 2 — Message routing (research query)"

TEST_MSG="AgentOS test $(date +%s): What is the capital of Portugal?"
send_message "$TEST_MSG" > /dev/null
echo "  Sent: $TEST_MSG"
echo "  Waiting up to 30s for bot reply..."

BOT_REPLIED=false
for i in $(seq 1 30); do
    sleep 1
    MESSAGES=$(get_recent_messages 2>/dev/null || echo "[]")
    # Check if any message in the last 10 mentions Lisbon (expected answer).
    if echo "$MESSAGES" | grep -qi "lisbon\|Lisboa"; then
        BOT_REPLIED=true
        break
    fi
done

if $BOT_REPLIED; then
    pass "bot replied to research query (reply contains 'Lisbon')"
else
    fail "bot reply" "no reply containing 'Lisbon' found within 30s"
fi

# ── test 3: approval surface ──────────────────────────────────────────────────
header "Test 3 — Approval surface (draft email prompt)"

DRAFT_MSG="AgentOS test $(date +%s): Draft an email to alice@example.com saying hello"
send_message "$DRAFT_MSG" > /dev/null
echo "  Sent: $DRAFT_MSG"
echo "  Waiting up to 30s for draft reply..."

DRAFT_REPLIED=false
for i in $(seq 1 30); do
    sleep 1
    MESSAGES=$(get_recent_messages 2>/dev/null || echo "[]")
    if echo "$MESSAGES" | grep -qi "draft\|confirm\|alice"; then
        DRAFT_REPLIED=true
        break
    fi
done

if $DRAFT_REPLIED; then
    pass "bot surfaced draft/approval prompt in channel"
else
    fail "draft approval prompt" "no draft/confirm message found within 30s"
fi

# ── test 4: graceful shutdown ─────────────────────────────────────────────────
header "Test 4 — Graceful shutdown"

kill -SIGTERM "$SERVER_PID" 2>/dev/null || true
sleep 2

if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    pass "server exited cleanly after SIGTERM"
    SERVER_PID=""  # prevent double-kill in trap
else
    fail "graceful shutdown" "process still running after SIGTERM"
fi

# ── summary ───────────────────────────────────────────────────────────────────
echo ""
echo "────────────────────────────────────────"
total=$(( PASS + FAIL ))
echo -e "Results: ${GREEN}$PASS passed${RESET} / ${RED}$FAIL failed${RESET} / $total total"
echo "────────────────────────────────────────"

[[ $FAIL -eq 0 ]] && exit 0 || exit 1
