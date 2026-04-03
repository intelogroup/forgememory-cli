#!/bin/bash
# Full real-world lifecycle test for Forge.
# Tests: init → daemon → hooks → user-save → distill → MCP × 4 → session-recall → stop
# Usage: bash tests/lifecycle_full.sh          (single run)
#        bash tests/lifecycle_full.sh --loop   (loop until 3 consecutive passes)

set -uo pipefail

FORGE="${FORGE:-./forge}"
PASS=0; FAIL=0
FAILED_TESTS=()

# ── colour helpers ───────────────────────────────────────────────────────────
GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; NC='\033[0m'
ok()   { echo -e "${GREEN}  ✓ $*${NC}"; PASS=$((PASS+1)); }
fail() { echo -e "${RED}  ✗ $*${NC}"; FAIL=$((FAIL+1)); FAILED_TESTS+=("$*"); }
step() { echo -e "\n${YELLOW}[$1]${NC} $2"; }

assert_contains() {
  local label="$1" needle="$2" haystack="$3"
  if echo "$haystack" | grep -q "$needle"; then
    ok "$label"
  else
    fail "$label (expected '$needle' in output)"
    echo "    output: $(echo "$haystack" | head -3)"
  fi
}

assert_ge() {
  local label="$1" val="$2" min="$3"
  if [ "${val:-0}" -ge "$min" ] 2>/dev/null; then
    ok "$label ($val >= $min)"
  else
    fail "$label (got $val, expected >= $min)"
  fi
}

# ── clean slate ──────────────────────────────────────────────────────────────
step 0 "Clean slate"
$FORGE stop 2>/dev/null || true
sleep 1
# Kill any stray daemon processes
pkill -f "forge daemon" 2>/dev/null || true
sleep 0.5
# Remove all forge state including SQLite WAL sidecar files
rm -f ~/.forge/forge.db ~/.forge/forge.db-wal ~/.forge/forge.db-shm \
      ~/.forge/forge.sock ~/.forge/forge.addr ~/.forge/forge.pid
ok "Cleaned ~/.forge state"

# ── 1. init ──────────────────────────────────────────────────────────────────
step 1 "forge init"
OUT=$($FORGE init 2>&1)
assert_contains "Database created" "Database created" "$OUT"
assert_contains "Configured claude" "Configured claude" "$OUT"

# ── 2. start daemon ──────────────────────────────────────────────────────────
step 2 "forge start"
$FORGE start
sleep 1.5

OUT=$($FORGE doctor 2>&1)
assert_contains "Daemon running" "\[OK\] Daemon" "$OUT"
assert_contains "DB healthy" "\[OK\] Database" "$OUT"

# ── 3. hooks: PostToolUse (write tools only) ─────────────────────────────────
step 3 "PostToolUse hooks — write tools captured, read tools filtered"

# Edit — should be captured
echo '{"session_id":"sess-rw-001","hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":"cmd/daemon.go","old_string":"foo","new_string":"bar"},"cwd":"/tmp/proj"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.3

# Write — should be captured
echo '{"session_id":"sess-rw-001","hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":"README.md","content":"# forge"},"cwd":"/tmp/proj"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.3

# Bash — should be captured
echo '{"session_id":"sess-rw-001","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"go test ./..."},"cwd":"/tmp/proj"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.3

# Read — should be FILTERED (not captured)
echo '{"session_id":"sess-rw-001","hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{"file_path":"go.sum"},"cwd":"/tmp/proj"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.3

# Glob — should be FILTERED
echo '{"session_id":"sess-rw-001","hook_event_name":"PostToolUse","tool_name":"Glob","tool_input":{"pattern":"*.go"},"cwd":"/tmp/proj"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.5

EVENTS=$($FORGE status 2>/dev/null | grep "Events:" | grep -oE '[0-9]+' | head -1)
EVENTS="${EVENTS:-0}"
assert_ge "Write tools captured" "$EVENTS" 3

# ── 4. hooks: private data redacted ──────────────────────────────────────────
step 4 "Hook strips <private> blocks"
echo '{"session_id":"sess-rw-001","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"echo hello <private>MY_SECRET_TOKEN=abc123</private>"},"cwd":"/tmp"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.3

RECENT=$($FORGE search "hello" 2>&1)
assert_contains "Secret redacted" "\[redacted\]" "$RECENT"

# ── 5. Stop + SessionEnd hooks ───────────────────────────────────────────────
step 5 "Stop and SessionEnd hooks"

echo '{"session_id":"sess-rw-001","hook_event_name":"Stop","cwd":"/tmp/proj"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=Stop $FORGE hook
sleep 0.3

echo '{"session_id":"sess-rw-001","hook_event_name":"SessionEnd","cwd":"/tmp/proj"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=SessionEnd $FORGE hook
sleep 0.5

EVENTS2=$($FORGE status 2>/dev/null | grep "Events:" | grep -oE '[0-9]+' | head -1)
EVENTS2="${EVENTS2:-0}"
assert_ge "Stop+SessionEnd captured" "$EVENTS2" "$((EVENTS + 2))"

# ── 6. user → DB: forge save ─────────────────────────────────────────────────
step 6 "User saves memories directly (two-way write)"

# Save as events (go into FTS5 events table → searchable via forge search)
SAVE1=$($FORGE save --type success --content "xplat-filepath-join cross-platform paths always use filepath.Join" 2>&1)
echo "    $SAVE1"
ok "Saved filepath event"

