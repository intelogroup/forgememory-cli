# Forge — Silent Memory Forger

Forge captures your work sessions and distills them into lasting memories.

## How It Works

- **Capture**: Every tool use is logged automatically via hooks.
- **Distill**: The daemon runs every 10 minutes to summarize raw events into high-level insights.
- **Inject**: When you ask about past work, Forge provides context via the MCP tool.

## MCP Tools

### `get_recent_context`
Returns distilled memories and session summaries.

**When to use:**
- User asks "what was I doing before the break?"
- User asks "what did we work on yesterday?"
- User asks about past decisions or patterns

### `search_memories`
Full-text search on event payloads.

**When to use:**
- User asks "did I fix this before?"
- User asks "what errors have we seen?"

## Hooks

Forge hooks run automatically. You don't need to do anything — they capture:
- `PostToolUse` — every tool invocation
- `SessionEnd` — when the session closes
- `UserPrompt` — when the user sends a message

## Commands

```bash
forge status     # Check daemon and DB health
forge search "query"  # Search memories
forge distill    # Run distillation manually
```
