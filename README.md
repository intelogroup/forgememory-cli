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

## Documentation Reference

### Configuration Precedence & Caching
Configuration is loaded using the following precedence (highest priority first):
1. **Environment Variables**: `FORGE_PROVIDER`, `FORGE_API_KEY`, `FORGE_MODEL`, `FORGE_BASE_URL` (Warning: The background daemon parses only `~/.forge/config` at startup).
2. **Project Local Config**: `.forge/config.local` (private/gitignored) and `.forge/config` (committed to repo).
3. **Global Config**: `~/.forge/config` (managed via `forge config`).

> [!NOTE]
> The background daemon **caches configuration at startup**. You must run `forge stop && forge start` to apply new configuration changes.

### Distillation Threshold
To maintain high-quality principles and avoid noise, the distillation engine requires **3+ semantically related events** or a completed session boundary before extracting principles. If `forge distill` returns 0 principles while events remain queued, this is normal behavior indicating the cluster density threshold was not met.

### Supported Providers
Supported providers include:
- `forgememo` / `openai` / `anthropic` / `groq` / `nvidia` / `ollama`
- `openrouter` (URL: `https://openrouter.ai/api/v1`)
- `antigravity` (Gemini CLI backend)

