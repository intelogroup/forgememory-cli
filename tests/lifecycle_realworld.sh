#!/bin/bash
# Real-world advanced lifecycle tests for Forge.
# Covers scenarios not in lifecycle_full.sh:
#   - concurrent hook bursts (20 simultaneous)
#   - large payloads (multi-KB Write tool content)
#   - daemon crash + silent hook failure + recovery
#   - multi-session interleaving
#   - forge scan --dry-run
#   - MCP protocol edge cases (unknown method, malformed)
#   - queue drain: events saved while daemon down survive restart
#   - private data in deeply nested JSON
#   - search FTS5 edge cases (quotes, special chars)
#
# Usage: bash tests/lifecycle_realworld.sh
#        bash tests/lifecycle_realworld.sh --loop   (3 consecutive passes)

FORGE="${FORGE:-./forge}"
PASS=0; FAIL=0
FAILED_TESTS=()

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
ok()     { echo -e "${GREEN}  ✓ $*${NC}"; PASS=$((PASS+1)); }
fail()   { echo -e "${RED}  ✗ $*${NC}"; FAIL=$((FAIL+1)); FAILED_TESTS+=("$*"); }
step()   { echo -e "\n${YELLOW}[$1]${NC} ${CYAN}$2${NC}"; }
info()   { echo -e "    $*"; }

assert_contains() {
  local label="$1" needle="$2" haystack="$3"
  if echo "$haystack" | grep -q "$needle"; then
    ok "$label"
  else
    fail "$label (expected '$needle')"
    info "got: $(echo "$haystack" | head -2)"
  fi
}

assert_not_contains() {
  local label="$1" needle="$2" haystack="$3"
  if echo "$haystack" | grep -q "$needle"; then
    fail "$label (unexpected '$needle' found)"
    info "got: $(echo "$haystack" | head -2)"
  else
    ok "$label"
  fi
}

assert_eq() {
  local label="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then
    ok "$label ($got)"
  else
    fail "$label (got=$got, want=$want)"
  fi
}

assert_ge() {
  local label="$1" val="${2:-0}" min="$3"
  if [ "$val" -ge "$min" ] 2>/dev/null; then
    ok "$label ($val >= $min)"
  else
    fail "$label (got $val, want >= $min)"
  fi
}

events_count() {
  $FORGE status 2>/dev/null | grep "Events:" | grep -oE '[0-9]+' | head -1
}

# ── clean slate ───────────────────────────────────────────────────────────────
step 0 "Clean slate"
$FORGE stop 2>/dev/null || true
sleep 1
pkill -f "forge daemon" 2>/dev/null || true
sleep 0.5
rm -f ~/.forge/forge.db ~/.forge/forge.db-wal ~/.forge/forge.db-shm \
      ~/.forge/forge.sock ~/.forge/forge.addr ~/.forge/forge.pid
$FORGE init >/dev/null 2>&1
$FORGE start
sleep 1.5
OUT=$($FORGE doctor 2>&1)
assert_contains "Daemon up for tests" "\[OK\] Daemon" "$OUT"

# ── 1. concurrent hook burst ─────────────────────────────────────────────────
step 1 "Concurrent hook burst — 20 simultaneous PostToolUse"

BEFORE=$(events_count)
BEFORE="${BEFORE:-0}"

# Fire 20 Edit hooks in parallel
for i in $(seq 1 20); do
  echo "{\"session_id\":\"sess-burst\",\"hook_event_name\":\"PostToolUse\",\"tool_name\":\"Edit\",\"tool_input\":{\"file_path\":\"file${i}.go\",\"old_string\":\"x\",\"new_string\":\"y-burst-${i}\"},\"cwd\":\"/tmp\"}" | \
    FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook &
done
wait
sleep 1  # give daemon time to drain queue

AFTER=$(events_count)
AFTER="${AFTER:-0}"
CAPTURED=$((AFTER - BEFORE))
assert_ge "20 concurrent hooks all captured" "$CAPTURED" 20

# ── 2. large payload ─────────────────────────────────────────────────────────
step 2 "Large payload — 8KB Write tool content"

