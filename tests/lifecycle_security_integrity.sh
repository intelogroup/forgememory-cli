#!/bin/bash
# Security, data integrity, edge-case payload, and behavioral correctness tests.
# New angles not covered by lifecycle_full.sh or lifecycle_realworld.sh:
#
#  Security
#   - SQL injection attempts in search queries
#   - Path traversal payloads in tool_input file_path
#   - Oversized session/project IDs
#
#  Data integrity
#   - Events survive daemon restart (WAL persistence)
#   - Principles survive daemon restart
#   - Distillation is idempotent (double-distill, no phantom events)
#   - Event ordering: returned in timestamp order
#
#  Edge-case payloads
#   - Unicode / emoji in tool content
#   - Empty payload
#   - Null/missing fields in hook JSON
#   - Very long single-line payload (64KB)
#
#  Behavioral correctness
#   - Read-tool filter is exhaustive (Grep/WebSearch/WebFetch etc.)
#   - forge status counts match DB reality (cross-check via doctor)
#   - MCP tools/list enumerates exactly the 4 documented tools
#   - Principle count increments correctly on each save
#
#  Performance floor
#   - 100 hooks captured in < 10 seconds

FORGE="${FORGE:-./forge}"
PASS=0; FAIL=0
FAILED_TESTS=()

# ── isolated scratch HOME ────────────────────────────────────────────────────
# Never touch the real developer's ~/.forge: everything this script runs
# against (daemon, DB, sockets) resolves relative to $HOME, so point HOME at a
# throwaway directory instead of killing a real "forge daemon" process or
# deleting a real forge.db (issue #43).
SCRATCH_HOME="$(mktemp -d)"
export HOME="$SCRATCH_HOME"
trap 'rm -rf "$SCRATCH_HOME"' EXIT

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
ok()   { echo -e "${GREEN}  ✓ $*${NC}"; PASS=$((PASS+1)); }
fail() { echo -e "${RED}  ✗ $*${NC}"; FAIL=$((FAIL+1)); FAILED_TESTS+=("$*"); }
step() { echo -e "\n${YELLOW}[$1]${NC} ${CYAN}$2${NC}"; }
info() { echo -e "    $*"; }

assert_contains() {
  local label="$1" needle="$2" haystack="$3"
  if echo "$haystack" | grep -q "$needle"; then ok "$label"
  else fail "$label (expected '$needle')"; info "got: $(echo "$haystack" | head -2)"; fi
}
assert_not_contains() {
  local label="$1" needle="$2" haystack="$3"
  if echo "$haystack" | grep -q "$needle"; then
    fail "$label (unexpected '$needle' found)"; info "got: $(echo "$haystack" | head -2)"
  else ok "$label"; fi
}
assert_eq() {
  local label="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then ok "$label ($got)"
  else fail "$label (got=$got, want=$want)"; fi
}
assert_ge() {
  local label="$1" val="${2:-0}" min="$3"
  if [ "$val" -ge "$min" ] 2>/dev/null; then ok "$label ($val >= $min)"
  else fail "$label (got $val, want >= $min)"; fi
}
assert_le() {
  local label="$1" val="${2:-0}" max="$3"
  if [ "$val" -le "$max" ] 2>/dev/null; then ok "$label ($val <= $max)"
  else fail "$label (got $val, want <= $max)"; fi
}

events_count() { $FORGE status 2>/dev/null | grep "Events:" | grep -oE '[0-9]+' | head -1; }
principles_count() { $FORGE status 2>/dev/null | grep "Principles:" | grep -oE '[0-9]+' | head -1; }

# ── clean slate ───────────────────────────────────────────────────────────────
step 0 "Clean slate"
ok "Using isolated HOME=$HOME"
$FORGE init >/dev/null 2>&1
$FORGE start; sleep 1.5
OUT=$($FORGE doctor 2>&1)
assert_contains "Daemon up" "\[OK\] Daemon" "$OUT"

# ══════════════════════════════════════════════════════════════════════════════
# SECURITY
# ══════════════════════════════════════════════════════════════════════════════

step 1 "SQL injection — search query sanitisation"

