package main

import "fmt"

func runHelp(args []string) {
	if len(args) == 1 && args[0] == "mcp" {
		fmt.Print(`forge help mcp — MCP tool reference

  get_recent_context
    When:    session start — prime context before any work
    Returns: session summaries, principles, active failures, cached external context
    Params:  limit (number, default 10), project_id (string, optional)

  search_memories
    When:    before solving an error or re-implementing a feature
    Returns: matching raw event payloads with recency-ranked results
    Params:  query (string, required), limit (number, default 10), project_id (string, optional)

  get_principles
    When:    before architecture or design decisions
    Returns: distilled high-level patterns and preferences for this project
    Params:  limit (number, default 10), project_id (string, optional)

  inject_principles
    When:    before sending a prompt to a code agent
    Returns: prompt with relevant past lessons prepended
    Params:  prompt (string, required), project_id (string, optional)

  get_session_summaries
    When:    when you need a narrative of recent work
    Returns: synthesized summaries of past sessions across all agents
    Params:  limit (number, default 5), project_id (string, optional)

  get_project_timeline
    When:    orienting on cross-agent history
    Returns: chronological session entries from Claude, Gemini, and Codex
    Params:  limit (number, default 10), project_id (string, optional)

  get_external_context
    When:    you need cached library/API docs for this project
    Returns: stored retrieval results only — no live fetches performed
    Params:  project_id, source, library_name, query (all optional strings), limit (number, default 5)

  get_active_failures
    When:    something is mysteriously broken
    Returns: unresolved failure alerts detected from prior sessions
    Params:  project_id (string, optional), limit (number, default 5)

  get_distillation_health
    When:    checking if distillation is running and healthy
    Returns: last run status, error message, and undistilled backlog count
    Params:  (none)

  get_alerts
    When:    checking for memory quality issues
    Returns: active distillation or backlog alerts
    Params:  (none)

  get_forge_status
    When:    forge seems silent or broken — no principles, hooks not firing, distillation failing
    Returns: daemon status, hook registration, event ingestion health, distillation health — with fix commands
    Params:  (none)

See 'forge agent-guide' for a copy-pasteable CLAUDE.md / system prompt block.
`)
		return
	}
	printUsage()
}

func runAgentGuide(_ []string) {
	fmt.Print(`# Forge Memory — Agent Guide

## MCP Tools

Add to your MCP config: {"forge": {"command": "forge", "args": ["mcp"]}}

Call these tools at the right moment:
  get_recent_context    -> session start (always)
  search_memories       -> before solving errors or re-implementing features
  get_principles        -> before architecture/design decisions
  inject_principles     -> before sending a prompt to a code agent (pass prompt as arg)
  get_active_failures   -> when something is mysteriously broken
  get_session_summaries -> when you need a narrative of recent work
  get_project_timeline  -> when orienting on cross-agent history
  get_external_context  -> when you need cached library/API docs

## Auto-Injection

At every session start, Forge auto-injects relevant past lessons into your context.
No tool call needed — if principles exist for the current project, you'll see them.

## Claude Code Skills (installed by 'forge init')

  /forge-distill   -> manually trigger distillation
  /forge-status    -> check daemon health and memory stats
  /forge-search    -> search memory from the chat interface

## Quick health check
  forge status     -> DB stats, daemon, distillation state
  forge doctor     -> self-test: DB, daemon, agents, binary

## Flags
  forge status --json   -> machine-readable status
  forge help mcp        -> full MCP tool docs with params
`)
}