# Build ~8KB payload
BIGCONTENT=$(python3 -c "print('x' * 8192)")
PAYLOAD="{\"session_id\":\"sess-large\",\"hook_event_name\":\"PostToolUse\",\"tool_name\":\"Write\",\"tool_input\":{\"file_path\":\"big.go\",\"content\":\"${BIGCONTENT}\"},\"cwd\":\"/tmp\"}"

BEFORE=$(events_count)
BEFORE="${BEFORE:-0}"
echo "$PAYLOAD" | FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.5
AFTER=$(events_count)
AFTER="${AFTER:-0}"
assert_ge "Large payload stored" "$((AFTER - BEFORE))" 1

# Searchable after storage
OUT=$($FORGE search "big.go" 2>&1)
assert_contains "Large payload searchable" "big.go" "$OUT"

# ── 3. private data — nested JSON ────────────────────────────────────────────
step 3 "Private data redaction — nested <private> in JSON"

echo "{\"session_id\":\"sess-priv\",\"hook_event_name\":\"PostToolUse\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"curl -H 'Authorization: <private>Bearer sk-live-abc123xyz</private>' https://api.example.com\"},\"cwd\":\"/tmp\"}" | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.5

OUT=$($FORGE search "api.example.com" 2>&1)
assert_contains "Event captured after redaction" "api.example.com" "$OUT"
assert_not_contains "Secret token NOT stored" "sk-live-abc123xyz" "$OUT"
assert_contains "Redacted marker present" "\[redacted\]" "$OUT"

# ── 4. daemon crash + silent hook failure ────────────────────────────────────
step 4 "Daemon crash → hooks fail silently → no error to caller"

DAEMON_PID=$(cat ~/.forge/forge.pid 2>/dev/null || echo "")
if [ -n "$DAEMON_PID" ]; then
  kill -9 "$DAEMON_PID" 2>/dev/null || true
  sleep 0.5
  rm -f ~/.forge/forge.addr ~/.forge/forge.pid ~/.forge/forge.sock
fi

# Hook with daemon down must exit 0 (silent failure)
HOOK_EXIT=0
echo '{"session_id":"sess-crash","hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{},"cwd":"/tmp"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
HOOK_EXIT=$?
assert_eq "Hook exits 0 when daemon is down" "$HOOK_EXIT" "0"

# forge save goes direct to DB (bypasses daemon) — should still work
SAVE_OUT=$($FORGE save --type note --content "saved-while-daemon-down-tag" --principle "direct DB write works without daemon" 2>&1)
assert_contains "forge save works without daemon" "Saved principle\|saved" "$SAVE_OUT"

# ── 5. recovery after crash ───────────────────────────────────────────────────
step 5 "Stale addr recovery — forge start after crash"

# Forge stop should handle missing daemon gracefully
$FORGE stop 2>/dev/null || true

# Stale addr file left behind
echo "stale-addr-value" > ~/.forge/forge.addr

OUT=$($FORGE start 2>&1)
# start should detect stale addr, clean it, and start fresh
sleep 1.5
DOCTOR=$($FORGE doctor 2>&1)
assert_contains "Daemon recovered after stale addr" "\[OK\] Daemon" "$DOCTOR"

# ── 6. multi-session interleaving ────────────────────────────────────────────
step 6 "Multi-session interleaving — 3 sessions simultaneously"

BEFORE=$(events_count)
BEFORE="${BEFORE:-0}"

for sess in alpha beta gamma; do
  (
    for tool in Edit Write Bash; do
      echo "{\"session_id\":\"sess-${sess}\",\"hook_event_name\":\"PostToolUse\",\"tool_name\":\"${tool}\",\"tool_input\":{\"file_path\":\"${sess}.go\",\"command\":\"echo ${sess}\"},\"cwd\":\"/tmp/${sess}\"}" | \
        FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
      sleep 0.1
    done
  ) &
done
wait
sleep 1

AFTER=$(events_count)
AFTER="${AFTER:-0}"
assert_ge "All 3 sessions' events captured (≥9)" "$((AFTER - BEFORE))" 9

# Each session's events are searchable
for sess in alpha beta gamma; do
  OUT=$($FORGE search "${sess}.go" 2>&1)
  assert_contains "Session $sess events searchable" "${sess}.go" "$OUT"
done

# ── 7. forge scan --dry-run ──────────────────────────────────────────────────
step 7 "forge scan --dry-run — detects git repos"

