# Forgememo - Silent Memory Layer for AI Agents

Silent background daemon and CLI that captures agent tool usage, distills engineering principles, and automatically injects relevant context into future sessions.

## Codebase Architecture

```
        ┌────────────────────────────────────────────────────────┐
        │                 AI Coding Agent (Client)               │
        │      (Claude Code, Gemini CLI, Codex, OpenCode)       │
        └───────────────────────────┬────────────────────────────┘
                                    │
                  Hook Events       │  MCP StdIO (Tools)
             (Tool Use, Prompts)    │  (get_principles, search)
                                    ▼
        ┌────────────────────────────────────────────────────────┐
        │                   forgememo CLI / IPC                  │
        │               (cmd/cli.go, cmd/hook.go)                │
        └───────────────────────────┬────────────────────────────┘
                                    │
                                    │ IPC Socket / Named Pipe
                                    ▼
        ┌────────────────────────────────────────────────────────┐
        │                    forgememo Daemon                    │
        │                     (cmd/daemon.go)                    │
        ├───────────────────────────┼────────────────────────────┤
        │  internal/scanner/        │  internal/distill/         │
        │  Agent detection          │  Inference engine          │
        ├───────────────────────────┼────────────────────────────┤
        │  internal/ipc/            │  internal/mcp/             │
        │  Connection listener      │  Model Context Protocol    │
        └───────────────────────────┬────────────────────────────┘
                                    │
                                    ▼
                        ┌───────────────────────┐
                        │       SQLite DB       │
                        │  (internal/db/db.go)  │
                        │  events & principles  │
                        └───────────────────────┘
```

## Directory Structure

- `cmd/`: CLI commands, hooks, daemon entrypoints.
- `internal/`:
  - `agent/`: Adapters for agent hook and skill configurations.
  - `config/`: Configuration handling (`~/.forge/config`).
  - `db/`: SQLite connection, migrations, CRUD logic, FTS5 indexes.
  - `distill/`: LLM distillation pipeline (mines raw logs to extract principles).
  - `ipc/`: Unix domain socket (POSIX) and named pipe (Windows) communication.
  - `mcp/`: MCP server implementation exposing memory tools.
  - `scanner/`: Auto-detection routines for active agent directories.
  - `service/`: Service manager (launchd/systemd/Windows scheduled tasks).
- `skills/`: Prompt and integration files loaded by agents.
- `npm/`: Node package wrapper.
- `payment/`: Payment server (auth, Stripe checkout, credits).

## Quick Install

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/intelogroup/forgememory-cli/main/install.sh | sh

# Windows
irm https://raw.githubusercontent.com/intelogroup/forgememory-cli/main/install.ps1 | iex
```

## Quick Start

```bash
forgememo init    # Detect agents and set up hooks
forgememo start   # Start background daemon
forgememo status  # Check daemon and memory health
```
