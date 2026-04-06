#!/bin/bash
# Integration test: Simulate Claude Code session flow
# Tests: SessionStart → PostToolUse (x3) → Stop → SessionEnd → Distill → MCP query

set -e
FORGE="${FORGE:-./forge}"
BUILT_FORGE=0

cleanup() {
  if [ "$BUILT_FORGE" -eq 1 ]; then
    rm -f "$FORGE"
  fi
}
trap cleanup EXIT

if [ ! -x "$FORGE" ]; then
  echo "Building Forge test binary at $FORGE..."
  go build -o "$FORGE" ./cmd
  BUILT_FORGE=1
fi

echo "=== Forge Integration Test ==="
echo ""

# Clean slate
$FORGE stop > /dev/null 2>&1 || true
rm -f ~/.forge/forge.db ~/.forge/forge.db-shm ~/.forge/forge.db-wal ~/.forge/forge.sock
$FORGE init > /dev/null 2>&1

# Start daemon
echo "[1] Starting daemon..."
$FORGE start
sleep 1
$FORGE status
echo ""

# Simulate SessionStart hook
echo "[2] SessionStart hook → capture session start"
echo '{"session_id":"sess-001","hook_event_name":"SessionStart"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=SessionStart $FORGE hook
sleep 0.5

# Simulate PostToolUse hooks (user working)
echo "[3] PostToolUse #1 → Bash command"
echo '{"session_id":"sess-001","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"git status"}}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.5

echo "[4] PostToolUse #2 → Edit file"
echo '{"session_id":"sess-001","hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":"src/daemon.py","old_string":"serve_forever()","new_string":"polling loop"}}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.5

echo "[5] PostToolUse #3 → Write file"
echo '{"session_id":"sess-001","hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":"CHANGELOG.md","content":"## 0.4.13\\n- Fix daemon"}}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=PostToolUse $FORGE hook
sleep 0.5

# Check events captured
echo ""
echo "[6] Verify events captured..."
$FORGE status
EVENTS=$($FORGE status 2>/dev/null | grep "Events:" | awk '{print $2}')
if [ "$EVENTS" -ge 4 ]; then
  echo "  ✓ $EVENTS events captured"
else
  echo "  ✗ Expected ≥4 events, got $EVENTS"
  exit 1
fi
echo ""

# Simulate Stop hook
echo "[7] Stop hook → session ends"
echo '{"session_id":"sess-001","hook_event_name":"Stop"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=Stop $FORGE hook
sleep 0.5

# Simulate SessionEnd hook
echo "[8] SessionEnd hook → final cleanup"
echo '{"session_id":"sess-001","hook_event_name":"SessionEnd"}' | \
  FORGE_SOURCE_TOOL=claude FORGE_EVENT_TYPE=SessionEnd $FORGE hook
sleep 0.5

# Run distillation
echo ""
echo "[9] Distill events..."
$FORGE distill
$FORGE status
echo ""

# Query via MCP (what Claude would see)
echo "[10] MCP query: get_recent_context"
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_recent_context","arguments":{"limit":5}}}' | \
  $FORGE mcp 2>/dev/null | tail -1 | python3 -m json.tool 2>/dev/null || \
  echo '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_recent_context","arguments":{"limit":5}}}' | \
  $FORGE mcp 2>/dev/null | tail -1
echo ""

# Query via MCP: search
echo "[11] MCP query: search_memories for 'daemon'"
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_memories","arguments":{"query":"daemon","limit":5}}}' | \
  $FORGE mcp 2>/dev/null | tail -1 | python3 -m json.tool 2>/dev/null || \
  echo '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_memories","arguments":{"query":"daemon","limit":5}}}' | \
  $FORGE mcp 2>/dev/null | tail -1
echo ""

# CLI search
echo "[12] CLI search: 'Edit'"
$FORGE search "Edit"
echo ""

echo "=== Test Complete ==="
echo ""

# Final status
$FORGE status

# Cleanup
$FORGE stop
