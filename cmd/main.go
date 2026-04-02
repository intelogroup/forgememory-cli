package main

import (
	"fmt"
	"os"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	switch os.Args[1] {
	case "hook":
		runHook(os.Args[2:])
	case "daemon":
		runDaemon(os.Args[2:])
	case "init":
		runInit(os.Args[2:])
	case "start":
		runStart(os.Args[2:])
	case "stop":
		runStop(os.Args[2:])
	case "status":
		statusOutput()
	case "distill":
		runDistill(os.Args[2:])
	case "search":
		runSearch(os.Args[2:])
	case "mcp":
		runMCP(os.Args[2:])
	case "doctor":
		runDoctor(os.Args[2:])
	case "version":
		fmt.Printf("forge %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`forge — silent memory forger for AI agents

Usage:
  forge <command> [args]

Commands:
  init          Detect agents, create DB, register hooks, install skills
  start         Start the daemon
  stop          Stop the daemon
  status        Show DB stats, daemon health, skill status
  distill       Run distillation manually
  search <q>    Full-text search on event payloads
  mcp           Start MCP server (stdio transport, for Claude Code)
  doctor        Self-test: check DB, daemon, agents, binary
  hook          (internal) Hook entrypoint — called by agents
  daemon        (internal) Daemon entrypoint — long-running service
  version       Print version
  help          Print this help

Environment:
  FORGE_SESSION_ID    Session identifier (set by hook)
  FORGE_SOURCE_TOOL   Source agent: claude/gemini/codex (set by hook)
  FORGE_EVENT_TYPE    Event type: PostToolUse/SessionEnd/UserPrompt (set by hook)
  FORGE_TOOL_NAME     Tool name if applicable (set by hook)
  FORGE_PIPE_ADDR     Daemon IPC address (set by daemon)
  FORGE_PROVIDER      Inference provider: anthropic/openai/ollama
  FORGE_API_KEY       API key for inference provider
`)
}
