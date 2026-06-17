# Forge — Silent Memory Forger: Feasibility & Plan

## Feasibility Assessment (April 2026)

### Hook Support by Agent

| Agent | Hook System | How It Works | Two-Way Communication |
|-------|-------------|--------------|----------------------|
| **Claude Code** | ✅ Full | Shell command receives JSON on stdin, returns JSON on stdout. Events: SessionStart, PreToolUse, PostToolUse, Stop, SessionEnd. Also supports HTTP hooks (POST to URL). | ✅ Yes — hook can return context to Claude |
| **Gemini CLI** | ⚠️ Partial | MCP server integration, GEMINI.md context files. No explicit hook events documented like Claude Code. | ✅ Via MCP tools |
| **Codex** | ⚠️ Partial | CLI-based, no detailed hook docs found. Has config system. | ✅ Via skill files |

### Key Insight from Claude Code Docs

Claude Code hooks are **two-way**:
- Hook receives tool call context on stdin (JSON)
- Hook can return `additionalContext` that Claude sees
- Hook can block tool calls (`exit 2`)
- Hook can run async (background)

This means Forge can:
1. **Capture** every tool call (PostToolUse hook)
2. **Inject context** by returning distilled memories as additionalContext
3. **Block** dangerous operations based on past patterns

### OS Priority Recommendation

| OS | Priority | Why |
|----|----------|-----|
| **macOS** | 🥇 First | Claude Code is most mature here. Go cross-compiles trivially. Forgemo Python already works here. |
| **Linux** | 🥈 Second | Same as macOS. CI/CD environments. |
| **Windows** | 🥉 Third | Hooks need PowerShell. Named Pipes. More friction but still supported. |

### Go vs Python for macOS

**Go will work BETTER than Python on macOS:**

| Factor | Python (Forgemo) | Go (Forge) |
|--------|------------------|------------|
| Binary | Requires Python runtime | Single 10MB binary |
| Startup | ~200ms import overhead | ~1ms |
| Hook speed | Slow (Python import + Flask) | Fast (static binary) |
| Distribution | `pip install` | `brew install` |
| Cross-compile | Painful | `GOOS=darwin GOARCH=arm64` |

---

## Architecture

### Binary Architecture

One Go binary, three entrypoints via subcommands:

```
forge
├── forge hook        ← compiled for fast startup, static link
├── forge daemon      ← long-running service
└── forge             ← CLI wrapper (init, start, stop, status, distill)
```

Single binary. Cross-compiled for:
- `forge` (macOS ARM64)
- `forge` (Linux AMD64/ARM64)
- `forge.exe` (Windows AMD64)

### System Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│  Agent Layer                                                        │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                         │
│  │ Claude   │  │ Gemini   │  │ Codex    │                         │
│  │ Code     │  │ CLI      │  │          │                         │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘                         │
│       │              │              │                               │
│  ┌────┴─────┐  ┌────┴─────┐  ┌────┴─────┐                         │
│  │ MCP Tool │  │ Hook     │  │ Hook     │                         │
│  │ (stdio)  │  │ (yaml)   │  │ (yaml)   │                         │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘                         │
└───────┼──────────────┼──────────────┼───────────────────────────────┘
        │              │              │
   ┌────▼──────────────▼──────────────▼────┐
   │         IPC Channel                   │
   │  ┌─────────────────────────────────┐  │
   │  │ Unix Socket  (macOS/Linux)      │  │
   │  │ Named Pipe   (Windows)          │  │
   │  └─────────────────────────────────┘  │
   └────────────────┬──────────────────────┘
                    │
   ┌────────────────▼──────────────────────┐
   │         Forge Daemon (Go binary)      │
   │  ┌──────────┐  ┌──────────────────┐   │
   │  │ Event    │  │ Distillation     │   │
   │  │ Ingester │  │ Engine           │   │
   │  └────┬─────┘  └───────┬──────────┘   │
   │       │                │              │
   │  ┌────▼────────────────▼──────────┐   │
   │  │         SQLite + FTS5          │   │
   │  │  (local, encrypted at rest)    │   │
   │  └───────────────┬────────────────┘   │
   │                  │                    │
   │  ┌───────────────▼────────────────┐   │
   │  │         MCP Server             │   │
   │  │  (stdin/stdout transport)      │   │
   │  └────────────────────────────────┘   │
   └───────────────────────────────────────┘
