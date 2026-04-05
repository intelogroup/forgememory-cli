# Forgememo - Silent Memory Layer for AI Agents

Captures tool usage, distills insights into principles, and injects context for coding agents.

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
# Initialize
forgememo init

# Start daemon
forgememo start

# Configure inference (optional, Forgememo is default)
forgememo config --provider forgememo   # auto-cheapest, includes free tier
forgememo config --provider openai --api-key sk-...
forgememo login --email user --password pass --purchase  # Paid credits

# Check status
forgememo status
forgememo health

# Search memories
forgememo search "pattern"

# Open dashboard
forgememo ui
```

## Reporting

```bash
make downloads-report
```

The report compares npm download totals with GitHub release archive downloads by version and platform.

## Providers

| Provider | Cost | Setup |
|----------|------|-------|
| Forgememo | Cheapest | `forgememo config --provider forgememo` |
| Ollama | Free | Just run `ollama serve` |
| OpenAI | Paid | `forgememo config --provider openai --api-key sk-...` |
| Anthropic | Paid | `forgememo config --provider anthropic --api-key sk-ant-...` |
| Forgememo Credits | Paid | `forgememo login --email user --password pass` |

Defaults by provider:
- `forgememo`: `claude-haiku-4-5-20251001`
- `anthropic`: `claude-haiku-4-5-20251001`
- `openai`: `gpt-4o`
- `ollama`: `llama3:latest`

Priority when unset: `forgememo` first.

## Config File

`~/.forge/config` format:

```bash
FORGE_PROVIDER=anthropic
FORGE_API_KEY=sk-ant-...
FORGE_MODEL=claude-haiku-4-5-20251001
FORGE_TIMEOUT=30s
FORGE_RETRIES=3
FORGE_DISTILL_INTERVAL=10m
```

## JSON Output

Machine-readable output for agents:

```bash
forgememo status --json
forgememo search "query" --json
forgememo config --show --json
forgememo health
```

## Commands

- `forgememo init` - Initialize database and hooks
- `forgememo start` / `forgememo stop` - Daemon lifecycle
- `forgememo status` - Show stats
- `forgememo health` - Show distillation health and alerts
- `forgememo search <query>` - Full-text search
- `forgememo distill` - Run distillation manually
- `forgememo ui` - Open memory dashboard
- `forgememo mcp` - Start MCP server
- `forgememo config` - Configure provider
- `forgememo login` - Login with credits

## License

MIT
