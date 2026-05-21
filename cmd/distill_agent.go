package main

import (
	"flag"
	"log"
	"os"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	"github.com/forge/forge/internal/mcp"
)

// runDistillAgent is an internal command spawned by the daemon distill loop.
// It gathers context via Forge MCP tools, then runs context-aware synthesis.
func runDistillAgent(args []string) {
	fs := flag.NewFlagSet("distill-agent", flag.ContinueOnError)
	sessionID := fs.String("session-id", "", "Session ID")
	projectID := fs.String("project-id", "", "Project ID")
	checkpointKind := fs.String("checkpoint-kind", "daemon", "Checkpoint kind")
	checkpointKey := fs.String("checkpoint-key", "", "Stable checkpoint key")
	cutoffTS := fs.String("cutoff-ts", "", "Optional timestamp cutoff")
	if err := fs.Parse(args); err != nil {
		log.Printf("distill-agent: flag parse error: %v", err)
		os.Exit(0)
	}

	if *sessionID == "" {
		os.Exit(0)
	}

	database, err := db.Open("")
	if err != nil {
		log.Printf("distill-agent: failed to open DB: %v", err)
		os.Exit(0)
	}
	defer database.Close()

	if *checkpointKey != "" {
		exists, err := database.HasSessionCheckpoint(*checkpointKey)
		if err == nil && exists {
			os.Exit(0)
		}
	}

	events, err := database.SessionEventsUpTo(*sessionID, *cutoffTS, 50)
	if err != nil {
		log.Printf("distill-agent: failed to get session events: %v", err)
		os.Exit(0)
	}
	if len(events) < 3 {
		_ = database.MarkSessionDistilled(*sessionID)
		os.Exit(0)
	}

	proj := *projectID
	if proj == "" {
		proj = events[0].ProjectID
	}

	runAt := time.Now().UTC()
	start := time.Now()

	cfg := distill.LoadConfig()
	d := distill.New(database, cfg)

	// Stage 1: Gather context via MCP (optional — fall back to context-free if unavailable)
	contextBlock := gatherMCPContext(proj)
	if contextBlock != "" {
		d.SetSessionContext(contextBlock)
		log.Printf("distill-agent: gathered context for %s (session %s)", proj, *sessionID)
	} else {
		log.Printf("distill-agent: no MCP context available, proceeding without context")
	}

	// Stage 2: Context-aware synthesis
	summary, err := d.SynthesizeCheckpoint(*sessionID, proj, *checkpointKind, *checkpointKey, events)
	if err != nil {
		log.Printf("distill-agent: synthesis failed: %v", err)
		_ = database.RecordDistillationFailure(runAt, time.Since(start), len(events), err.Error(), runAt)
		os.Exit(1)
	}
	if summary == nil {
		_ = database.MarkSessionDistilled(*sessionID)
		os.Exit(0)
	}

	count, err := d.DistillCheckpointSummary(*summary, events)
	if err != nil {
		log.Printf("distill-agent: principle extraction failed: %v", err)
		_ = database.RecordDistillationFailure(runAt, time.Since(start), len(events), err.Error(), runAt)
	}
	_ = database.MarkSessionDistilled(*sessionID)
	_ = database.RecordDistillationSuccess(runAt, time.Since(start), len(events), count, runAt)

	log.Printf("distill-agent: session %s → %d events, %d principle(s) (context: %t)", *sessionID, len(events), count, contextBlock != "")
	os.Exit(0)
}

// gatherMCPContext connects to Forge MCP and gathers existing knowledge about a project.
// Returns a formatted context block, or empty string if MCP is unavailable.
func gatherMCPContext(projectID string) string {
	client, err := mcp.NewClient()
	if err != nil {
		return ""
	}
	defer client.Close()

	args := map[string]any{"project_id": projectID}

	var sb strings.Builder
	sb.WriteString("[EXISTING KNOWLEDGE — what Forge already knows about this project]\n\n")

	// Gather principles
	if text, err := client.CallTool("get_principles", mergeArgs(args, map[string]any{"limit": float64(10)})); err == nil && text != "" {
		sb.WriteString("Existing principles:\n")
		sb.WriteString(formatMCPText(text, "  "))
		sb.WriteString("\n")
	}

	// Gather session summaries
	if text, err := client.CallTool("get_session_summaries", mergeArgs(args, map[string]any{"limit": float64(5)})); err == nil && text != "" {
		sb.WriteString("Past sessions:\n")
		sb.WriteString(formatMCPText(text, "  "))
		sb.WriteString("\n")
	}

	// Gather cross-session patterns
	if text, err := client.CallTool("get_cross_session_patterns", mergeArgs(args, map[string]any{"limit": float64(10)})); err == nil && text != "" {
		sb.WriteString("Cross-session patterns:\n")
		sb.WriteString(formatMCPText(text, "  "))
		sb.WriteString("\n")
	}

	result := strings.TrimSpace(sb.String())
	if result == "" {
		return ""
	}

	result += "\n\n[END EXISTING KNOWLEDGE]\n"
	result += "\nThe block above shows what we already know about this project. Use it to avoid duplicating existing knowledge. The new session events follow below.\n"
	return result
}

// formatMCPText indents and cleans up text from MCP tool responses.
func formatMCPText(text, indent string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip headers (##, ###) and separator lines (---, ___)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") && len(trimmed) > 1 && trimmed[1] == '-' || strings.HasPrefix(trimmed, "*") {
			continue
		}
		out = append(out, indent+trimmed)
	}
	return strings.Join(out, "\n") + "\n"
}

func mergeArgs(a, b map[string]any) map[string]any {
	result := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		result[k] = v
	}
	for k, v := range b {
		result[k] = v
	}
	return result
}