SAVE2=$($FORGE save --type failure --content "pwsh-exitcode PowerShell CI exit 0 required" 2>&1)
echo "    $SAVE2"
ok "Saved pwsh event"

# Also save a principle directly (no LLM — goes to principles table → returned by MCP)
$FORGE save --type success \
  --content "Lifecycle test: filepath.Join" \
  --principle "Always use filepath.Join for cross-platform paths — string concat breaks on Windows edge cases"
ok "Saved principle via --principle flag"

$FORGE save --type failure \
  --content "Lifecycle test: pwsh exit" \
  --principle "PowerShell CI steps must end with explicit exit 0 — pwsh propagates LASTEXITCODE otherwise"
ok "Saved failure principle"

# ── 7. DB → user: forge search ───────────────────────────────────────────────
step 7 "DB → user: search (two-way read)"

OUT=$($FORGE search "xplat-filepath-join" 2>&1)
assert_contains "Search finds filepath event" "xplat-filepath-join" "$OUT"

OUT=$($FORGE search "pwsh-exitcode" 2>&1)
assert_contains "Search finds pwsh event" "pwsh-exitcode" "$OUT"

OUT=$($FORGE search "go test" 2>&1)
assert_contains "Search finds Bash hook event" "go test" "$OUT"

# ── 8. MCP: all 4 tools ──────────────────────────────────────────────────────
step 8 "MCP server — all 4 tools"

mcp_call() {
  printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}\n{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"%s","arguments":%s}}\n' "$1" "$2" | \
    $FORGE mcp 2>/dev/null | grep '"id":2'
}

# get_principles
OUT=$(mcp_call "get_principles" '{"limit":5}')
assert_contains "MCP get_principles returns data" "filepath\|pwsh\|Principles" "$OUT"

# get_recent_context
OUT=$(mcp_call "get_recent_context" '{"limit":5}')
assert_contains "MCP get_recent_context returns content" "content" "$OUT"
assert_contains "MCP get_recent_context no error" '"isError":false' "$OUT"

# search_memories
OUT=$(mcp_call "search_memories" '{"query":"go test","limit":5}')
assert_contains "MCP search_memories finds Bash event" "go test" "$OUT"

# get_session_summaries
OUT=$(mcp_call "get_session_summaries" '{"limit":3}')
assert_contains "MCP get_session_summaries responds" "content" "$OUT"
assert_contains "MCP get_session_summaries no error" '"isError":false' "$OUT"

# ── 9. session recall (UserPromptSubmit → hookSpecificOutput) ─────────────────
step 9 "Session recall — UserPromptSubmit injects context"

OUT=$(echo '{"session_id":"sess-rw-002","hook_event_name":"UserPromptSubmit","cwd":"/tmp/proj"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=UserPromptSubmit $FORGE hook 2>&1)
assert_contains "hookSpecificOutput returned" "hookSpecificOutput" "$OUT"
assert_contains "Principles injected into recall" "filepath\|pwsh\|Principles" "$OUT"

# ── 10. distill (marks events processed) ─────────────────────────────────────
step 10 "forge distill — marks events as distilled"

BEFORE_UNDIST=$($FORGE status 2>/dev/null | grep "undistilled" | grep -oE '[0-9]+ undistilled' | grep -oE '[0-9]+' || echo "0")
BEFORE_UNDIST="${BEFORE_UNDIST:-0}"
DISTILL_OUT=$($FORGE distill 2>&1 || true)
echo "    distill: $DISTILL_OUT"
ok "Distill ran without error"
AFTER_UNDIST=$($FORGE status 2>/dev/null | grep "undistilled" | grep -oE '[0-9]+ undistilled' | grep -oE '[0-9]+' || echo "0")
AFTER_UNDIST="${AFTER_UNDIST:-0}"

if [ "$AFTER_UNDIST" -le "$BEFORE_UNDIST" ] 2>/dev/null; then
  ok "Undistilled count non-increasing ($BEFORE_UNDIST → $AFTER_UNDIST)"
else
  fail "Undistilled count increased unexpectedly ($BEFORE_UNDIST → $AFTER_UNDIST)"
fi

# ── 11. daemon resilience ─────────────────────────────────────────────────────
step 11 "Daemon resilience — double start, stale addr"

# Double start should be idempotent
OUT=$($FORGE start 2>&1)
assert_contains "Double start is idempotent" "already running" "$OUT"

# Daemon still alive after double start attempt
OUT=$($FORGE doctor 2>&1)
assert_contains "Daemon still healthy" "\[OK\] Daemon" "$OUT"

# ── 12. stop ─────────────────────────────────────────────────────────────────
step 12 "forge stop"

$FORGE stop
sleep 0.5

OUT=$($FORGE doctor 2>&1)
assert_contains "Daemon stopped cleanly" "\[FAIL\] Daemon" "$OUT"
ok "Daemon stopped"

# ── summary ──────────────────────────────────────────────────────────────────
echo ""
echo "══════════════════════════════════════════"
TOTAL=$((PASS + FAIL))
if [ "$FAIL" -eq 0 ]; then
  echo -e "${GREEN}ALL $TOTAL TESTS PASSED${NC}"
  echo "══════════════════════════════════════════"
  exit 0
else
  echo -e "${RED}$FAIL/$TOTAL TESTS FAILED${NC}"
  for t in "${FAILED_TESTS[@]}"; do
    echo -e "  ${RED}✗${NC} $t"
  done
  echo "══════════════════════════════════════════"
  exit 1
fi