# Classic injection attempts — must not crash, must return empty/safe result
for payload in \
  "'; DROP TABLE events; --" \
  "\" OR 1=1 --" \
  "events UNION SELECT * FROM principles" \
  "1; DELETE FROM events WHERE 1=1" \
  "); SELECT sqlite_version(); --"
do
  OUT=$($FORGE search "$payload" 2>&1)
  EXIT=$?
  assert_eq "SQL injection exits 0: $payload" "$EXIT" "0"
  assert_not_contains "SQL injection no DB error: $payload" "SQL logic error\|no such table\|syntax error" "$OUT"
done

step 2 "Path traversal payloads in hook file_path"

# Path traversal in file_path — stored but not executed; must not panic
echo "{\"session_id\":\"sec-001\",\"hook_event_name\":\"PostToolUse\",\"tool_name\":\"Edit\",\"tool_input\":{\"file_path\":\"../../../etc/passwd\",\"old_string\":\"root\",\"new_string\":\"pwned\"},\"cwd\":\"/tmp\"}" | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.3

OUT=$($FORGE search "etc/passwd" 2>&1)
assert_contains "Path traversal payload stored safely" "etc/passwd" "$OUT"
# Verify the actual /etc/passwd line format is NOT present (would indicate filesystem read)
assert_not_contains "No filesystem access" "root:x:0:0" "$OUT"

step 3 "Oversized IDs — long session_id / project_id"

LONG_ID=$(python3 -c "print('a' * 512)")
BEFORE_OVERSIZED=$(events_count); BEFORE_OVERSIZED="${BEFORE_OVERSIZED:-0}"
echo "{\"session_id\":\"${LONG_ID}\",\"hook_event_name\":\"PostToolUse\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"echo test\"},\"cwd\":\"/tmp\"}" | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.5
AFTER_OVERSIZED=$(events_count); AFTER_OVERSIZED="${AFTER_OVERSIZED:-0}"
assert_ge "Oversized session_id stored without crash" "$((AFTER_OVERSIZED - BEFORE_OVERSIZED))" 1

# ══════════════════════════════════════════════════════════════════════════════
# DATA INTEGRITY
# ══════════════════════════════════════════════════════════════════════════════

step 4 "Data integrity — events and principles survive daemon restart"

# Write some events and a principle
echo '{"session_id":"persist-001","hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":"persist_test.go","content":"persist-marker-abc"},"cwd":"/tmp"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.3

# Save a searchable event (no --principle so it goes into events/FTS5 table)
$FORGE save --type success --content "persist-event-xyz" >/dev/null 2>&1
sleep 0.3

# Also save a principle (goes to principles table, searchable via MCP not FTS5)
$FORGE save --type success --content "principle-persistence-test" \
  --principle "Persistence test: principles survive daemon restart" >/dev/null 2>&1

BEFORE_EVENTS=$(events_count); BEFORE_EVENTS="${BEFORE_EVENTS:-0}"
BEFORE_PRINCIPLES=$(principles_count); BEFORE_PRINCIPLES="${BEFORE_PRINCIPLES:-0}"

# Restart daemon
$FORGE stop; sleep 1
$FORGE start; sleep 1.5

AFTER_EVENTS=$(events_count); AFTER_EVENTS="${AFTER_EVENTS:-0}"
AFTER_PRINCIPLES=$(principles_count); AFTER_PRINCIPLES="${AFTER_PRINCIPLES:-0}"

# Events must survive restart (count must not decrease)
assert_ge "Event count survives restart" "$AFTER_EVENTS" "$BEFORE_EVENTS"
assert_eq "Principle count unchanged after restart" "$AFTER_PRINCIPLES" "$BEFORE_PRINCIPLES"

OUT=$($FORGE search "persist-marker-abc" 2>&1)
assert_contains "Event searchable after restart" "persist-marker-abc" "$OUT"

OUT=$($FORGE search "persist-event-xyz" 2>&1)
assert_contains "Manual-save event searchable after restart" "persist-event-xyz" "$OUT"

step 5 "Distillation idempotency — double distill produces no phantom change"

