package main

import (
	"fmt"
	"os"
)

// version is set at build time via -ldflags "-X main.version=..."
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	// Handle flags before command dispatch
	if os.Args[1] == "--version" || os.Args[1] == "-v" {
		fmt.Printf("forge %s\n", version)
		os.Exit(0)
	}

	switch os.Args[1] {
	case "hook":
		runHook(os.Args[2:])
	case "daemon":
		runDaemon(os.Args[2:])
	case "init":
		runInit(os.Args[2:])
	case "sync-integrations":
		runSyncIntegrations(os.Args[2:])
	case "start":
		runStart(os.Args[2:])
	case "stop":
		runStop(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
	case "health":
		runHealth(os.Args[2:])
	case "distill":
		runDistill(os.Args[2:])
	case "memory":
		runMemory(os.Args[2:])
	case "harden":
		runHarden(os.Args[2:])
	case "search":
		runSearch(os.Args[2:])
	case "save":
		runSave(os.Args[2:])
	case "scan":
		runScan(os.Args[2:])
	case "commits":
		runCommits(os.Args[2:])
	case "streams":
		runStreams(os.Args[2:])
	case "steering":
		runSteering(os.Args[2:])
	case "profile":
		runProfile(os.Args[2:])
	case "knowledge-gap":
		runKnowledgeGap(os.Args[2:])
	case "coach":
		runCoach(os.Args[2:])
	case "prompt-doctor":
		runPromptDoctor(os.Args[2:])
	case "stats":
		runStats(os.Args[2:])
	case "mcp":
		runMCP(os.Args[2:])
	case "ui":
		runDashboard(os.Args[2:])
	case "doctor":
		runDoctor(os.Args[2:])
	case "service-install":
		runServiceInstall(os.Args[2:])
	case "service-uninstall":
		runServiceUninstall(os.Args[2:])
	case "service-start":
		runServiceStart(os.Args[2:])
	case "service-stop":
		runServiceStop(os.Args[2:])
	case "context":
		runContext(os.Args[2:])
	case "config":
		runConfig(os.Args[2:])
	case "synthesize-session":
		runSynthesizeSession(os.Args[2:]) // internal, not shown in help
	case "version":
		fmt.Printf("forge %s\n", version)
	case "agent-guide":
		runAgentGuide(os.Args[2:])
	case "inject-check":
		runInjectCheck(os.Args[2:]) // agent hook + pre-push git hook
	case "help", "--help", "-h":
		runHelp(os.Args[2:])
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
   sync-integrations Refresh Codex/Claude/Gemini MCP and hook integrations
   start         Start the daemon
   stop          Stop the daemon
   status        Show DB stats, daemon health, skill status (--json for machine-readable)
   health        Show distillation health and alerts
   save          Save a memory directly (--type --content [--principle])
   scan          Mine recent git history for learnings (--dry-run)
   distill       Run distillation manually (--all drains backlog, --wait queues behind running distill)
   memory        Manage distilled principles (list, delete, rate, thumbs-up/down)
   harden        Key lifecycle: rotate-key, revoke <id>
   context       Show recent work across all agents (sessions + principles + timeline)
   commits       Link git commits to the session active when they landed (--path --since)
   streams       Group sessions into multi-day work streams (--path --gap)
   steering      Show mid-task redirect rate per session (--path)
   profile       Score builder on 5 axes from local commits/streams/steering (--path)
   knowledge-gap Surface recurring technical knowledge gaps + vocab corrections (--path --all --vocab --json)
   coach         Review evidence-backed coaching items (status|list|explain|accept|defer|dismiss|review; --json for reads)
   prompt-doctor Surface recurring prompt anti-patterns + SCARF fix suggestions (--path --all --coach --json)
   stats         Fun aggregates: archetype, peak hour, top prompt words, agent-parallelism (--path)
   search <q>    Full-text search on event payloads
   mcp           Start MCP server (stdio transport, for Claude Code)
   ui            Start memory dashboard web UI (http://localhost:5555; traces, evaluations, artifacts)
   doctor        Self-test: check DB, daemon, agents, binary
     inject-check  Check staged changes against stored principles [--pre-push]
     config        Configure inference provider (--provider --api-key [--model --timeout --retries])
    agent-guide   Print copy-pasteable agent setup guide (CLAUDE.md / system prompt)
   hook          (internal) Hook entrypoint — called by agents
   daemon        (internal) Daemon entrypoint — long-running service
   version       Print version
   help          Print this help
   help mcp      Show MCP tool docs with params

 Agent setup:
   1. forge init            — detect agents, install hooks + skills
   2. forge start           — start background daemon
   3. Add MCP server:         {"forge": {"command": "forge", "args": ["mcp"]}}
   4. forge agent-guide     — copy-pasteable CLAUDE.md / system prompt block
   5. forge help mcp        — full MCP tool docs with params

 Provider setup:
   forge config --show
   forge config --provider openai --api-key sk-...
   forge config --provider anthropic --api-key sk-ant-...
   forge config --provider groq --api-key gsk-...
   forge config --provider ollama --model llama3:latest

 Environment:
   FORGE_SESSION_ID    Session identifier (set by hook)
   FORGE_SOURCE_TOOL   Source agent: claude/gemini/codex (set by hook)
   FORGE_EVENT_TYPE    Event type: PostToolUse/SessionEnd/UserPromptSubmit (set by hook)
   FORGE_TOOL_NAME     Tool name if applicable (set by hook)
   FORGE_OBSERVABILITY_MODE  minimal (default), standard, or forensic
   FORGE_TASK_ID        Optional evaluation task identifier
   FORGE_PIPE_ADDR     Daemon IPC address (set by daemon)
   FORGE_PROVIDER      Inference provider: anthropic/openai/groq/nvidia/ollama/codex
   FORGE_API_KEY       API key for inference provider
   FORGE_MODEL         Model override (provider default if omitted)
   FORGE_TIMEOUT       Inference timeout (e.g. 30s)
   FORGE_RETRIES       Retry attempts for transient failures
   FORGE_DISTILL_INTERVAL Daemon distillation interval (e.g. 10m)
 `)
}