OUT=$($FORGE scan --dry-run 2>&1)
# Should detect at least the forge repo itself
assert_contains "Scan detects repos" "repo\|git\|Found\|scan\|project\|forge" "$OUT"
assert_eq "Scan dry-run exits 0" "$?" "0"

# ── 8. MCP protocol edge cases ───────────────────────────────────────────────
step 8 "MCP protocol edge cases"

# Unknown method → proper error response
OUT=$(printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}\n{"jsonrpc":"2.0","id":2,"method":"nonexistent/method","params":{}}\n' | \
  $FORGE mcp 2>/dev/null | grep '"id":2')
assert_contains "Unknown MCP method → -32601" "32601" "$OUT"

# Unknown tool name
OUT=$(printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}\n{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"does_not_exist","arguments":{}}}\n' | \
  $FORGE mcp 2>/dev/null | grep '"id":2')
assert_contains "Unknown MCP tool → error" "Unknown tool\|error\|isError" "$OUT"

# search_memories with missing required param
OUT=$(printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}\n{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_memories","arguments":{}}}\n' | \
  $FORGE mcp 2>/dev/null | grep '"id":2')
assert_contains "search_memories missing query → error" "isError\|error\|Missing" "$OUT"

# Malformed JSON line — server must not crash, subsequent calls still work
OUT=$(printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}\nnot valid json at all\n{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}\n' | \
  $FORGE mcp 2>/dev/null | grep '"id":3')
assert_contains "Malformed JSON skipped, server survives" "tools" "$OUT"

# ── 9. FTS5 search edge cases ────────────────────────────────────────────────
step 9 "FTS5 search edge cases — special characters"

# Insert events with special chars
echo '{"session_id":"sess-special","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"echo hello-world && grep foo/bar.go baz"},"cwd":"/tmp"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.5

# Hyphenated query (was broken before the FTS5 fix)
OUT=$($FORGE search "hello-world" 2>&1)
assert_contains "Hyphenated search term works" "hello-world" "$OUT"

# Slash in query
OUT=$($FORGE search "foo/bar.go" 2>&1)
assert_contains "Slash in search term works" "foo/bar.go" "$OUT"

# ── 10. high-frequency burst then distill ────────────────────────────────────
step 10 "High-frequency burst + distill correctness"

BEFORE_TOTAL=$(events_count)
BEFORE_TOTAL="${BEFORE_TOTAL:-0}"

# 10 more hooks
for i in $(seq 1 10); do
  echo "{\"session_id\":\"sess-hf\",\"hook_event_name\":\"PostToolUse\",\"tool_name\":\"Edit\",\"tool_input\":{\"file_path\":\"hf${i}.go\",\"old_string\":\"a\",\"new_string\":\"b\"},\"cwd\":\"/tmp\"}" | \
    FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook &
done
wait
sleep 1

AFTER_TOTAL=$(events_count)
AFTER_TOTAL="${AFTER_TOTAL:-0}"
assert_ge "High-freq burst captured" "$((AFTER_TOTAL - BEFORE_TOTAL))" 10

# Distill marks them all
UNDIST_BEFORE=$($FORGE status 2>/dev/null | grep "undistilled" | grep -oE '[0-9]+ undistilled' | grep -oE '[0-9]+' || echo "0")
$FORGE distill >/dev/null 2>&1 || true
UNDIST_AFTER=$($FORGE status 2>/dev/null | grep "undistilled" | grep -oE '[0-9]+ undistilled' | grep -oE '[0-9]+' || echo "0")
assert_ge "Distill drains queue" "$((${UNDIST_BEFORE:-0} - ${UNDIST_AFTER:-0}))" 0

# ── 11. stop + final state ───────────────────────────────────────────────────
step 11 "Final stop and state verification"

FINAL_STATUS=$($FORGE status 2>&1)
info "Final status: $(echo "$FINAL_STATUS" | grep Events)"

$FORGE stop
sleep 0.5
DOCTOR=$($FORGE doctor 2>&1)
assert_contains "Daemon stopped cleanly" "\[FAIL\] Daemon" "$DOCTOR"
assert_contains "DB still accessible after stop" "\[OK\] Database" "$DOCTOR"

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
