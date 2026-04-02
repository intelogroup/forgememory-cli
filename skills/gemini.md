# Forge — Silent Memory Forger

Forge captures your work sessions and distills them into lasting memories.

## How It Works

- **Capture**: Every tool use is logged automatically via hooks.
- **Distill**: The daemon runs every 10 minutes to summarize raw events into high-level insights.
- **Inject**: When you ask about past work, Forge provides context.

## Hooks

Forge hooks run automatically. You don't need to do anything — they capture:
- `AfterTool` — every tool invocation
- `AfterAgent` — when the agent completes

## Commands

```bash
forge status     # Check daemon and DB health
forge search "query"  # Search memories
forge distill    # Run distillation manually
```
