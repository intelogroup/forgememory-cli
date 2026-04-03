# Forge - Silent Memory Layer for AI Agents

Captures tool usage, distills insights into principles, and injects context for coding agents.

## Quick Install

### macOS / Linux
```bash
curl -fsSL https://raw.githubusercontent.com/intelogroup/forgememory-cli/main/install.sh | sh
```

### Windows
```powershell
irm https://raw.githubusercontent.com/intelogroup/forgememory-cli/main/install.ps1 | iex
```

Or download directly:
- [Windows x64](https://github.com/intelogroup/forgememory-cli/releases/latest/download/forge-windows-amd64.zip)
- [macOS Intel](https://github.com/intelogroup/forgememory-cli/releases/latest/download/forge-darwin-amd64.tar.gz)
- [macOS Apple Silicon](https://github.com/intelogroup/forgememory-cli/releases/latest/download/forge-darwin-arm64.tar.gz)
- [Linux x64](https://github.com/intelogroup/forgememory-cli/releases/latest/download/forge-linux-amd64.tar.gz)
- [Linux ARM](https://github.com/intelogroup/forgememory-cli/releases/latest/download/forge-linux-arm64.tar.gz)

## Usage

```bash
# Initialize
forge init

# Start daemon
forge start

# Configure inference (optional, free Ollama is default)
forge config --provider openai --api-key sk-...
forge login --email user --password pass --purchase  # Paid credits

# Check status
forge status

# Search memories
forge search "pattern"

# Open dashboard
forge ui
```

## Providers

| Provider | Cost | Setup |
|----------|------|-------|
| Ollama | Free | Just run `ollama serve` |
| OpenAI | Paid | `forge config --provider openai --api-key sk-...` |
| Anthropic | Paid | `forge config --provider anthropic --api-key sk-ant-...` |
| Forge Credits | Paid | `forge login --email user --password pass` |

## Commands

- `forge init` - Initialize database and hooks
- `forge start` / `forge stop` - Daemon lifecycle
- `forge status` - Show stats
- `forge search <query>` - Full-text search
- `forge distill` - Run distillation manually
- `forge ui` - Open memory dashboard
- `forge mcp` - Start MCP server
- `forge config` - Configure provider
- `forge login` - Login with credits

## License

MIT