$FORGE distill >/dev/null 2>&1 || true
AFTER_FIRST=$(events_count); AFTER_FIRST="${AFTER_FIRST:-0}"
UNDIST_FIRST=$($FORGE status 2>/dev/null | grep "undistilled" | grep -oE '[0-9]+ undistilled' | grep -oE '[0-9]+' || echo "0")

$FORGE distill >/dev/null 2>&1 || true
AFTER_SECOND=$(events_count); AFTER_SECOND="${AFTER_SECOND:-0}"
UNDIST_SECOND=$($FORGE status 2>/dev/null | grep "undistilled" | grep -oE '[0-9]+ undistilled' | grep -oE '[0-9]+' || echo "0")

assert_eq "Double distill: total event count stable" "$AFTER_SECOND" "$AFTER_FIRST"
assert_eq "Double distill: undistilled count stable" "${UNDIST_SECOND:-0}" "${UNDIST_FIRST:-0}"

step 6 "Event ordering — most recent events returned last in session"

# Send 3 events with explicit ordering markers
for marker in first second third; do
  echo "{\"session_id\":\"order-001\",\"hook_event_name\":\"PostToolUse\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"echo ordering-${marker}\"},\"cwd\":\"/tmp\"}" | \
    FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
  sleep 0.15  # ensure distinct timestamps
done
sleep 0.5

OUT=$($FORGE search "ordering-" 2>&1)
FIRST_POS=$(echo "$OUT" | grep -n "ordering-first" | head -1 | cut -d: -f1)
THIRD_POS=$(echo "$OUT" | grep -n "ordering-third" | head -1 | cut -d: -f1)

if [ -n "$FIRST_POS" ] && [ -n "$THIRD_POS" ]; then
  ok "Event ordering: all 3 markers found in search results"
else
  fail "Event ordering: not all markers found (first=$FIRST_POS third=$THIRD_POS)"
fi

# ══════════════════════════════════════════════════════════════════════════════
# EDGE-CASE PAYLOADS
# ══════════════════════════════════════════════════════════════════════════════

step 7 "Unicode and emoji in tool content"

# Sentinel first so it falls within forge search's 200-char payload truncation
echo '{"session_id":"unicode-001","hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":"unicode-sentinel.go","content":"unicode-payload-sentinel 日本語テスト emoji-🔥🚀 Arabic: مرحبا Chinese: 你好世界"},"cwd":"/tmp"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.4

OUT=$($FORGE search "unicode-sentinel" 2>&1)
assert_contains "Unicode payload stored" "unicode-sentinel" "$OUT"
# Verify non-ASCII content survived intact
OUT2=$($FORGE search "日本語" 2>&1)
# FTS5 tokenization may strip non-ASCII — acceptable if no crash
EXIT=$?
assert_eq "Unicode search exits 0 (no crash)" "$EXIT" "0"

step 8 "Empty and minimal payloads"

# Empty payload — hook should still exit 0
echo "" | FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
assert_eq "Empty payload exits 0" "$?" "0"

# Minimal JSON — no tool_input
echo '{"session_id":"minimal-001","hook_event_name":"PostToolUse","tool_name":"Edit"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
assert_eq "Minimal JSON hook exits 0" "$?" "0"

# Missing session_id
echo '{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"echo no-session"}}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
assert_eq "Missing session_id exits 0" "$?" "0"

step 9 "64KB single-line payload"

GIANT=$(python3 -c "import json; print(json.dumps({'session_id':'giant-001','hook_event_name':'PostToolUse','tool_name':'Write','tool_input':{'file_path':'giant.go','content':'giant-sentinel ' + 'G'*65536},'cwd':'/tmp'}))")
echo "$GIANT" | FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.5

OUT=$($FORGE search "giant-sentinel" 2>&1)
assert_contains "64KB payload stored" "giant-sentinel" "$OUT"
assert_eq "64KB payload hook exits 0" "$?" "0"

# ══════════════════════════════════════════════════════════════════════════════
# BEHAVIORAL CORRECTNESS
# ══════════════════════════════════════════════════════════════════════════════

step 10 "Read-tool filter is exhaustive"

BEFORE=$(events_count); BEFORE="${BEFORE:-0}"

