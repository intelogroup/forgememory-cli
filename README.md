# Forgememo - Silent Memory Layer for AI Agents

Captures tool usage from Claude Code, Gemini, Codex CLI, and OpenCode — distills
insights into principles, cross-session patterns, and injects relevant context
into future sessions automatically.

## Quick Install

No npm required.

### macOS / Linux
```bash
curl -fsSL https://raw.githubusercontent.com/intelogroup/forgememory-cli/main/install.sh | sh
```

### Windows
```powershell
irm https://raw.githubusercontent.com/intelogroup/forgememory-cli/main/install.ps1 | iex
```

The installer adds both `forgememo` (primary) and `forge` (alias) commands.

Or download directly:
- [Windows x64](https://github.com/intelogroup/forgememory-cli/releases/latest/download/forge-windows-amd64.zip)
- [macOS Intel](https://github.com/intelogroup/forgememory-cli/releases/latest/download/forge-darwin-amd64.tar.gz)
- [macOS Apple Silicon](https://github.com/intelogroup/forgememory-cli/releases/latest/download/forge-darwin-arm64.tar.gz)
- [Linux x64](https://github.com/intelogroup/forgememory-cli/releases/latest/download/forge-linux-amd64.tar.gz)
- [Linux ARM](https://github.com/intelogroup/forgememory-cli/releases/latest/download/forge-linux-arm64.tar.gz)

## Usage

```bash
# Initialize — detects agents, creates DB, installs hooks + skills
forgememo init

# Start background daemon
forgememo start

# Configure inference (optional, Forgememo is default)
forgememo config --provider forgememo
forgememo config --provider openai --api-key sk-...

# Check memory
forgememo status          # events, principles, sessions, patterns
forgememo health          # distillation status + alerts
forgememo search "query"  # full-text search on tool-use logs

# Open dashboard
forgememo ui
```

## How It Works

```
Agent works → forge hook captures tool events → daemon stores in SQLite
                                                         ↓
                              Session ends? → forge distill-agent:
                              1. Gathers context via MCP tools
                                 (existing principles, past sessions, patterns)
                              2. Context-aware LLM synthesis
                                 (avoids duplicating what's already known)
                              3. Stores summary + new principles
                                                         ↓
                              Every 30 min → cross-session synthesis:
                                LLM analyzes recent summaries for
                                recurring failure modes, preferences, progress
                                                         ↓
                              Next session start → context injection:
                                Relevant past lessons auto-injected
                                into agent prompts
```

## Memory Architecture

| Layer | What | How |
|-------|------|-----|
| Raw events | Every tool call, prompt, file edit | Stored in SQLite, FTS5-indexed |
| Session summaries | Per-session narrative (goal, investigation, learnings) | LLM-synthesized on session end, keyword-tagged |
| Principles | Durable project-specific engineering knowledge | Extracted during synthesis, impact-scored, conflict-detected |
| Cross-session patterns | Recurring themes across 5+ sessions | Periodically mined from summaries |
| Context injection | Relevant memories auto-injected into agent prompts | On every UserPromptSubmit via hook + MCP |

The distill process is itself an **agent** — it calls forge's own MCP tools to look
up what it already knows before extracting new knowledge, avoiding duplicates and
producing higher-quality output than blind batch processing.

## Providers

| Provider | Cost | Setup |
|----------|------|-------|
| Forgememo | Cheapest (free tier) | `forgememo config --provider forgememo` |
| Ollama | Free | `ollama serve` + `forgememo config --provider ollama` |
| OpenAI | Paid | `forgememo config --provider openai --api-key sk-...` |
| Anthropic | Paid | `forgememo config --provider anthropic --api-key sk-ant-...` |
| Groq | Free tier | `forgememo config --provider groq --api-key gsk-...` |
| NVIDIA | Free tier / Paid | `forgememo config --provider nvidia --api-key nvapi-...` |

## MCP Tools

Forge exposes 12 tools via MCP (Model Context Protocol), auto-configured for
Claude Code, Gemini, Codex CLI, and OpenCode:

| Tool | When to call |
|------|-------------|
| `get_recent_context` | Session start — prime context |
| `search_memories` | Before solving errors or re-implementing |
| `get_principles` | Before architecture/design decisions |
| `get_session_summaries` | When you need a narrative of recent work |
| `get_cross_session_patterns` | To see recurring patterns over time |
| `get_project_timeline` | To orient on cross-agent history |
| `get_external_context` | To retrieve cached library/API docs |
| `get_active_failures` | When something is mysteriously broken |
| `inject_principles` | Before sending a prompt — enhances it with past lessons |

## Commands

- `forge init` — Detect agents, create DB, install hooks + skills
- `forge start` / `forge stop` — Daemon lifecycle
- `forge status` — Events, principles, session summaries, cross-session patterns
- `forge health` — Distillation health and backlog alerts
- `forge search <query>` — Full-text search on event payloads
- `forge distill` — Run distillation manually (`--all` drains backlog)
- `forge scan` — Mine recent git history for learnings
- `forge ui` — Memory dashboard (localhost:5555)
- `forge mcp` — Start MCP server (agents spawn this automatically)
- `forge doctor` — Self-test: DB, daemon, agents, binary
- `forge config` — Configure inference provider
- `forge login` — Login to Forgememo cloud

## License

MIT