```

### Data Model (SQLite + FTS5)

```sql
-- Raw events (the forge's raw material)
CREATE TABLE events (
    id          TEXT PRIMARY KEY,  -- UUID
    ts          TEXT NOT NULL,     -- ISO 8601
    session_id  TEXT NOT NULL,
    project_id  TEXT NOT NULL,
    source_tool TEXT NOT NULL,     -- claude/gemini/codex/copilot
    event_type  TEXT NOT NULL,     -- PostToolUse/SessionEnd/UserPrompt
    tool_name   TEXT,
    payload     TEXT NOT NULL,     -- JSON
    distilled   INTEGER DEFAULT 0  -- 0=raw, 1=processed
);

-- Distilled principles (the forged memories)
CREATE TABLE principles (
    id           TEXT PRIMARY KEY,
    ts           TEXT NOT NULL,
    type         TEXT NOT NULL,     -- architecture/bugfix/pattern/preference
    title        TEXT NOT NULL,
    narrative    TEXT NOT NULL,
    impact_score REAL DEFAULT 0.5,
    project_id   TEXT NOT NULL,
    source_event TEXT,              -- FK to events.id
    fingerprint  TEXT UNIQUE        -- hash(title + project) for dedup
);

-- Session summaries (context windows)
CREATE TABLE session_summaries (
    id         TEXT PRIMARY KEY,
    ts         TEXT NOT NULL,
    session_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    summary    TEXT NOT NULL,
    tokens     INTEGER DEFAULT 0
);

-- FTS5 full-text search
CREATE VIRTUAL TABLE events_fts USING fts5(
    payload, content=events, content_rowid=rowid
);
```

### Hook Flow (the "silent" part)

```
User runs: git commit -m "fix: Windows daemon"
         │
         ▼
Agent hook fires (PostToolUse)
         │
         ▼
forge hook --event PostToolUse --tool git
         │
         ├── Read stdin JSON (hook payload)
         ├── Serialize to {session_id, project_id, tool_name, payload}
         ├── Write to Named Pipe / Unix Socket
         │   └── If pipe closed: exit 0 in <1ms (silent failure)
         └── Exit 0
         │
         ▼
Daemon receives on pipe
         │
         ├── INSERT INTO events
         ├── Trigger distillation if threshold reached
         └── Return
```

**Key insight:** The hook never talks to SQLite. It only writes to a pipe. If the daemon is down, the pipe write fails silently. No broken workflow.

### The "Two-Way" Communication Flow

```
┌─────────────────────────────────────────────────────────────────┐
│  Claude Code Session                                           │
│                                                                 │
│  1. User submits prompt                                         │
│  2. Claude decides to run Bash tool                             │
│  3. PreToolUse hook fires → forge hook --event PreToolUse       │
│     └─> Reads JSON from stdin: {tool_name: "Bash", ...}        │
│     └─> Writes to daemon socket (or fails silently)             │
│     └─> Returns: exit 0 (allow) or exit 2 (block)              │
│                                                                 │
│  4. Bash tool executes                                          │
│  5. PostToolUse hook fires → forge hook --event PostToolUse     │
│     └─> Captures result                                         │
│     └─> Returns additionalContext if memories relevant          │
│                                                                 │
│  6. Claude sees additionalContext:                              │
│     "You've fixed this Windows daemon issue before.             │
│      The solution was: use CREATE_BREAKAWAY_FROM_JOB"           │
│                                                                 │
│  7. Stop hook fires → forge hook --event Stop                   │
│     └─> Triggers distillation of session events                 │
└─────────────────────────────────────────────────────────────────┘
```

### MCP Server (Context Injection)

Claude Code also supports MCP servers. Forge can run as an MCP server:

```json
// ~/.claude/settings.json
{
  "mcpServers": {
    "forge": {
      "command": "forge",
      "args": ["mcp"],
      "env": {}
    }
  }
}
```

Then Claude can call `get_recent_context()` and `search_memories()` tools directly.

### Agent Integration Points

| Agent | Hook Registration | Skill File | MCP |
|-------|-------------------|------------|-----|
| Claude Code | `~/.claude/settings.json` hooks | `~/.claude/skills/forge.md` | stdio MCP server |
| Gemini | `~/.gemini/settings.json` hooks | `~/.gemini/forge-skill.md` | (uses skill + hooks) |
| Codex | `~/.codex/settings.json` hooks | `~/.codex/skills/forge.json` | (uses skill + hooks) |

### CLI Commands

```
forge init              # Detect agents, create DB, register hooks, install skills
forge start             # Start daemon (launchd/systemd/Windows Service)
forge stop              # Stop daemon
forge status            # DB stats, daemon health, skill status
forge distill           # Run distillation manually
forge search "query"    # Search memories
forge config            # Set inference provider
forge doctor            # Self-test
forge mcp               # Start MCP server (stdio transport)
```

### Project Structure

```
forge/
├── cmd/
│   ├── main.go          # Entry point, subcommand routing
│   ├── hook.go          # Hook binary logic
│   ├── daemon.go        # Daemon binary logic
│   └── cli.go           # CLI commands (init, start, stop, status)
├── internal/
│   ├── db/
│   │   ├── db.go        # SQLite connection, migrations
│   │   ├── events.go    # Event CRUD
│   │   └── principles.go # Principle CRUD + FTS
│   ├── ipc/
│   │   ├── pipe.go      # Named pipe (Windows)
│   │   └── socket.go    # Unix socket (POSIX)
│   ├── distill/
│   │   └── distill.go   # LLM distillation logic
│   ├── mcp/
│   │   └── server.go    # MCP protocol handler
│   ├── agent/
│   │   ├── claude.go    # Claude Code integration
│   │   ├── gemini.go    # Gemini CLI integration
│   │   └── codex.go     # Codex integration
│   └── config/
│       └── config.go    # Provider config, env vars
├── skills/
│   ├── claude.md
│   ├── gemini.md
│   └── codex.json
├── go.mod
└── Makefile             # Cross-compilation targets
```

### Why SQLite

| Option | Concurrent Writes | FTS | Zero Deps | Vector Search | Verdict |
|--------|:-:|:-:|:-:|:-:|---------|
| **SQLite + FTS5** | ✓ WAL mode | ✓ Built-in | ✓ Single file | ❌ (need extension) | **v1 winner** |
| LanceDB | ✓ | ❌ | ❌ Heavy | ✓ Native | Overkill for events |
| JSON/JSONL | ❌ Corruption risk | ❌ Manual | ✓ | ❌ | Too simple |

### Why Go (not Rust)

| Factor | Go | Rust |
|--------|-----|------|
| Startup time | ~1ms | ~1ms |
| Binary size | ~8MB | ~5MB |
| Cross-compile | `GOOS=windows GOARCH=amd64` | Needs cross toolchain |
| SQLite | `modernc.org/sqlite` (pure Go) | `rusqlite` |
| MCP stdio | `encoding/json` + stdin | `serde` + stdin |
| Learning curve | Low | High |
| Dev speed | Fast | Slower |

### What Makes It "Silent"

1. **Hooks fail silently** — pipe write fails in <1ms if daemon is down
2. **No user prompts after init** — provider auto-detected or set via `forge config`
3. **Distillation runs in background** — never blocks the user
4. **MCP injection is on-demand** — agent requests context when it needs it
5. **No terminal output from hooks** — zero flicker

---

## Implementation Status

### ✅ Completed (v0.1.0)

| Component | Status | Notes |
|-----------|--------|-------|
| Go binary | ✓ | 10MB macOS, 11MB Windows |
| SQLite + FTS5 | ✓ | `modernc.org/sqlite` (pure Go) |
| Unix socket IPC | ✓ | macOS/Linux |
| TCP fallback IPC | ✓ | Windows |
| Hook command | ✓ | Silent failure in <1ms |
| Daemon command | ✓ | Listens on socket, writes to SQLite |
| Init command | ✓ | Creates DB, detects agents |
| Status command | ✓ | Shows DB stats |
| Search command | ✓ | FTS5 on event payloads |
| MCP server | ✓ | 3 tools: get_recent_context, search_memories, get_principles |
| Skill files | ✓ | Claude, Gemini, Codex |
| Cross-compile | ✓ | `GOOS=windows` just works |

### 🔲 Future Roadmap

| Feature / Improvement | Priority | Description |
|-----------|----------|-------------|
| Encryption at rest | High | Integrate SQLCipher for secure database storage |
| Multi-device sync | High | Secure cloud/peer synchronization of local memory data |
| Serialization hardening | High | Enforce strict schemas (JSON/Protobuf) for memories/state; ban loose engines (msgpack, pickle) |
| Enclave / Sandbox tool execution | High | Sandbox any future execution tools in isolated microVMs or containers |
| Structured type-matching boundaries | High | Pass all external code/log payloads through strict schemas to block command-injection |
| Web dashboard | Medium | Rich frontend dashboard (`forgememory-app`) for analytics and visualization |
| Rate limiting | Medium | Implement rate limiting on MCP server event loop |
| Ingestion-time payload sanitization | Medium | Strip sensitive data (e.g. credentials, tokens) before DB entry |

---

## Recommendation

1. **Start with macOS + Claude Code** — highest feasibility, most users
2. **Go approach is correct** — single binary, fast hooks, trivial cross-compile
3. **Two integration paths**: Hooks (capture/inject) + MCP (query tools)
4. **Gemini/Codex later** — add when their hook systems mature