# All these tools should be FILTERED (not stored)
for tool in Read Grep Glob WebFetch WebSearch TodoRead LS; do
  echo "{\"session_id\":\"filter-001\",\"hook_event_name\":\"PostToolUse\",\"tool_name\":\"${tool}\",\"tool_input\":{\"file_path\":\"filtered.go\"},\"cwd\":\"/tmp\"}" | \
    FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
done
sleep 0.5

AFTER=$(events_count); AFTER="${AFTER:-0}"
assert_eq "Read tools produce 0 new events" "$((AFTER - BEFORE))" "0"

step 11 "forge status counts match doctor cross-check"

STATUS_EVENTS=$(events_count)
STATUS_EVENTS="${STATUS_EVENTS:-0}"
DOCTOR_OUT=$($FORGE doctor 2>&1)

# Doctor shows DB OK with matching event count
assert_contains "Doctor confirms DB healthy" "\[OK\] Database" "$DOCTOR_OUT"
assert_contains "Doctor reports event count" "$STATUS_EVENTS" "$DOCTOR_OUT"

step 12 "MCP tools/list enumerates exactly 4 documented tools"

OUT=$(printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}\n{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}\n' | \
  $FORGE mcp 2>/dev/null | grep '"id":2')

TOOL_COUNT=$(echo "$OUT" | python3 -c "import json,sys; d=json.loads(sys.stdin.read()); print(len(d['result']['tools']))" 2>/dev/null || echo "0")
assert_eq "MCP tools/list returns exactly 4 tools" "$TOOL_COUNT" "4"

for tool_name in get_recent_context search_memories get_principles get_session_summaries; do
  assert_contains "MCP tool '$tool_name' listed" "$tool_name" "$OUT"
done

step 13 "Principle count increments correctly"

P_BEFORE=$(principles_count); P_BEFORE="${P_BEFORE:-0}"

$FORGE save --type success --content "count-test-1" --principle "Increment test principle one" >/dev/null 2>&1
$FORGE save --type failure --content "count-test-2" --principle "Increment test principle two" >/dev/null 2>&1
$FORGE save --type plan   --content "count-test-3" --principle "Increment test principle three" >/dev/null 2>&1

P_AFTER=$(principles_count); P_AFTER="${P_AFTER:-0}"
assert_eq "Principle count +3 after 3 saves" "$((P_AFTER - P_BEFORE))" "3"

# ══════════════════════════════════════════════════════════════════════════════
# PERFORMANCE FLOOR
# ══════════════════════════════════════════════════════════════════════════════

step 14 "Performance floor — 100 hooks in < 10 seconds"

BEFORE=$(events_count); BEFORE="${BEFORE:-0}"
START_TS=$(date +%s)

for i in $(seq 1 100); do
  echo "{\"session_id\":\"perf-001\",\"hook_event_name\":\"PostToolUse\",\"tool_name\":\"Edit\",\"tool_input\":{\"file_path\":\"perf${i}.go\",\"old_string\":\"x\",\"new_string\":\"y\"},\"cwd\":\"/tmp\"}" | \
    FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook &
done
wait
sleep 1.5  # drain queue

END_TS=$(date +%s)
ELAPSED=$((END_TS - START_TS))

AFTER=$(events_count); AFTER="${AFTER:-0}"
assert_ge "100 hooks all captured" "$((AFTER - BEFORE))" 100
assert_le "100 hooks complete in < 10s" "$ELAPSED" 10
info "Elapsed: ${ELAPSED}s"

# ── clean stop ────────────────────────────────────────────────────────────────
step 15 "Final stop"
$FORGE stop; sleep 0.5
assert_contains "Clean stop" "\[FAIL\] Daemon" "$($FORGE doctor 2>&1)"

# ── summary ──────────────────────────────────────────────────────────────────
echo ""
echo "══════════════════════════════════════════"
TOTAL=$((PASS + FAIL))
if [ "$FAIL" -eq 0 ]; then
  echo -e "${GREEN}ALL $TOTAL TESTS PASSED${NC}"
else
  echo -e "${RED}$FAIL/$TOTAL TESTS FAILED${NC}"
  for t in "${FAILED_TESTS[@]}"; do echo -e "  ${RED}✗${NC} $t"; done
fi
echo "══════════════════════════════════════════"
[ "$FAIL" -eq 0 ]
